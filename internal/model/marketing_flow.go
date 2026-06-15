package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// MarketingFlow represents an automated marketing flow template/configuration.
type MarketingFlow struct {
	ID           uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID       uuid.UUID      `gorm:"index;not null"`
	Name         string         `gorm:"not null"`
	TriggerType  string         `gorm:"not null"` // order.created, checkout.abandoned, customer.dormant, etc.
	TriggerConfig datatypes.JSON `gorm:"type:jsonb;default:'{}'"`  // delay, timing, etc.
	Conditions   datatypes.JSON `gorm:"type:jsonb;default:'[]'"`   // [{field:"customer_tier", op:"eq", value:"VIP"}]
	Actions      datatypes.JSON `gorm:"type:jsonb;default:'[]'"`   // [{type:"send_email", config:{...}}]
	Enabled      bool           `gorm:"default:true"`
	IsTemplate   bool           `gorm:"default:false"` // true for pre-built templates
	TemplateKey  string         `gorm:"default:''"`    // unique key for pre-built templates: welcome, abandoned_cart, etc.
	CreatedAt    time.Time      `gorm:"autoCreateTime"`
	UpdatedAt    time.Time      `gorm:"autoUpdateTime"`
}

// MarketingFlowRun records each execution of a marketing flow.
type MarketingFlowRun struct {
	ID              uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	FlowID          uuid.UUID  `gorm:"index;not null"`
	ShopID          uuid.UUID  `gorm:"index;not null"`
	CustomerEmail   string     `gorm:"index"`
	CustomerName    string     `gorm:"default:''"`
	TriggerEventID  string     `gorm:"default:''"`  // the event payload ID that triggered this run
	Status          string     `gorm:"default:pending;index"` // pending, sent, failed, skipped
	ErrorMessage    string     `gorm:"type:text"`
	ResendMessageID string     `gorm:"default:''"`  // Resend API message ID for tracking
	ActionIndex     int        `gorm:"default:0"`    // which action in the sequence was executed
	ExecutedAt      *time.Time
	CreatedAt       time.Time  `gorm:"autoCreateTime"`
}
