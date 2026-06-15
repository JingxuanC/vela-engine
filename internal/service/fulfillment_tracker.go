// Package service provides business logic for the Vela AI API.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
)

// FulfillmentTracker queries Shopify Admin GraphQL for fulfillment tracking events
// and persists them to the local database.
type FulfillmentTracker struct {
	db         *gorm.DB
	apiVersion string
	httpClient *http.Client
	eventBus   eventbus.EventBus
}

// NewFulfillmentTracker creates a new FulfillmentTracker.
func NewFulfillmentTracker(db *gorm.DB, apiVersion string) *FulfillmentTracker {
	return &FulfillmentTracker{
		db:         db,
		apiVersion: apiVersion,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// SetEventBus sets the optional event bus for publishing fulfillment status changes.
func (t *FulfillmentTracker) SetEventBus(bus eventbus.EventBus) { t.eventBus = bus }

// shopifyAccessToken looks up the Shopify access token for a shop.
func (t *FulfillmentTracker) shopifyAccessToken(ctx context.Context, shopID uuid.UUID) (domain, token string, err error) {
	var shop model.Shop
	if err := t.db.WithContext(ctx).Where("id = ?", shopID).First(&shop).Error; err != nil {
		return "", "", fmt.Errorf("shop not found: %w", err)
	}
	return shop.ShopDomain, shop.AccessToken, nil
}

// ── GraphQL types ─────────────────────────────────────────────────────────────

type fulfillmentEventRaw struct {
	Status             string  `json:"status"`
	Message            string  `json:"message"`
	City               string  `json:"city"`
	Province           string  `json:"province"`
	Country            string  `json:"country"`
	HappenedAt         string  `json:"happenedAt"`
	EstimatedDeliveryAt *string `json:"estimatedDeliveryAt"`
}

type fulfillmentTrackingInfo struct {
	Number  string `json:"number"`
	URL     string `json:"url"`
	Company string `json:"company"`
}

type fulfillmentNode struct {
	ID           string                    `json:"id"`
	TrackingInfo []fulfillmentTrackingInfo `json:"trackingInfo"`
	Events       []struct {
		Node fulfillmentEventRaw `json:"node"`
	} `json:"edges"`
}

type orderFulfillmentsResponse struct {
	Data struct {
		Node struct {
			Fulfillments struct {
				Nodes []fulfillmentNode `json:"nodes"`
			} `json:"fulfillments"`
		} `json:"node"`
	} `json:"data"`
}

// SyncOrderFulfillments fetches fulfillment tracking data from Shopify Admin GraphQL
// and writes events into fulfillment_events, updating synced_orders tracking fields.
// orderGID: the Shopify Admin GraphQL GID for the order, e.g. "gid://shopify/Order/123456"
// platformID: the numeric Shopify order ID (used to find synced_orders)
func (t *FulfillmentTracker) SyncOrderFulfillments(ctx context.Context, shopID uuid.UUID, orderGID, platformID string) (int, error) {
	domain, token, err := t.shopifyAccessToken(ctx, shopID)
	if err != nil {
		return 0, err
	}
	if token == "" {
		return 0, fmt.Errorf("shop has no access token")
	}

	query := fmt.Sprintf(`{
		node(id: "%s") {
			... on Order {
				fulfillments(first: 10) {
					nodes {
						id
						trackingInfo {
							number
							url
							company
						}
						events(first: 50) {
							nodes: edges {
								node {
									status
									message
									city
									province
									country
									happenedAt
									estimatedDeliveryAt
								}
							}
						}
					}
				}
			}
		}
	}`, orderGID)

	body := map[string]interface{}{"query": query}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("marshal graphql body: %w", err)
	}

	url := fmt.Sprintf("https://%s/admin/api/%s/graphql.json", domain, t.apiVersion)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyJSON))
	if err != nil {
		return 0, fmt.Errorf("graphql request: %w", err)
	}
	req.Header.Set("X-Shopify-Access-Token", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("graphql request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf("graphql: %s — %s", resp.Status, string(b))
	}

	var result orderFulfillmentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode graphql response: %w", err)
	}

	// Find the synced_order by (shop_id, platform_id)
	var syncedOrder model.SyncedOrder
	if err := t.db.WithContext(ctx).
		Where("shop_id = ? AND platform_id = ?", shopID, platformID).
		First(&syncedOrder).Error; err != nil {
		slog.Warn("fulfillment_tracker: order not found in synced_orders", "platform_id", platformID, "err", err)
		// Don't fail — we can still write events, just can't set tracking on the order
	}

	eventCount := 0
	fulfillments := result.Data.Node.Fulfillments.Nodes

	// Track best tracking info (first fulfillment with tracking URL wins)
	var bestTrackingNumber, bestTrackingURL, bestCarrier string
	var latestEventStatus string
	var latestEventHappenedAt time.Time

	for _, f := range fulfillments {
		// Extract tracking info from the first fulfillment that has it
		if bestTrackingNumber == "" && len(f.TrackingInfo) > 0 {
			for _, ti := range f.TrackingInfo {
				if ti.Number != "" {
					bestTrackingNumber = ti.Number
					bestTrackingURL = ti.URL
					bestCarrier = ti.Company
					break
				}
			}
		}

		// Dedup: load existing events for this fulfillment
		var existingHappenAts []time.Time
		for _, edge := range f.Events {
			ht, err := time.Parse(time.RFC3339, edge.Node.HappenedAt)
			if err != nil {
				slog.Warn("fulfillment_tracker: parse happenedAt", "value", edge.Node.HappenedAt, "err", err)
				continue
			}
			existingHappenAts = append(existingHappenAts, ht)
		}

		// Check which events already exist (by fulfillment_id + status + happened_at dedup)
		var existingEvents []model.FulfillmentEvent
		if len(existingHappenAts) > 0 {
			t.db.WithContext(ctx).
				Where("fulfillment_id = ? AND happened_at IN ?", f.ID, existingHappenAts).
				Find(&existingEvents)
		}

		existingKeys := make(map[string]bool)
		for _, ee := range existingEvents {
			key := fmt.Sprintf("%s|%s|%d", ee.FulfillmentID, ee.Status, ee.HappenedAt.Unix())
			existingKeys[key] = true
		}

		for _, edge := range f.Events {
			e := edge.Node
			ht, err := time.Parse(time.RFC3339, e.HappenedAt)
			if err != nil {
				continue
			}
			key := fmt.Sprintf("%s|%s|%d", f.ID, e.Status, ht.Unix())
			if existingKeys[key] {
				continue // already exists
			}

			var estimatedAt *time.Time
			if e.EstimatedDeliveryAt != nil && *e.EstimatedDeliveryAt != "" {
				if eta, err := time.Parse(time.RFC3339, *e.EstimatedDeliveryAt); err == nil {
					estimatedAt = &eta
				}
			}

			fe := model.FulfillmentEvent{
				ShopID:              shopID,
				OrderID:             syncedOrder.ID,
				FulfillmentID:       f.ID,
				Status:              e.Status,
				Message:             e.Message,
				City:                e.City,
				Province:            e.Province,
				Country:             e.Country,
				HappenedAt:          ht,
				EstimatedDeliveryAt: estimatedAt,
			}

			if err := t.db.WithContext(ctx).Create(&fe).Error; err != nil {
				slog.Warn("fulfillment_tracker: failed to create event", "fulfillment_id", f.ID, "status", e.Status, "err", err)
				continue
			}
			eventCount++

			// Publish to EventBus so other services (push notification, AI) can react
			if t.eventBus != nil {
				eventPayload := map[string]interface{}{
					"order_id":           syncedOrder.PlatformID,
					"tracking_number":    bestTrackingNumber,
					"carrier":            bestCarrier,
					"status":             e.Status,
					"message":            e.Message,
					"city":               e.City,
					"province":           e.Province,
					"country":            e.Country,
					"happened_at":        ht.Format(time.RFC3339),
					"estimated_delivery": "",
				}
				if estimatedAt != nil {
					eventPayload["estimated_delivery"] = estimatedAt.Format(time.RFC3339)
				}
				payloadJSON, _ := json.Marshal(eventPayload)
				t.eventBus.Publish(ctx, &eventbus.Event{
					Type:    eventbus.EventFulfillmentStatusChanged,
					ShopID:  shopID,
					Payload: json.RawMessage(payloadJSON),
				})
			}

			// Track latest event for order update
			if ht.After(latestEventHappenedAt) {
				latestEventHappenedAt = ht
				latestEventStatus = e.Status
			}
		}
	}

	// Update synced_orders tracking fields
	if syncedOrder.ID != uuid.Nil && (bestTrackingNumber != "" || latestEventStatus != "") {
		updates := map[string]interface{}{}
		if bestTrackingNumber != "" {
			updates["tracking_number"] = bestTrackingNumber
			updates["tracking_url"] = bestTrackingURL
			updates["carrier"] = bestCarrier
		}
		if latestEventStatus != "" {
			updates["latest_event_status"] = latestEventStatus
		}

		// Set estimated_delivery_at from the latest event that has one
		for _, f := range fulfillments {
			for _, edge := range f.Events {
				e := edge.Node
				if e.EstimatedDeliveryAt != nil && *e.EstimatedDeliveryAt != "" {
					if eta, err := time.Parse(time.RFC3339, *e.EstimatedDeliveryAt); err == nil {
						updates["estimated_delivery_at"] = eta
						break
					}
				}
			}
		}

		if err := t.db.WithContext(ctx).Model(&syncedOrder).Updates(updates).Error; err != nil {
			slog.Warn("fulfillment_tracker: failed to update order tracking", "platform_id", platformID, "err", err)
		}
	}

	return eventCount, nil
}

