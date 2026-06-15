package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
)

const reviewAttributionWindowDays = 30

// ReviewRevenueAttributor matches incoming orders against review invitations
// and attributes revenue when a customer who received an invitation makes a purchase.
type ReviewRevenueAttributor struct {
	db       *gorm.DB
	eventBus eventbus.EventBus
}

// NewReviewRevenueAttributor creates a new ReviewRevenueAttributor.
func NewReviewRevenueAttributor(db *gorm.DB, bus eventbus.EventBus) *ReviewRevenueAttributor {
	return &ReviewRevenueAttributor{db: db, eventBus: bus}
}

// syncedOrderPayload mirrors model.SyncedOrder for unmarshalling from event payload.
type syncedOrderPayload struct {
	ID          uuid.UUID       `json:"id"`
	ShopID      uuid.UUID       `json:"shop_id"`
	PlatformID  string          `json:"platform_id"`
	OrderNumber int             `json:"order_number"`
	CustomerEmail string        `json:"customer_email"`
	CustomerName  string        `json:"customer_name"`
	TotalPrice    float64       `json:"total_price"`
	Currency      string        `json:"currency"`
	LineItems     json.RawMessage `json:"line_items"`
}

// HandleOrderCreated is the EventBus handler for EventOrderCreated and EventOrderUpdated.
func (a *ReviewRevenueAttributor) HandleOrderCreated(ctx context.Context, event *eventbus.Event) error {
	if event.Type != eventbus.EventOrderCreated && event.Type != eventbus.EventOrderUpdated {
		return nil
	}

	var order syncedOrderPayload
	if err := json.Unmarshal(event.Payload, &order); err != nil {
		slog.Warn("review_revenue_attrib: failed to parse order payload", "error", err)
		return nil
	}

	if order.CustomerEmail == "" {
		return nil
	}

	var items []lineItem
	if err := json.Unmarshal(order.LineItems, &items); err != nil {
		slog.Warn("review_revenue_attrib: failed to parse line_items", "order_id", order.PlatformID, "error", err)
		return nil
	}

	if len(items) == 0 {
		return nil
	}

	now := time.Now().UTC()
	windowStart := now.Add(-reviewAttributionWindowDays * 24 * time.Hour)

	for _, item := range items {
		if item.ProductID == 0 {
			continue
		}

		// Find matching review invitations for this customer+product sent within the attribution window
		var invitations []model.ReviewInvitation
		err := a.db.WithContext(ctx).
			Where("shop_id = ? AND customer_email = ? AND product_id = ? AND status IN ? AND sent_at BETWEEN ? AND ?",
				event.ShopID, order.CustomerEmail, item.ProductID, []string{"sent", "converted"}, windowStart, now).
			Find(&invitations).Error
		if err != nil {
			slog.Warn("review_revenue_attrib: query invitations failed",
				"shop_id", event.ShopID, "customer", order.CustomerEmail, "product_id", item.ProductID, "error", err)
			continue
		}

		if len(invitations) == 0 {
			continue
		}

		for _, inv := range invitations {
			// Calculate per-line-item attribution amount (not full order total)
			attrRevenue := order.TotalPrice
			if item.Price != "" {
				itemPrice, err := parsePrice(item.Price)
				if err == nil && itemPrice > 0 {
					qty := item.Quantity
					if qty <= 0 {
						qty = 1
					}
					attrRevenue = itemPrice * float64(qty)
				}
			}

			// Insert attribution record (idempotent via unique constraint)
			attr := model.ReviewRevenueAttribution{
				ShopID:        event.ShopID,
				InvitationID:  inv.ID,
				OrderID:       order.PlatformID,
				CustomerEmail: order.CustomerEmail,
				ProductID:     item.ProductID,
				Revenue:       attrRevenue,
			}

			result := a.db.WithContext(ctx).Where(
				"shop_id = ? AND invitation_id = ? AND order_id = ?",
				attr.ShopID, attr.InvitationID, attr.OrderID,
			).FirstOrCreate(&attr)

			if result.Error != nil {
				slog.Warn("review_revenue_attrib: failed to insert attribution",
					"invitation_id", inv.ID, "order_id", order.PlatformID, "error", result.Error)
				continue
			}

			if result.RowsAffected == 0 {
				// Already exists (duplicate, skip counter update)
				continue
			}

			// Update invitation aggregated counters
			if err := a.db.WithContext(ctx).Model(&model.ReviewInvitation{}).
				Where("id = ?", inv.ID).
				Updates(map[string]interface{}{
					"attributed_revenue": gorm.Expr("attributed_revenue + ?", order.TotalPrice),
					"attributed_orders":  gorm.Expr("attributed_orders + 1"),
				}).Error; err != nil {
				slog.Warn("review_revenue_attrib: failed to update invitation counters",
					"invitation_id", inv.ID, "error", err)
			}

			// Publish attribution event
			a.publishAttributed(ctx, inv, order.PlatformID, item.ProductID, order.TotalPrice)

			slog.Info("review_revenue_attrib: revenue attributed",
				"invitation_id", inv.ID,
				"order_id", order.PlatformID,
				"product_id", item.ProductID,
				"revenue", order.TotalPrice,
				"customer", order.CustomerEmail,
			)
		}
	}

	return nil
}

// publishAttributed publishes a best-effort review.revenue_attributed event.
func (a *ReviewRevenueAttributor) publishAttributed(ctx context.Context, inv model.ReviewInvitation, orderID string, productID int64, revenue float64) {
	if a.eventBus == nil {
		return
	}
	payload := map[string]interface{}{
		"invitation_id":  inv.ID.String(),
		"order_id":       orderID,
		"product_id":     productID,
		"customer_email": inv.CustomerEmail,
		"revenue":        revenue,
	}
	ev, err := eventbus.NewEvent(eventbus.EventReviewRevenueAttributed, inv.ShopID, payload, "review_revenue_attributor")
	if err != nil {
		slog.Error("review_revenue_attrib: failed to create event", "error", err)
		return
	}
	if err := a.eventBus.Publish(ctx, ev); err != nil {
		slog.Warn("review_revenue_attrib: failed to publish event", "error", err)
	}
}

// Start subscribes to EventOrderCreated and EventOrderUpdated on the EventBus.
func (a *ReviewRevenueAttributor) Start(ctx context.Context, bus eventbus.EventBus) error {
	if _, err := bus.Subscribe(eventbus.EventOrderCreated, a.HandleOrderCreated); err != nil {
		return fmt.Errorf("review_revenue_attrib: subscribe to EventOrderCreated: %w", err)
	}
	if _, err := bus.Subscribe(eventbus.EventOrderUpdated, a.HandleOrderCreated); err != nil {
		return fmt.Errorf("review_revenue_attrib: subscribe to EventOrderUpdated: %w", err)
	}
	slog.Info("review_revenue_attributor: subscribed to EventOrderCreated + EventOrderUpdated")
	return nil
}

// parsePrice parses a Shopify price string (e.g. "19.99") to float64.
func parsePrice(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty price")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("parse price %q: %w", s, err)
	}
	return f, nil
}
