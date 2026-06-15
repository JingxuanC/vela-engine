// Package model defines GORM database models for the Vela AI API.
package model

import (
	"time"

	"github.com/google/uuid"
)

// FulfillmentEvent stores a single tracking event from a Shopify fulfillment.
// Synced via Shopify Admin GraphQL API (fulfillment events).
type FulfillmentEvent struct {
	ID               uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID           uuid.UUID  `gorm:"index;not null"`
	OrderID          uuid.UUID  `gorm:"index;not null"`                     // FK → synced_orders.id
	FulfillmentID    string     `gorm:"index;not null"`                     // Shopify fulfillment GID
	Status           string     `gorm:"not null"`                           // e.g. LABEL_PRINTED, IN_TRANSIT, DELIVERED
	Message          string     // human-readable tracking message
	City             string
	Province         string
	Country          string
	HappenedAt       time.Time  `gorm:"not null"`                           // when the event occurred (Shopify timestamp)
	EstimatedDeliveryAt *time.Time
	CreatedAt        time.Time  `gorm:"autoCreateTime"`
}

// TableName overrides the default table name for GORM.
func (FulfillmentEvent) TableName() string {
	return "fulfillment_events"
}
