package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

type SocialConnection struct {
	ID               uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID           uuid.UUID  `gorm:"uniqueIndex:idx_sc_shop_platform;not null"`
	Platform         string     `gorm:"uniqueIndex:idx_sc_shop_platform;not null"`
	EncryptedToken   string     `gorm:"type:text;not null"`
	TokenType        string     `gorm:"default:Bearer"`
	ExpiresAt        *time.Time
	RefreshToken     string     `gorm:"type:text"`
	PlatformUserID   string
	PlatformUserName string
	BoardCount       int        `gorm:"default:0"`
	Status           string     `gorm:"default:active"`
	CreatedAt        time.Time  `gorm:"autoCreateTime"`
	UpdatedAt        time.Time  `gorm:"autoUpdateTime"`
}

type SocialPost struct {
	ID            uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID        uuid.UUID  `gorm:"index;not null"`
	ConnectionID  uuid.UUID  `gorm:"index;not null"`
	Platform      string     `gorm:"not null"`
	ContentID     string
	ContentPieceID *uuid.UUID `gorm:"index"`      // links to ContentPiece for attribution
	PinID         string     `gorm:"uniqueIndex"`
	BoardID       string     `gorm:"not null"`
	MediaURL      string
	Title         string     `gorm:"size:500"`
	Description   string     `gorm:"type:text"`
	LinkURL       string
	UTMURL        string     `gorm:"type:text"`   // UTM tracking URL for attribution
	Status        string     `gorm:"default:draft;index"`
	ErrorMessage  string
	ScheduledAt   *time.Time
	PublishedAt   *time.Time
	CreatedAt     time.Time  `gorm:"autoCreateTime"`
	UpdatedAt     time.Time  `gorm:"autoUpdateTime"`
}

type ContentJob struct {
	ID               uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID           uuid.UUID      `gorm:"index;not null"`
	JobType          string         `gorm:"not null"`
	SourceProductIDs datatypes.JSON `gorm:"type:jsonb"`
	PlatformTargets  datatypes.JSON `gorm:"type:jsonb"`
	Tone             string         `gorm:"default:professional"`
	Language         string         `gorm:"default:en"`
	Status           string         `gorm:"default:pending;index"`
	Result           datatypes.JSON `gorm:"type:jsonb"`
	ErrorMessage     string
	CreatedAt        time.Time      `gorm:"autoCreateTime"`
	UpdatedAt        time.Time      `gorm:"autoUpdateTime"`
}

type CustomerChurnRisk struct {
	ID              uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID          uuid.UUID      `gorm:"uniqueIndex:idx_churn_shop_cust;not null"`
	CustomerID      string         `gorm:"uniqueIndex:idx_churn_shop_cust;not null"`
	ChurnScore      float64        `gorm:"default:0"`
	RiskLevel       string         `gorm:"default:low"`
	LastOrderDays   int            `gorm:"default:0"`
	OrderCountTrend float64        `gorm:"default:0"`
	SentimentTrend  float64        `gorm:"default:0"`
	TopFactors      datatypes.JSON `gorm:"type:jsonb"`
	ComputedAt      time.Time      `gorm:"autoCreateTime"`
}

type Customer360View struct {
	ID                  uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID              uuid.UUID      `gorm:"uniqueIndex:idx_c360_shop_cust;not null"`
	CustomerID          string         `gorm:"uniqueIndex:idx_c360_shop_cust;not null"`
	TotalOrders         int            `gorm:"default:0"`
	TotalSpent          float64        `gorm:"default:0"`
	AvgOrderValue       float64        `gorm:"default:0"`
	LastOrderAt         *time.Time
	ChurnRiskID         *uuid.UUID
	Segments            datatypes.JSON `gorm:"type:jsonb"`
	Tags                string
	LifetimeValueScore  float64        `gorm:"default:0"`
	UpdatedAt           time.Time      `gorm:"autoUpdateTime"`
}

type AutomationRule struct {
	ID              uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID          uuid.UUID      `gorm:"index;not null"`
	Name            string         `gorm:"not null"`
	Description     string
	TriggerType     string         `gorm:"not null"`
	TriggerConfig   datatypes.JSON `gorm:"type:jsonb"`
	Conditions      datatypes.JSON `gorm:"type:jsonb"`
	Actions         datatypes.JSON `gorm:"type:jsonb"`
	Priority        int            `gorm:"default:0"`
	CooldownMinutes int            `gorm:"default:0"`
	Enabled         bool           `gorm:"default:true"`
	LastRunAt       *time.Time
	CreatedAt       time.Time      `gorm:"autoCreateTime"`
	UpdatedAt       time.Time      `gorm:"autoUpdateTime"`
}

type RuleExecutionLog struct {
	ID           uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID       uuid.UUID      `gorm:"index;not null"`
	RuleID       uuid.UUID      `gorm:"index;not null"`
	TriggerEvent string
	Matched      bool           `gorm:"default:false"`
	ActionsTaken datatypes.JSON `gorm:"type:jsonb"`
	Result       string
	ErrorMessage string
	ExecutedAt   time.Time      `gorm:"autoCreateTime"`
}

type ExchangeAIRecommendation struct {
	ID                    uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID                uuid.UUID      `gorm:"index;not null"`
	ReturnID              uuid.UUID      `gorm:"uniqueIndex:idx_ex_ai_return;not null"`
	OriginalProductID     string         `gorm:"not null"`
	RecommendedProductIDs datatypes.JSON `gorm:"type:jsonb"`
	Reasoning             string         `gorm:"type:text"`
	ConfidenceScore       float64        `gorm:"default:0"`
	Status                string         `gorm:"default:pending"`
	CreatedAt             time.Time      `gorm:"autoCreateTime"`
}
