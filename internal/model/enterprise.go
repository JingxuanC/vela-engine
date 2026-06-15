package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

type StoreGroup struct {
	ID          uuid.UUID          `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	OwnerShopID uuid.UUID          `gorm:"index;not null"`
	Name        string             `gorm:"not null"`
	BillingPlan string             `gorm:"default:enterprise"`
	MemberCount int                `gorm:"default:1"`
	IsActive    bool               `gorm:"default:true"`
	CreatedAt   time.Time          `gorm:"autoCreateTime"`
	UpdatedAt   time.Time          `gorm:"autoUpdateTime"`
	Members     []StoreGroupMember `gorm:"foreignKey:GroupID;constraint:OnDelete:CASCADE"`
}
type StoreGroupMember struct {
	ID       uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	GroupID  uuid.UUID `gorm:"index;not null;uniqueIndex:idx_group_shop"`
	ShopID   uuid.UUID `gorm:"not null;uniqueIndex:idx_group_shop"`
	Role     string    `gorm:"default:member"`
	JoinedAt time.Time `gorm:"autoCreateTime"`
}
type ApiKey struct {
	ID          uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID      uuid.UUID      `gorm:"index;not null"`
	Label       string         `gorm:"not null"`
	Prefix      string         `gorm:"uniqueIndex;not null;size:40"`
	KeyHash     string         `gorm:"not null"`
	KeyLastFour string         `gorm:"size:4"`
	Scopes      datatypes.JSON `gorm:"type:jsonb"`
	RateLimit   int            `gorm:"default:60"`
	IsActive    bool           `gorm:"default:true"`
	LastUsedAt  *time.Time
	ExpiresAt   *time.Time
	CreatedAt   time.Time `gorm:"autoCreateTime"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime"`
}
type AnalyticsEvent struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID     uuid.UUID `gorm:"index:idx_analytics_shop_created;not null"`
	CustomerID string    `gorm:"index;not null"`
	SessionID  string    `gorm:"index"`
	EventType  string    `gorm:"not null;index"`
	Channel    string    `gorm:"not null;index"`
	Source     string
	Medium     string
	URL        string
	Referrer   string
	Device     string
	Metadata   datatypes.JSON `gorm:"type:jsonb"`
	OrderID    string         `gorm:"index"`
	OrderValue float64
	CreatedAt  time.Time `gorm:"autoCreateTime;index:idx_analytics_shop_created"`
}
type AttributionModel struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID         uuid.UUID `gorm:"uniqueIndex;not null"`
	ModelType      string    `gorm:"default:linear"`
	WindowDays     int       `gorm:"default:30"`
	LastComputedAt *time.Time
	ResultsJSON    datatypes.JSON `gorm:"type:jsonb"`
	CreatedAt      time.Time      `gorm:"autoCreateTime"`
	UpdatedAt      time.Time      `gorm:"autoUpdateTime"`
}
type PredictiveScore struct {
	ID                     uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID                 uuid.UUID `gorm:"index;not null"`
	CustomerID             string    `gorm:"index;not null"`
	CustomerEmail          string    `gorm:"index"`
	ChurnScore             float64   `gorm:"default:0"`
	ChurnRiskLevel         string    `gorm:"default:low"`
	ChurnReason            string
	PredictedLTV           float64 `gorm:"default:0"`
	PredictedNextOrderDate *time.Time
	PredictedOrderValue    float64   `gorm:"default:0"`
	ModelVersion           string    `gorm:"default:v1"`
	ComputedAt             time.Time `gorm:"autoCreateTime"`
	ExpiresAt              *time.Time
}
