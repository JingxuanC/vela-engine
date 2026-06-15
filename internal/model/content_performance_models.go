package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ContentPiece represents a single piece of AI-generated content for a shop.
// Each piece belongs to a ContentJob and tracks its lifecycle from generation
// through publishing to archival. Multi-tenant isolation via ShopID on all
// unique indexes.
type ContentPiece struct {
	ID          uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShortID     string     `gorm:"uniqueIndex:idx_cp_shop_short;size:8;not null"`
	ShopID      uuid.UUID  `gorm:"uniqueIndex:idx_cp_shop_short;index:idx_cp_shop_type;not null"`
	JobID       uuid.UUID  `gorm:"index:idx_cp_job;not null"`
	ContentType string     `gorm:"index:idx_cp_shop_type;not null"` // desc|blog|social
	Platform    string     `gorm:"index"`                           // target platform (pinterest, instagram, etc.)
	ProductID   string     `gorm:"index:idx_cp_product"`
	Title       string     `gorm:"size:500"`
	Body        string     `gorm:"type:text;not null"`
	Hashtags    string     `gorm:"type:text"`
	Tone        string     `gorm:"default:professional"`
	Language    string     `gorm:"default:en"`
	UTMURL      string     `gorm:"type:text"`
	Status      string     `gorm:"default:draft;index"` // draft|generated|published|archived
	CreatedAt   time.Time  `gorm:"autoCreateTime"`
	UpdatedAt   time.Time  `gorm:"autoUpdateTime"`
	DeletedAt   gorm.DeletedAt `gorm:"index"`
}

// ContentPerformance stores aggregated attribution metrics for a ContentPiece.
// One record per ContentPiece. Multi-tenant isolation via composite unique
// index on (ShopID, ContentPieceID).
type ContentPerformance struct {
	ID                uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID            uuid.UUID  `gorm:"uniqueIndex:idx_cperf_shop_piece;index:idx_cperf_shop;not null"`
	ContentPieceID    uuid.UUID  `gorm:"uniqueIndex:idx_cperf_shop_piece;not null"`
	ContentType       string     `gorm:"index"`

	AttributedOrders  int        `gorm:"default:0"`
	AttributedRevenue float64    `gorm:"default:0"`
	AttributedUnits   int        `gorm:"default:0"`
	LastAttributionAt *time.Time

	TotalImpressions  int        `gorm:"default:0"`
	TotalClicks       int        `gorm:"default:0"`
	TotalEngagement   int        `gorm:"default:0"`

	CreatedAt         time.Time  `gorm:"autoCreateTime"`
	UpdatedAt         time.Time  `gorm:"autoUpdateTime"`
}

// ContentPlatformMetrics stores per-day, per-platform analytics metrics for a
// ContentPiece. Multi-tenant isolation via composite unique index on
// (ShopID, ContentPieceID, Platform, MetricsDate).
type ContentPlatformMetrics struct {
	ID              uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID          uuid.UUID  `gorm:"uniqueIndex:idx_cpm_unique;index;not null"`
	ContentPieceID  uuid.UUID  `gorm:"uniqueIndex:idx_cpm_unique;index:idx_cpm_cp_platform;not null"`
	Platform        string     `gorm:"uniqueIndex:idx_cpm_unique;index:idx_cpm_cp_platform;not null"`
	MetricsDate     time.Time  `gorm:"uniqueIndex:idx_cpm_unique;index;not null"`

	PlatformPostID  string     `gorm:"index"`
	SocialPostID    *uuid.UUID `gorm:"index"`

	Impressions     int        `gorm:"default:0"`
	Clicks          int        `gorm:"default:0"`
	Saves           int        `gorm:"default:0"`
	Engagement      int        `gorm:"default:0"`
	Reach           int        `gorm:"default:0"`
	VideoViews      int        `gorm:"default:0"`

	PulledAt        time.Time  `gorm:"autoCreateTime"`
	CreatedAt       time.Time  `gorm:"autoCreateTime"`
}
