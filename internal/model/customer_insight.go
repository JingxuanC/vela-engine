package model

import (
	"time"

	"github.com/google/uuid"
)

// CustomerInsight stores computed LTV predictions and churn risk scores
// for each customer of a shop, derived from synced_orders data.
type CustomerInsight struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID    uuid.UUID `gorm:"index;not null;uniqueIndex:idx_ci_shop_email"`
	CustomerEmail string `gorm:"not null;uniqueIndex:idx_ci_shop_email"`
	CustomerName  string

	// Base RFM (redundant, for fast queries)
	TotalOrders          int     `gorm:"default:0"`
	TotalSpent           float64 `gorm:"default:0"`
	AvgOrderValue        float64 `gorm:"default:0"`
	LastOrderAt          *time.Time
	FirstOrderAt         *time.Time
	AvgOrderIntervalDays float64 `gorm:"default:0"`

	// Predictive metrics
	PredictedLTV      float64 `gorm:"default:0"`
	ChurnRiskScore    float64 `gorm:"default:0"`
	ChurnRiskLevel    string  `gorm:"default:low"`
	ActiveProbability float64 `gorm:"default:0"`

	// Trend signals (recent 3 orders vs earliest 3)
	AvgRecentValue float64 `gorm:"default:0"`
	AvgEarlyValue  float64 `gorm:"default:0"`

	// Metadata
	Confidence string `gorm:"default:medium"`
	DataPoints int    `gorm:"default:0"`
	ComputedAt time.Time `gorm:"autoCreateTime"`
	CreatedAt  time.Time `gorm:"autoCreateTime"`
	UpdatedAt  time.Time `gorm:"autoUpdateTime"`
}

// TableName overrides the default table name to "customer_insights".
func (CustomerInsight) TableName() string {
	return "customer_insights"
}
