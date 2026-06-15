package model

import (
	"time"

	"github.com/google/uuid"
)

// AuditLog records administrative and system actions for compliance and debugging.
type AuditLog struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID    uuid.UUID `gorm:"index;not null"`
	Action    string    `gorm:"not null"`  // e.g. "billing.subscribe", "inbox.reply", "marketing.flow.execute"
	Resource  string    `gorm:"not null"`  // e.g. "subscription:xxx", "conversation:xxx", "flow:xxx"
	Status    string    `gorm:"not null"`  // "success", "error"
	Detail    string    `gorm:"type:text"` // JSON detail payload
	CreatedAt time.Time `gorm:"autoCreateTime;index"`
}
