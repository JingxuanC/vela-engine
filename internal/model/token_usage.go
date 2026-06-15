package model

import (
	"time"

	"github.com/google/uuid"
)

type TokenUsage struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID     uuid.UUID `gorm:"index:idx_token_usage_shop_month;not null"`
	Feature    string    `gorm:"not null;index"`
	Operation  string    `gorm:"not null"`
	Count      int       `gorm:"default:1"`
	CostUSD    float64   `gorm:"default:0"`
	RecordedAt time.Time `gorm:"index:idx_token_usage_shop_month;not null;default:now()"`
	Status     string    `gorm:"default:success"`
	ErrorMsg   string
}

func (TokenUsage) TableName() string { return "token_usages" }
