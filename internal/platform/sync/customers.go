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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CustomerSyncer struct {
	db       *gorm.DB
	client   *ShopifyRESTClient
	eventBus eventbus.EventBus
}

func NewCustomerSyncer(db *gorm.DB, c *ShopifyRESTClient, bus eventbus.EventBus) *CustomerSyncer {
	return &CustomerSyncer{db: db, client: c, eventBus: bus}
}
func (s *CustomerSyncer) EntityType() string { return "customers" }
func (s *CustomerSyncer) Sync(ctx context.Context, opts SyncOptions) (*SyncResult, error) {
	r := &SyncResult{}
	start := time.Now()
	defer func() { r.DurationMs = time.Since(start).Milliseconds() }()

	var shop model.Shop
	if err := s.db.First(&shop, "id = ?", opts.ShopID).Error; err != nil {
		return nil, fmt.Errorf("sync customers: %w", err)
	}

	params := url.Values{}
	params.Set("limit", "250")
	if !opts.FullSync && !opts.Since.IsZero() {
		params.Set("updated_at_min", opts.Since.Format(time.RFC3339))
	}

	page, err := s.client.GetPaginated(ctx, shop.ShopDomain, shop.AccessToken, "customers.json", params)
	if err != nil {
		return nil, err
	}

	const maxPages = 200 // safety limit: 200 pages × 250 = 50,000 customers
	for pageNum := 1; page != nil && pageNum <= maxPages; pageNum++ {
		// Check context cancellation between pages
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		default:
		}

		var w struct {
			Customers []struct {
				ID          int64     `json:"id"`
				Email       string    `json:"email"`
				FirstName   string    `json:"first_name"`
				LastName    string    `json:"last_name"`
				OrdersCount int       `json:"orders_count"`
				TotalSpent  string    `json:"total_spent"`
				Tags        string    `json:"tags"`
				UpdatedAt   time.Time `json:"updated_at"`
			} `json:"customers"`
		}
		if err := json.Unmarshal(page.Body, &w); err != nil {
			slog.Error("sync: unmarshal customers", "page", pageNum, "error", err)
			r.Errors++
			r.FailedPages++
			break
		}
		for _, c := range w.Customers {
			totalSpent := 0.0
			if c.TotalSpent != "" {
				if price, err := strconv.ParseFloat(strings.TrimSpace(c.TotalSpent), 64); err != nil {
					slog.Warn("sync: failed to parse customer total_spent", "customer_id", c.ID, "value", c.TotalSpent, "error", err)
				} else {
					totalSpent = price
				}
			}

			m := model.SyncedCustomer{
				ShopID:      opts.ShopID,
				PlatformID:  fmt.Sprintf("%d", c.ID),
				Email:       c.Email,
				FirstName:   c.FirstName,
				LastName:    c.LastName,
				OrdersCount: c.OrdersCount,
				TotalSpent:  totalSpent,
				Tags:        c.Tags,
			}
			res := s.db.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "shop_id"}, {Name: "platform_id"}},
				DoUpdates: clause.AssignmentColumns([]string{"email", "first_name", "last_name", "orders_count", "total_spent", "tags"}),
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
				if s.eventBus != nil {
					ev, evErr := eventbus.NewEvent(eventbus.EventCustomerSynced, shop.ID,
						eventbus.PipelineEventPayload{
							EntityID:  fmt.Sprintf("%d", c.ID),
							ShopID:    shop.ID.String(),
							Source:    "cron",
							Action:    "upsert",
							Timestamp: time.Now().UTC().Format(time.RFC3339),
						}, "sync/customers")
					if evErr == nil {
						if pubErr := s.eventBus.Publish(ctx, ev); pubErr != nil {
							slog.Warn("sync: failed to publish customer event", "customer_id", c.ID, "error", pubErr)
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
	slog.Info("sync", "pipeline", "customers", "shop_id", opts.ShopID.String(), "count", r.TotalRecords, "new", r.NewRecords, "updated", r.UpdatedRecords, "errors", r.Errors, "duration_ms", r.DurationMs)
	return r, nil
}
