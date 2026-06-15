// Package model defines GORM database models for the Vela AI API.
package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// CustomerConversation stores a customer↔merchant conversation from the storefront chat inbox.
// When AI can't resolve a customer issue, the conversation is escalated here for merchant manual reply.
type CustomerConversation struct {
	ID            uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	ShopID        uuid.UUID      `gorm:"index;not null" json:"shop_id"`
	CustomerEmail string         `gorm:"default:''" json:"customer_email"`
	CustomerName  string         `gorm:"default:''" json:"customer_name"`
	Status        string         `gorm:"default:active;index" json:"status"` // active / resolved
	Messages      datatypes.JSON `gorm:"type:jsonb;default:'[]'" json:"messages"`
	Source        string         `gorm:"default:storefront_chat" json:"source"`
	CreatedAt     time.Time      `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt     time.Time      `gorm:"autoUpdateTime" json:"updated_at"`
}
