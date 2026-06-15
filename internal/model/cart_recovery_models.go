package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// SyncedCheckout stores Shopify checkout data synced via webhook.
// Composite unique on (shop_id, platform_token) for UPSERT behavior.
type SyncedCheckout struct {
	ID            uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID        uuid.UUID      `gorm:"not null;uniqueIndex:idx_shop_checkout_token"`
	PlatformToken string         `gorm:"not null;uniqueIndex:idx_shop_checkout_token"` // Shopify checkout token
	CustomerEmail string         `gorm:"index"`
	CustomerName  string
	TotalPrice    float64
	Currency      string         `gorm:"default:USD"`
	LineItems     datatypes.JSON `gorm:"type:jsonb"`
	Status        string         `gorm:"default:open;index"` // open, abandoned, recovered, ordered, expired
	AbandonedAt   *time.Time
	RecoveredAt   *time.Time
	CampaignID    *uuid.UUID `gorm:"index"` // campaign that recovered this checkout
	OrderID       string                    // corresponding Shopify Order platform_id
	CreatedAt     time.Time  `gorm:"autoCreateTime;index"`
	UpdatedAt     time.Time  `gorm:"autoUpdateTime"`
}

// CartRecoveryCode stores a generated discount code for cart recovery.
// Code + ShopID composite unique index.
type CartRecoveryCode struct {
	ID              uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	CampaignID      uuid.UUID  `gorm:"index;not null"`
	ShopID          uuid.UUID  `gorm:"index;not null;uniqueIndex:idx_code_shop"`
	CheckoutID      uuid.UUID  `gorm:"index"` // associated checkout
	Code            string     `gorm:"not null;uniqueIndex:idx_code_shop"`
	ShopifyCodeID   string     // Shopify DiscountCode node ID
	DiscountPercent float64
	IsUsed          bool       `gorm:"default:false"`
	UsedAt          *time.Time
	OrderID         string     // platform_id of the order using this code
	OrderTotal      float64    // order total amount
	ExpiresAt       time.Time
	CreatedAt       time.Time  `gorm:"autoCreateTime"`
}

// CartRecoverySend stores an email send record for cart recovery.
type CartRecoverySend struct {
	ID              uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	CampaignID      uuid.UUID  `gorm:"index;not null"`
	ShopID          uuid.UUID  `gorm:"index;not null"`
	CheckoutID      uuid.UUID  `gorm:"index"` // associated checkout
	DiscountCodeID  *uuid.UUID              // associated discount code (nil if discount=0%)
	CustomerEmail   string     `gorm:"index"`
	ResendMessageID string     // Resend API returned message ID
	Status          string     `gorm:"default:sent;index"` // sent, opened, bounced, failed
	OpenedAt        *time.Time // email open time (written by Resend webhook)
	SentAt          time.Time  `gorm:"autoCreateTime;index"`
}

// CartRecoveryStats stores campaign aggregated statistics (computed hourly).
type CartRecoveryStats struct {
	ID               uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	CampaignID       uuid.UUID `gorm:"uniqueIndex:idx_campaign_period;not null"`
	Period           string    `gorm:"not null;uniqueIndex:idx_campaign_period"` // "2026-06"

	AbandonedCarts   int64
	EmailsSent       int64
	EmailsOpened     int64
	CodesGenerated   int64
	CodesUsed        int64
	OrdersRecovered  int64
	RevenueRecovered float64
	DiscountCost     float64
	NetRevenue       float64

	ComputedAt time.Time `gorm:"autoCreateTime"`
}
