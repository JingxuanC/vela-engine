package model

import (
	"time"

	"github.com/google/uuid"
)

// ReviewRevenueAttribution tracks revenue attributed to review invitations.
// A single order can be attributed to at most one invitation (unique constraint).
type ReviewRevenueAttribution struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID        uuid.UUID `gorm:"index;not null"`
	InvitationID  uuid.UUID `gorm:"index;not null"`
	OrderID       string    `gorm:"not null"` // Shopify order platform_id
	CustomerEmail string    `gorm:"not null"`
	ProductID     int64     `gorm:"not null"`
	Revenue       float64   `gorm:"not null"`
	AttributedAt  time.Time `gorm:"autoCreateTime"`
}
