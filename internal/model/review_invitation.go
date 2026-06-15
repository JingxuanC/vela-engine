package model

import (
	"time"

	"github.com/google/uuid"
)

// ReviewInvitation represents a review invitation sent to a customer after an order is fulfilled.
type ReviewInvitation struct {
	ID            uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID        uuid.UUID  `gorm:"index;not null"`
	OrderID       int64      `gorm:"not null"` // Shopify order ID
	ProductID     int64      `gorm:"not null"` // Shopify product ID
	CustomerEmail string     `gorm:"not null"`
	CustomerName  string
	ProductTitle  string
	ProductImage  string
	Status        string     `gorm:"default:pending"` // pending/sent/converted/expired
	SendAt        *time.Time // 计划发送时间
	SentAt        *time.Time
	ReviewID      int64 // Shopify review ID (如果转化)
	AttributedRevenue float64 `gorm:"default:0"`
	AttributedOrders  int     `gorm:"default:0"`
	CreatedAt     time.Time `gorm:"autoCreateTime"`
	UpdatedAt     time.Time `gorm:"autoUpdateTime"`
}
