package eventbus

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type EventType string

const (
	// Phase 2 unified pipeline events
	EventProductSynced  EventType = "pipeline.product.synced"
	EventOrderSynced    EventType = "pipeline.order.synced"
	EventCustomerSynced EventType = "pipeline.customer.synced"
	EventReturnSynced    EventType = "pipeline.return.synced"
	EventCheckoutSynced  EventType = "pipeline.checkout.synced"
	EventReviewSynced   EventType = "pipeline.review.synced"
	EventReviewReplied  EventType = "pipeline.review.replied"

	// Legacy events (keep for backward compatibility during migration)
	EventProductCreated      EventType = "product.created"
	EventProductUpdated      EventType = "product.updated"
	EventOrderCreated        EventType = "order.created"
	EventOrderUpdated        EventType = "order.updated"
	EventCustomerCreated     EventType = "customer.created"
	EventShopInstalled       EventType = "shop.installed"
	EventSubscriptionUpdated EventType = "billing.subscription.updated"
	EventAIInteraction       EventType = "ai.interaction"
	EventOrderRecovered      EventType = "cart_recovery.order_recovered"

	// Content Factory events
	EventContentGenerated  EventType = "content.generated"
	EventContentPublished  EventType = "content.published"
	EventContentAttributed EventType = "content.attributed"

	// Insight events
	EventInsightGenerated EventType = "insight.generated"

	// Fulfillment events
	EventFulfillmentStatusChanged EventType = "fulfillment.status_changed"

	// Review generation events
	EventOrderFulfilled           EventType = "order.fulfilled"
	EventReviewInvitationSent     EventType = "review.invitation_sent"
	EventReviewInvitationConverted EventType = "review.invitation_converted"
	EventReviewRevenueAttributed  EventType = "review.revenue_attributed"
)

// PipelineEventPayload is the unified event payload for all pipeline events.
type PipelineEventPayload struct {
	EntityID  string    `json:"entity_id"` // platform-internal ID (Shopify product ID, JudgeMe review ID, etc.)
	ShopID    string    `json:"shop_id"`
	Source    string    `json:"source"`  // "cron" | "webhook" | "manual"
	Action    string    `json:"action"`  // "upsert" | "delete"
	Timestamp string    `json:"ts"`
}

type Event struct {
	ID        uuid.UUID       `json:"id"`
	Type      EventType       `json:"type"`
	ShopID    uuid.UUID       `json:"shop_id"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
	Source    string          `json:"source"`
}

func NewEvent(t EventType, shopID uuid.UUID, payload interface{}, source string) (*Event, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("eventbus: marshal payload: %w", err)
	}
	return &Event{ID: uuid.New(), Type: t, ShopID: shopID, Timestamp: time.Now().UTC(), Payload: raw, Source: source}, nil
}
func (t EventType) StreamName() string  { return "eventbus:stream:" + string(t) }
func ConsumerGroupName(s string) string { return "eventbus:group:" + s }
