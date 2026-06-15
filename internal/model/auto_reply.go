package model

import (
	"time"

	"github.com/google/uuid"
)

// ── VCI Customer Profile (shared by Sales Agent + Auto Reply) ──
// NOTE: CustomerChatProfile is defined in models.go (the canonical definition).
// The auto_reply-specific fields (LastReviewSeverity, LastIssue, CompensationSent)
// should be added to the models.go version when needed.

// ── Auto Reply Recovery Tracking ──────────

// ReviewReplyCode maps a Shopify discount code back to the review reply that generated it.
// Used for attribution: when an order uses this code, revenue is attributed to auto reply.
type ReviewReplyCode struct {
	ID              uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID          uuid.UUID `gorm:"uniqueIndex:idx_rrc_shop_code;not null"`
	ReplyRecordID   uuid.UUID `gorm:"index;not null"` // FK to ReplyRecord
	Code            string    `gorm:"uniqueIndex:idx_rrc_shop_code;not null"` // e.g. "RV-A1B2-X7K9"
	ShopifyCodeID   string    // Shopify DiscountCode node ID
	DiscountPercent float64
	OrderTotal      float64 // Order total when code was used, set by webhook attribution
	ExpiresAt       time.Time
	IsUsed          bool `gorm:"default:false"`
	UsedAt          *time.Time
	CreatedAt       time.Time `gorm:"autoCreateTime"`
}

// ProductReviewInsight aggregates review feedback per product for merchant dashboards.
type ProductReviewInsight struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID    uuid.UUID `gorm:"uniqueIndex:idx_pri_shop_product;not null"`
	ProductID string    `gorm:"uniqueIndex:idx_pri_shop_product;not null"`
	Title     string

	TotalReviews     int
	NegativeReviews  int
	TopIssues        string
	DiscountsGiven   int
	DiscountsUsed    int
	RecoveredRevenue float64

	UpdatedAt time.Time `gorm:"autoUpdateTime"`
}

// ReviewRecoveryAttribution links a customer who received an auto-reply
// to their subsequent repurchase. Populated by order webhook.
// This is the core "value proof" table: did the AI reply actually bring them back?
type ReviewRecoveryAttribution struct {
	ID               uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID           uuid.UUID `gorm:"uniqueIndex:idx_rra_shop_email_order;not null"`
	CustomerEmail    string    `gorm:"uniqueIndex:idx_rra_shop_email_order;not null"`
	ReplyRecordID    uuid.UUID `gorm:"index;not null"`  // FK to ReplyRecord — the reply that was sent
	OriginalReviewID string    `gorm:"index"`           // platform review ID
	RepurchaseOrderID string   `gorm:"uniqueIndex:idx_rra_shop_email_order;not null"` // Shopify order ID
	RepurchaseAmount float64
	RepurchasedAt    time.Time
	CreatedAt        time.Time `gorm:"autoCreateTime"`
}
