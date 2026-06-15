// Package model defines GORM database models for the Vela AI API.
package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// JudgeMeSetting stores Judge.me integration configuration per shop.
type JudgeMeSetting struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID        uuid.UUID `gorm:"uniqueIndex;not null"`
	APIToken      string    `gorm:"not null"` // Private API Key
	Domain        string    `gorm:"not null"`
	ShopDomain    string    `gorm:"-"`        // runtime alias for Domain, not stored
	ConnectedAt   time.Time
	LastSyncAt    *time.Time
	WebhookSecret string    // Webhook 签名密钥
	IsActive      bool      `gorm:"default:true"`
	CreatedAt     time.Time `gorm:"autoCreateTime"`
	UpdatedAt     time.Time `gorm:"autoUpdateTime"`
}

// AfterFind syncs ShopDomain from Domain.
func (s *JudgeMeSetting) AfterFind(tx *gorm.DB) error {
	s.ShopDomain = s.Domain
	return nil
}

// JudgeMeSyncLog stores sync operation logs for Judge.me integration.
type JudgeMeSyncLog struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID       uuid.UUID `gorm:"index"`
	Action       string    // sync / webhook / test_connection
	Status       string    // success / failed
	Message      string
	ReviewsCount int
	CreatedAt    time.Time `gorm:"autoCreateTime"`
}
