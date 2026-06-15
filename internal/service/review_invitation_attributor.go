package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
)

// ReviewInvitationAttributor matches incoming synced reviews against pending/sent
// review invitations and marks them as converted.
type ReviewInvitationAttributor struct {
	db       *gorm.DB
	eventBus eventbus.EventBus
}

// NewReviewInvitationAttributor creates a new attributor.
func NewReviewInvitationAttributor(db *gorm.DB, bus eventbus.EventBus) *ReviewInvitationAttributor {
	return &ReviewInvitationAttributor{db: db, eventBus: bus}
}

// HandleReviewSynced is the EventBus handler for pipeline.review.synced events.
// It checks whether the review's email+product match any pending/sent invitation
// and marks it as converted.
func (a *ReviewInvitationAttributor) HandleReviewSynced(ctx context.Context, event *eventbus.Event) error {
	if event.Type != eventbus.EventReviewSynced {
		return nil
	}

	var payload struct {
		Platform      string `json:"platform"`
		PlatformID    string `json:"platform_id"`
		ProductID     string `json:"product_id"`
		Rating        int    `json:"rating"`
		ReviewerEmail string `json:"reviewer_email"`
		ReviewerName  string `json:"reviewer_name"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return nil // non-retryable parse error
	}

	if payload.ReviewerEmail == "" || payload.ProductID == "" {
		return nil
	}

	productID, err := strconv.ParseInt(payload.ProductID, 10, 64)
	if err != nil {
		return nil
	}

	// Find matching pending/sent invitations for this customer+product
	var invitations []model.ReviewInvitation
	if err := a.db.WithContext(ctx).
		Where("shop_id = ? AND customer_email = ? AND product_id = ? AND status IN ?",
			event.ShopID, payload.ReviewerEmail, productID, []string{"pending", "sent"}).
		Find(&invitations).Error; err != nil {
		return fmt.Errorf("review_attributor: query invitations: %w", err)
	}

	if len(invitations) == 0 {
		return nil // no matching invitation, not an attribution source
	}

	// Mark the first matching invitation as converted
	inv := invitations[0]
	reviewID, _ := strconv.ParseInt(payload.PlatformID, 10, 64)

	if err := a.db.WithContext(ctx).Model(&inv).Updates(map[string]interface{}{
		"status":    "converted",
		"review_id": reviewID,
	}).Error; err != nil {
		return fmt.Errorf("review_attributor: update invitation: %w", err)
	}

	slog.Info("review_attributor: invitation converted",
		"invitation_id", inv.ID,
		"review_platform_id", payload.PlatformID,
		"customer", inv.CustomerEmail,
		"rating", payload.Rating,
	)

	// Publish conversion event
	a.publishConverted(ctx, inv, reviewID, payload.Rating)

	return nil
}

// publishConverted publishes a best-effort review.invitation_converted event.
func (a *ReviewInvitationAttributor) publishConverted(ctx context.Context, inv model.ReviewInvitation, reviewID int64, rating int) {
	if a.eventBus == nil {
		return
	}
	payload := map[string]interface{}{
		"invitation_id":  inv.ID.String(),
		"order_id":       inv.OrderID,
		"product_id":     inv.ProductID,
		"customer_email": inv.CustomerEmail,
		"review_id":      reviewID,
		"rating":         rating,
	}
	ev, err := eventbus.NewEvent(eventbus.EventReviewInvitationConverted, inv.ShopID, payload, "review_attributor")
	if err != nil {
		slog.Error("review_attributor: failed to create event", "error", err)
		return
	}
	if err := a.eventBus.Publish(ctx, ev); err != nil {
		slog.Warn("review_attributor: failed to publish event", "error", err)
	}
}

// Start subscribes to EventReviewSynced on the EventBus and returns a cleanup function.
// Caller must call Start before StartConsumers is invoked on the bus.
func (a *ReviewInvitationAttributor) Start(ctx context.Context, bus eventbus.EventBus) error {
	_, err := bus.Subscribe(eventbus.EventReviewSynced, a.HandleReviewSynced)
	if err != nil {
		return fmt.Errorf("review_attributor: subscribe to EventReviewSynced: %w", err)
	}
	slog.Info("review_attributor: subscribed to EventReviewSynced")
	return nil
}
