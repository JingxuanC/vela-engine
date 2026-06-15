package model

import (
	"time"

	"github.com/google/uuid"
)

// ShopLLMConfig stores per-shop LLM provider and model preferences.
// Each shop has at most one config row (uniqueIndex on ShopID).
// When APIKey is empty, the platform's default API key is used.
// When APIKey is non-empty, it's treated as a bring-your-own-key (BYOK) override.
type ShopLLMConfig struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID      uuid.UUID `gorm:"uniqueIndex;not null"`
	Provider    string    `gorm:"default:deepseek"` // deepseek | dashscope | openai
	Model       string    `gorm:"default:deepseek-chat"`
	APIKey      string    `gorm:"default:''"` // empty = use platform key; else BYOK
	Temperature float64   `gorm:"default:0.7"`
	MaxTokens   int       `gorm:"default:2048"`
	IsActive    bool      `gorm:"default:true"`
	CreatedAt   time.Time `gorm:"autoCreateTime"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime"`
}

func (ShopLLMConfig) TableName() string { return "shop_llm_configs" }
