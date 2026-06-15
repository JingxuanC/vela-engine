package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
)

// ReviewVCIConsumer listens for review.replied events and updates customer profiles
// for the VCI (Vela Customer Intelligence) pipeline. Enriches customer_chat_profiles
// with negative review signals so the Sales Agent can personalize conversations.
type ReviewVCIConsumer struct {
	db *gorm.DB
}

// NewReviewVCIConsumer creates a new ReviewVCIConsumer.
func NewReviewVCIConsumer(db *gorm.DB) *ReviewVCIConsumer {
	return &ReviewVCIConsumer{db: db}
}

// Start subscribes to review.replied events.
func (c *ReviewVCIConsumer) Start(ctx context.Context, bus eventbus.EventBus) error {
	_, err := bus.Subscribe(eventbus.EventReviewReplied, c.handleReviewReplied)
	return err
}

type reviewRepliedPayload struct {
	PlatformID      string  `json:"platform_id"`
	CustomerEmail   string  `json:"customer_email"`
	CustomerID      string  `json:"customer_id"`
	ProductID       string  `json:"product_id"`
	Severity        string  `json:"severity"`
	Sentiment       string  `json:"sentiment"`
	Issues          string  `json:"issues"`
	DiscountCode    string  `json:"discount_code"`
	DiscountPercent float64 `json:"discount_percent"`
}

// handleReviewReplied writes negative review signals to customer_chat_profiles.
// Uses customer_id as the primary key (consistent with Sales Agent VCI).
func (c *ReviewVCIConsumer) handleReviewReplied(ctx context.Context, event *eventbus.Event) error {
	if c.db == nil {
		slog.Warn("review_vci: db is nil, skipping event")
		return nil
	}

	var payload reviewRepliedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		slog.Warn("review_vci: failed to unmarshal event payload", "err", err)
		return nil
	}

	if payload.CustomerID == "" {
		return nil // cannot associate with profile without customer_id
	}

	// Look up or create profile by shop_id + customer_id
	var profile model.CustomerChatProfile
	err := c.db.WithContext(ctx).
		Where("shop_id = ? AND customer_id = ?", event.ShopID, payload.CustomerID).
		First(&profile).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		profile = model.CustomerChatProfile{
			ShopID:     event.ShopID,
			CustomerID: payload.CustomerID,
		}
		if err := c.db.WithContext(ctx).Create(&profile).Error; err != nil {
			slog.Warn("review_vci: failed to create profile", "customer_id", payload.CustomerID, "err", err)
			return nil
		}
	} else if err != nil {
		slog.Warn("review_vci: failed to load profile", "customer_id", payload.CustomerID, "err", err)
		return nil
	}

	// Update profile with negative review signals
	updates := make(map[string]interface{})

	if payload.Severity != "" {
		updates["last_review_severity"] = payload.Severity
	}
	if payload.DiscountCode != "" {
		updates["compensation_sent"] = payload.DiscountCode
		updates["compensation_used"] = false
	}
	if payload.Issues != "" {
		// Store just the first issue for quick access; full issues in chat_intent_events
		issues := strings.Split(payload.Issues, ",")
		if len(issues) > 0 {
			updates["last_issue"] = strings.TrimSpace(issues[0])
		}
	}

	if len(updates) > 0 {
		if err := c.db.WithContext(ctx).Model(&profile).Updates(updates).Error; err != nil {
			slog.Warn("review_vci: failed to update profile", "customer_id", payload.CustomerID, "err", err)
		}
	}

	slog.Debug("review_vci: profile updated", "customer_id", payload.CustomerID, "severity", payload.Severity)

	// Update product-level review insight for merchant dashboard.
	c.upsertProductInsight(ctx, event.ShopID, &payload)

	return nil
}

// upsertProductInsight atomically increments product-level review counters.
// Uses INSERT ON CONFLICT (atomic create) + gorm.Expr increments (atomic update)
// so concurrent review.replied events on the same product won't lose counts.
func (c *ReviewVCIConsumer) upsertProductInsight(ctx context.Context, shopID uuid.UUID, payload *reviewRepliedPayload) {
	if payload.ProductID == "" {
		return
	}

	negIncr := 0
	if payload.Sentiment == "negative" {
		negIncr = 1
	}
	givenIncr := 0
	if payload.DiscountCode != "" {
		givenIncr = 1
	}

	// Step 1: Atomic INSERT-or-skip — ensures the row exists without racing.
	// DoNothing means concurrent inserts silently skip, no error.
	c.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "shop_id"}, {Name: "product_id"}},
		DoNothing: true,
	}).Create(&model.ProductReviewInsight{
		ShopID:    shopID,
		ProductID: payload.ProductID,
		Title:     payload.ProductID,
	})

	// Step 2: Atomic increments — gorm.Expr pushes arithmetic to the DB,
	// so concurrent writers each increment correctly without a read-then-write race.
	countUpdates := map[string]interface{}{
		"total_reviews": gorm.Expr("total_reviews + 1"),
	}
	if negIncr > 0 {
		countUpdates["negative_reviews"] = gorm.Expr("negative_reviews + 1")
	}
	if givenIncr > 0 {
		countUpdates["discounts_given"] = gorm.Expr("discounts_given + 1")
	}
	c.db.WithContext(ctx).Model(&model.ProductReviewInsight{}).
		Where("shop_id = ? AND product_id = ?", shopID, payload.ProductID).
		Updates(countUpdates)

	// Step 3: Best-effort issue tracking — merge into top_issues.
	// Non-atomic under extreme concurrency (two writers may race on the merge),
	// but counter accuracy is what matters for the dashboard.
	if payload.Issues != "" {
		var insight model.ProductReviewInsight
		c.db.WithContext(ctx).Where("shop_id = ? AND product_id = ?", shopID, payload.ProductID).
			First(&insight)
		c.db.WithContext(ctx).Model(&insight).
			Update("top_issues", mergeIssues(insight.TopIssues, payload.Issues))
	}
}

// mergeIssues merges new issues into the existing top_issues string,
// keeping only the first 5 unique issues (most recent first).
func mergeIssues(existing, newIssues string) string {
	parts := strings.Split(newIssues, ",")
	seen := make(map[string]bool)

	// Add new issues first
	var merged []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		merged = append(merged, p)
	}

	// Append existing issues that aren't duplicates
	for _, p := range strings.Split(existing, ",") {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		merged = append(merged, p)
	}

	if len(merged) > 5 {
		merged = merged[:5]
	}
	return strings.Join(merged, ", ")
}
