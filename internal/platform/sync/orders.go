package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type OrderSyncer struct {
	db       *gorm.DB
	client   *ShopifyRESTClient
	eventBus eventbus.EventBus
}

func NewOrderSyncer(db *gorm.DB, c *ShopifyRESTClient, bus eventbus.EventBus) *OrderSyncer {
	return &OrderSyncer{db: db, client: c, eventBus: bus}
}
func (s *OrderSyncer) EntityType() string { return "orders" }
func (s *OrderSyncer) Sync(ctx context.Context, opts SyncOptions) (*SyncResult, error) {
	r := &SyncResult{}
	start := time.Now()
	defer func() { r.DurationMs = time.Since(start).Milliseconds() }()

	var shop model.Shop
	if err := s.db.First(&shop, "id = ?", opts.ShopID).Error; err != nil {
		return nil, fmt.Errorf("sync orders: %w", err)
	}

	params := url.Values{}
	params.Set("limit", "250")
	params.Set("status", "any")
	if !opts.FullSync && !opts.Since.IsZero() {
		params.Set("updated_at_min", opts.Since.Format(time.RFC3339))
	}

	page, err := s.client.GetPaginated(ctx, shop.ShopDomain, shop.AccessToken, "orders.json", params)
	if err != nil {
		return nil, err
	}

	const maxPages = 200
	for pageNum := 1; page != nil && pageNum <= maxPages; pageNum++ {
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		default:
		}

		var w struct {
			Orders []struct {
				ID                int64  `json:"id"`
				OrderNumber       int    `json:"order_number"`
				Email             string `json:"email"`
				TotalPrice        string `json:"total_price"`
				Currency          string `json:"currency"`
				FinancialStatus   string `json:"financial_status"`
				FulfillmentStatus string `json:"fulfillment_status"`
				LineItems         []struct {
					ID        int64  `json:"id"`
					Title     string `json:"title"`
					Quantity  int    `json:"quantity"`
					Price     string `json:"price"`
					SKU       string `json:"sku"`
					ProductID int64  `json:"product_id"`
					VariantID int64  `json:"variant_id"`
				} `json:"line_items"`
				ShippingAddress struct {
					Name     string `json:"name"`
					Address1 string `json:"address1"`
					City     string `json:"city"`
					Province string `json:"province"`
					Zip      string `json:"zip"`
					Country  string `json:"country"`
				} `json:"shipping_address"`
				Customer struct {
					FirstName string `json:"first_name"`
					LastName  string `json:"last_name"`
				} `json:"customer"`
				UpdatedAt time.Time `json:"updated_at"`
				CreatedAt string    `json:"created_at"`
			} `json:"orders"`
		}
		if err := json.Unmarshal(page.Body, &w); err != nil {
			slog.Error("sync: unmarshal orders", "page", pageNum, "error", err)
			r.Errors++
			r.FailedPages++
			break
		}
		for _, o := range w.Orders {
			totalPrice := 0.0
			if o.TotalPrice != "" {
				if price, err := strconv.ParseFloat(strings.TrimSpace(o.TotalPrice), 64); err != nil {
					slog.Warn("sync: failed to parse order price", "order_id", o.ID, "value", o.TotalPrice, "error", err)
				} else {
					totalPrice = price
				}
			}
			customerName := o.Customer.FirstName + " " + o.Customer.LastName
			lineItemsJSON, _ := json.Marshal(o.LineItems)
			shippingJSON, _ := json.Marshal(o.ShippingAddress)

			// Parse Shopify order creation time for RFM recency
			var orderCreatedAt *time.Time
			if o.CreatedAt != "" {
				if t, err := time.Parse(time.RFC3339, o.CreatedAt); err == nil {
					orderCreatedAt = &t
				}
			}

			m := model.SyncedOrder{
				ShopID:            opts.ShopID,
				PlatformID:        fmt.Sprintf("%d", o.ID),
				OrderNumber:       o.OrderNumber,
				CustomerEmail:     o.Email,
				CustomerName:      customerName,
				TotalPrice:        totalPrice,
				Currency:          o.Currency,
				FinancialStatus:   o.FinancialStatus,
				FulfillmentStatus: o.FulfillmentStatus,
				LineItems:         datatypes.JSON(lineItemsJSON),
				ShippingAddress:   datatypes.JSON(shippingJSON),
				OrderCreatedAt:     orderCreatedAt,
			}
			res := s.db.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "shop_id"}, {Name: "platform_id"}},
				DoUpdates: clause.AssignmentColumns([]string{"order_number", "customer_email", "customer_name", "total_price", "currency", "financial_status", "fulfillment_status", "line_items", "shipping_address", "order_created_at"}),
			}).Create(&m)
			if res.Error != nil {
				r.Errors++
			} else {
				if res.RowsAffected == 1 {
					r.NewRecords++
				} else {
					r.UpdatedRecords++
				}
				r.TotalRecords++
				// Only publish on success — matching ProductSyncer behavior
				if s.eventBus != nil {
					ev, evErr := eventbus.NewEvent(eventbus.EventOrderSynced, shop.ID,
						eventbus.PipelineEventPayload{
							EntityID:  fmt.Sprintf("%d", o.ID),
							ShopID:    shop.ID.String(),
							Source:    "cron",
							Action:    "upsert",
							Timestamp: time.Now().UTC().Format(time.RFC3339),
						}, "sync/orders")
					if evErr == nil {
						if pubErr := s.eventBus.Publish(ctx, ev); pubErr != nil {
							slog.Warn("sync: failed to publish order event", "order_id", o.ID, "error", pubErr)
						}
					}
				}
			}
		}
		if page.NextPageURL == "" {
			break
		}
		var nextErr error
		page, nextErr = s.client.GetNextPage(ctx, page.NextPageURL, shop.AccessToken)
		if nextErr != nil {
			slog.Error("sync: next page failed", "page", pageNum+1, "url", page.NextPageURL, "error", nextErr)
			r.Errors++
			r.FailedPages++
			break
		}
	}
	slog.Info("sync", "pipeline", "orders", "shop_id", opts.ShopID.String(), "count", r.TotalRecords, "new", r.NewRecords, "updated", r.UpdatedRecords, "errors", r.Errors, "duration_ms", r.DurationMs)
	return r, nil
}