// PollUnfulfilled polls Shopify for tracking on all orders that are:
// - partially or fully fulfilled but not delivered (latest_event_status != 'DELIVERED')
// - OR fulfillment_status indicates they may have tracking
// Runs every 4 hours (triggered by cron).
func (t *FulfillmentTracker) PollUnfulfilled(ctx context.Context, shopID uuid.UUID) (int, int, error) {
	_, token, err := t.shopifyAccessToken(ctx, shopID)
	if err != nil {
		return 0, 0, err
	}
	if token == "" {
		return 0, 0, fmt.Errorf("shop has no access token")
	}

	// Find orders that might have tracking but haven't been marked DELIVERED
	var orders []model.SyncedOrder
	if err := t.db.WithContext(ctx).
		Where("shop_id = ?", shopID).
		Where("fulfillment_status IN ?", []string{"fulfilled", "partial"}).
		Where("(latest_event_status = '' OR latest_event_status != 'DELIVERED' OR latest_event_status IS NULL)").
		Find(&orders).Error; err != nil {
		return 0, 0, fmt.Errorf("query unfulfilled orders: %w", err)
	}

	totalOrders := len(orders)
	totalEvents := 0

	for _, o := range orders {
		orderGID := fmt.Sprintf("gid://shopify/Order/%s", o.PlatformID)
		count, err := t.SyncOrderFulfillments(ctx, shopID, orderGID, o.PlatformID)
		if err != nil {
			slog.Warn("fulfillment_tracker: poll failed for order", "platform_id", o.PlatformID, "err", err)
			continue
		}
		totalEvents += count
	}

	return totalOrders, totalEvents, nil
}

// GetFulfillmentEvents returns all tracking events for an order.
func (t *FulfillmentTracker) GetFulfillmentEvents(ctx context.Context, shopID uuid.UUID, platformID string) ([]model.FulfillmentEvent, error) {
	// First find the synced_order
	var order model.SyncedOrder
	if err := t.db.WithContext(ctx).
		Where("shop_id = ? AND platform_id = ?", shopID, platformID).
		First(&order).Error; err != nil {
		return nil, fmt.Errorf("order not found: %w", err)
	}

	var events []model.FulfillmentEvent
	if err := t.db.WithContext(ctx).
		Where("order_id = ?", order.ID).
		Order("happened_at ASC").
		Find(&events).Error; err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}

	return events, nil
}
