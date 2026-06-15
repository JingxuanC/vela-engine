// Package model defines GORM database models for the Vela AI API.
package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// Shop represents a tenant/store that uses the Vela platform.
// ShopDomain is the primary Shopify identifier (myshopify.com). 
// Domain is the custom domain (used in standalone mode).
// AccessToken is the OAuth token (Shopify mode), empty for standalone.
type Shop struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopDomain    string    `gorm:"uniqueIndex;not null"` // Shopify domain (myshopify.com)
	Domain        string    `gorm:"uniqueIndex"`          // custom domain (standalone mode)
	AccessToken   string    // OAuth access token (Shopify mode), empty for standalone
	Plan          string    `gorm:"default:free"`
	Vertical      string    `gorm:"default:''"` // "" / "fashion" / "furniture" / "wine"
	ContactEmail  string    `gorm:"default:''"` // merchant's email for notifications
	InstalledAt   time.Time `gorm:"autoCreateTime"`
	UpdatedAt     time.Time `gorm:"autoUpdateTime"`
	UninstalledAt *time.Time
	// Billing fields
	SubscriptionID string `gorm:"default:''"`
	TrialEndsAt    *time.Time
	BillingStatus  string `gorm:"default:active"` // active / cancelled / past_due
}

// TryOnRecord stores the result of a virtual try-on request.
type TryOnRecord struct {
	ID              uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID          uuid.UUID `gorm:"index;not null"`
	TaskID          string    `gorm:"uniqueIndex"`
	ProductID       string
	PersonImageURL  string
	GarmentImageURL string
	ResultImageURL  string
	Status          string `gorm:"default:pending;index"`
	ProcessingMs    int
	CostCNY         float64
	ErrorMessage    string
	CreatedAt       time.Time `gorm:"autoCreateTime"`
}

// Return represents a customer return request.
type Return struct {
	ID                 uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID             uuid.UUID `gorm:"index;not null"`
	OrderID            string    `gorm:"index"`
	OrderName          string
	CustomerEmail      string
	CustomerName       string
	CustomerAddress    string
	CustomerCity       string
	CustomerState      string
	CustomerPostalCode string
	CustomerCountry    string `gorm:"default:US"`
	Status             string `gorm:"default:pending;index"`
	ReturnReason       string
	ReturnNote         string
	ShipEngineLabelID  string
	TrackingNumber     string
	LabelURL           string
	RefundAmount       float64
	ExchangeOrderID    string
	Items              []ReturnItem `gorm:"foreignKey:ReturnID;constraint:OnDelete:CASCADE"`
	CreatedAt          time.Time    `gorm:"autoCreateTime"`
	UpdatedAt          time.Time    `gorm:"autoUpdateTime"`
}

// ReturnItem represents a single line item within a return request.
type ReturnItem struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ReturnID     uuid.UUID `gorm:"index;not null"`
	ProductID    string    `gorm:"not null"`
	VariantID    string
	ProductTitle string
	Quantity     int `gorm:"default:1"`
	Size         string
	Reason       string
	CreatedAt    time.Time `gorm:"autoCreateTime"`
}

// SyncedReturn mirrors a Shopify return from the returns/request or orders/updated webhook.
// This is the canonical source of truth — our Return model is the manual/fallback entry.
type SyncedReturn struct {
	ID           uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID       uuid.UUID  `gorm:"index;not null;uniqueIndex:idx_shop_platform_return"`
	Platform     string     `gorm:"default:shopify"`                            // "shopify"
	PlatformID   string     `gorm:"not null;uniqueIndex:idx_shop_platform_return"` // Shopify return ID
	Name         string     // "#1023-R1"
	Status       string     `gorm:"index"` // open/closed/declined
	OrderID      string     `gorm:"index"`
	OrderName    string
	CustomerID   string
	CustomerEmail string
	CustomerName  string
	LineItems    datatypes.JSON `gorm:"type:jsonb"` // []ReturnLineItemPayload
	SyncedAt     time.Time `gorm:"autoCreateTime"`
	UpdatedAt    time.Time `gorm:"autoUpdateTime"`
}

// ReturnLineItemPayload is a single return line item from the Shopify webhook.
type ReturnLineItemPayload struct {
	ID                    int64  `json:"id"`
	Quantity              int    `json:"quantity"`
	ReturnReason          string `json:"return_reason"`
	ReturnReasonNote      string `json:"return_reason_note"`
	LineItemID            int64  `json:"line_item_id"`
	FulfillmentLineItemID int64  `json:"fulfillment_line_item_id"`
}

// NotificationPrefs stores a shop's notification preferences.
type NotificationPrefs struct {
	ID                 uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID             uuid.UUID `gorm:"uniqueIndex;not null"`
	EmailReturnUpdates bool      `gorm:"default:true"`
	EmailUsageAlerts   bool      `gorm:"default:true"`
	EmailMarketing     bool      `gorm:"default:false"`
	EmailReviewInvites bool      `gorm:"default:true"`
	InAppNotifications bool      `gorm:"default:true"`
	CreatedAt          time.Time `gorm:"autoCreateTime"`
	UpdatedAt          time.Time `gorm:"autoUpdateTime"`
}

// CartRecoveryCampaign represents an automated cart recovery campaign.
type CartRecoveryCampaign struct {
	ID              uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID          uuid.UUID `gorm:"index:idx_cart_recovery_shop_created;uniqueIndex:idx_campaign_prefix_shop;not null"`
	Name            string
	IsActive        bool `gorm:"default:true"`
	DelayMinutes    int  `gorm:"default:60"`
	EmailSubject    string
	EmailBody       string
	DiscountPercent float64
	CodePrefix      string     `gorm:"type:varchar(32);uniqueIndex:idx_campaign_prefix_shop"` // discount code prefix
	LastExecutedAt  *time.Time
	ScheduleEnabled bool       `gorm:"default:true"`
	CreatedAt       time.Time  `gorm:"autoCreateTime;index:idx_cart_recovery_shop_created"`
	UpdatedAt       time.Time  `gorm:"autoUpdateTime"`
}

// --- Webhook Synced Data Models ---

// SyncedProduct stores Shopify product data synced via webhook.
type SyncedProduct struct {
	ID                 uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID             uuid.UUID      `gorm:"index;not null;uniqueIndex:idx_shop_platform_product"`
	PlatformID         string         `gorm:"not null;uniqueIndex:idx_shop_platform_product"`
	Title              string
	Description        string
	Vendor             string
	ProductType        string
	Status             string
	Variants           datatypes.JSON `gorm:"type:jsonb"`
	Images             datatypes.JSON `gorm:"type:jsonb"`
	Options            datatypes.JSON `gorm:"type:jsonb"`
	Tags               string
	CurrencyNormalized string         `gorm:"default:''" json:"currency_normalized"`
	Region             string         `gorm:"default:''" json:"region"`
	SizeNormalized     string         `gorm:"default:''" json:"size_normalized"`
	CategoryPath       string         `gorm:"default:''" json:"category_path"`
	Materials          datatypes.JSON `gorm:"type:jsonb;default:'[]'" json:"materials"`
	Handle             string         `gorm:"default:''"` // Shopify product URL slug
	CreatedAt          time.Time      `gorm:"autoCreateTime"`
	UpdatedAt          time.Time      `gorm:"autoUpdateTime"`
}

// --- Review Auto-Reply Models ---

// SyncedReview stores reviews synced from external platforms (JudgeMe, Loox, Yotpo).
// Platform-agnostic: uses Platform + PlatformID instead of platform-specific IDs.
type SyncedReview struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID     uuid.UUID `gorm:"not null;uniqueIndex:idx_shop_platform_review"`
	Platform   string    `gorm:"not null;uniqueIndex:idx_shop_platform_review"` // "judgeme" | "loox" | "yotpo" | "shopify_native"
	PlatformID string    `gorm:"not null;uniqueIndex:idx_shop_platform_review"` // platform-internal review ID

	Title         string
	Body          string
	Rating        float64
	ReviewerName  string
	ReviewerEmail string
	ProductID     string `gorm:"index"` // Shopify product ID
	ProductTitle  string // denormalized for display

	State          string // published / unpublished / spam
	Verified       bool
	HasPublicReply bool // whether a public reply already exists on the platform

	// Legacy field — kept for migration, deprecated
	JudgeMeReviewID int `gorm:"default:0"`

	SyncedFrom string    // webhook / full_sync / incremental_sync
	SyncedAt   time.Time `gorm:"autoCreateTime"`
	CreatedAt  time.Time // original creation time on the platform
	UpdatedAt  time.Time `gorm:"autoUpdateTime"`
}

// ReplyRecord stores AI-generated reply tracking with state machine.
// One record per review — status flows through the state machine.
type ReplyRecord struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID     uuid.UUID `gorm:"not null;uniqueIndex:idx_reply_shop_platform"`
	PlatformID string    `gorm:"not null;uniqueIndex:idx_reply_shop_platform"` // maps to SyncedReview.PlatformID
	ProductID  string    `gorm:"index"`

	CustomerName  string
	CustomerEmail string
	Rating        float64
	ReviewTitle   string
	ReviewBody    string
	SentimentScore float64

	AiReplies  datatypes.JSON `gorm:"type:jsonb"` // AI-generated 3 candidate replies
	FinalReply string                              // final reply content

	SendMode string `gorm:"default:manual"` // "auto" | "manual"

	Status string `gorm:"default:pending"`
	// pending    → waiting for processing
	// processing → worker is handling (transient, prevents concurrent processing)
	// approved   → manually approved, ready to send
	// sent       → successfully posted
	// failed     → posting failed
	// skipped    → manually skipped
	// cancelled  → merchant cancelled

	SentAt     *time.Time
	FailedAt   *time.Time
	FailReason string
	RetryCount int `gorm:"default:0"`

	// Legacy — deprecated, never persisted
	AutoApproved   bool    `gorm:"-" json:"-"`

	// Phase 1: review recovery tracking
	DiscountCode    string  `json:"discount_code"`                     // e.g. "RV-A1B2-X7K9"
	DiscountPercent float64 `gorm:"default:0" json:"discount_percent"`
	DiscountUsed    bool    `gorm:"default:false" json:"discount_used"`
	CustomerID      string  `gorm:"index" json:"customer_id"`          // Shopify customer ID for VCI
	Issues          string  `json:"issues"`                            // comma-separated: "size,color,quality"
	Severity        string  `gorm:"default:mild" json:"severity"`      // mild/moderate/severe

	CreatedAt time.Time `gorm:"autoCreateTime"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
}

// AutoReplyConfig stores per-shop auto-reply configuration.
type AutoReplyConfig struct {
	ShopID     uuid.UUID `gorm:"primaryKey"`
	Platform   string    `gorm:"primaryKey"` // "judgeme" | "loox" | "yotpo" | "shopify_native"
	APIToken   string    // encrypted
	ShopDomain string    // platform-specific domain
	Enabled    bool      `gorm:"default:false"`

	// Send mode
	Mode string `gorm:"default:manual"` // "auto" | "manual"

	// Strategy
	AutoReplyOnNew    bool    `gorm:"default:true"` // reply when new review arrives
	ReplyNegativeOnly bool    `gorm:"default:true"` // only reply to negative reviews (≤ threshold)
	Threshold         float64 `gorm:"default:3"`    // reply to reviews with rating ≤ this value

	// Discount tier percentages (Phase 1: review recovery)
	DiscountMild     float64 `gorm:"default:15"` // 2-3 star reviews
	DiscountModerate float64 `gorm:"default:30"` // 1-2 star reviews (matched to code in auto_reply.go)
	DiscountSevere   float64 `gorm:"default:50"` // 1 star reviews

	CreatedAt time.Time `gorm:"autoCreateTime"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
}

// AutoReplyLog stores operation logs for the auto-reply workflow.
type AutoReplyLog struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID        uuid.UUID `gorm:"index"`
	PlatformID    string    `gorm:"index"` // maps to SyncedReview.PlatformID
	ReplyRecordID uuid.UUID `gorm:"index"` // maps to ReplyRecord.ID

	Action     string // "analyze" | "generate" | "post_reply" | "retry" | "fail" | "manual_approve" | "manual_reject"
	Status     string // "success" | "failed" | "skipped"
	Message    string
	RetryCount int

	CreatedAt time.Time `gorm:"autoCreateTime"`
}

// ReviewAutoReplySetting stores per-shop auto-reply settings (legacy — migrated to AutoReplyConfig).
// Deprecated: use AutoReplyConfig instead.
type ReviewAutoReplySetting struct {
	ID                uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID            uuid.UUID `gorm:"uniqueIndex;not null"`
	AutoSendThreshold float64   `gorm:"default:1"`
	RequireApproval   bool      `gorm:"default:true"`
	DiscountMild      float64   `gorm:"default:15"`
	DiscountModerate  float64   `gorm:"default:20"`
	DiscountSevere    float64   `gorm:"default:30"`
	ReplyLanguage     string    `gorm:"default:auto"`
	DefaultReplyStyle string    `gorm:"default:professional"`
	CreatedAt         time.Time `gorm:"autoCreateTime"`
	UpdatedAt         time.Time `gorm:"autoUpdateTime"`
}

// DiscountCoupon stores discount coupon records (deprecated — Phase 1 doesn't do discounts).
// Deprecated: discount auto-issuance removed in Phase 1 refactor.
type DiscountCoupon struct {
	ID              uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID          uuid.UUID `gorm:"index"`
	CouponCode      string    `gorm:"uniqueIndex"`
	DiscountPercent float64
	DiscountAmount  float64
	MinOrderAmount  float64
	MaxUses         int `gorm:"default:1"`
	CurrentUses     int `gorm:"default:0"`
	ExpiresAt       time.Time
	Status          string `gorm:"default:active"`
	Source          string // review_reply / cart_recovery / manual
	SourceID        string
	CreatedAt       time.Time `gorm:"autoCreateTime"`
}

// SyncedOrder stores Shopify order data synced via webhook.
type SyncedOrder struct {
	ID                  uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID              uuid.UUID `gorm:"index;not null;uniqueIndex:idx_shop_platform_order"`
	PlatformID          string    `gorm:"not null;uniqueIndex:idx_shop_platform_order"`
	OrderNumber         int
	CustomerEmail       string    `gorm:"index:idx_shop_customer_email"`
	CustomerName        string
	TotalPrice          float64
	Currency            string `gorm:"default:USD"`
	FinancialStatus     string
	FulfillmentStatus   string
	LineItems           datatypes.JSON `gorm:"type:jsonb"`
	ShippingAddress     datatypes.JSON `gorm:"type:jsonb"`
	// Fulfillment tracking fields (synced from Shopify Admin GraphQL)
	TrackingNumber      string     `gorm:"default:''"`
	TrackingURL         string     `gorm:"default:''"`
	Carrier             string     `gorm:"default:''"`
	LatestEventStatus   string     `gorm:"default:''"`
	EstimatedDeliveryAt *time.Time
	OrderCreatedAt      *time.Time `gorm:"index"` // Shopify order creation time (for RFM recency)
	CreatedAt           time.Time      `gorm:"autoCreateTime"`
	UpdatedAt           time.Time      `gorm:"autoUpdateTime"`
}

// SyncedCustomer stores Shopify customer data synced via webhook.
type SyncedCustomer struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID      uuid.UUID `gorm:"not null;uniqueIndex:idx_shop_platform_customer"`
	PlatformID  string    `gorm:"not null;uniqueIndex:idx_shop_platform_customer"`
	Email       string
	FirstName   string
	LastName    string
	OrdersCount int
	TotalSpent  float64
	Tags        string
	CreatedAt   time.Time `gorm:"autoCreateTime"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime"`
}

// ExchangeOrder represents an exchange order linked to a return.
type ExchangeOrder struct {
	ID                uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID            uuid.UUID `gorm:"index;not null"`
	ReturnID          uuid.UUID `gorm:"uniqueIndex;not null"`
	OriginalProductID string    `gorm:"not null"`
	OriginalVariantID string
	NewProductID      string `gorm:"not null"`
	NewVariantID      string
	NewProductTitle   string
	NewVariantTitle   string
	Quantity          int `gorm:"default:1"`
	PriceOriginal     float64
	PriceNew          float64
	PriceDiff         float64
	Currency          string `gorm:"default:USD"`
	ShopifyOrderID    string
	Status            string `gorm:"default:pending"`
	TrackingNumber    string
	ShippingLabelURL  string
	Notes             string
	CreatedAt         time.Time `gorm:"autoCreateTime"`
	UpdatedAt         time.Time `gorm:"autoUpdateTime"`
}

// TryOnShare stores a shareable link for a virtual try-on result.
type TryOnShare struct {
	ID              uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID          uuid.UUID `gorm:"index;not null"`
	Slug            string    `gorm:"uniqueIndex;not null;size:12"`
	TryOnTaskID     string    `gorm:"index"`
	ProductID       string
	ProductTitle    string
	ProductPrice    float64
	ProductImageURL string
	ResultImageURL  string `gorm:"not null"`
	PersonImageURL  string
	ViewCount       int64     `gorm:"default:0"`
	CreatedAt       time.Time `gorm:"autoCreateTime"`
}

// InAppNotification stores a notification for in-app display.
type InAppNotification struct {
	ID        uint      `gorm:"primaryKey;autoIncrement"`
	ShopID    uuid.UUID `gorm:"index;not null"`
	Type      string    `gorm:"not null"`
	Channel   string    `gorm:"default:in_app"`
	Title     string    `gorm:"not null"`
	Body      string    `gorm:"type:text"`
	ActionURL string
	Priority  string    `gorm:"default:normal"`
	IsRead    bool      `gorm:"default:false"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
}

// ContactMessage stores a contact form submission from the landing page.
type ContactMessage struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name      string    `gorm:"not null"`
	Email     string    `gorm:"not null"`
	Message   string    `gorm:"type:text;not null"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
}

// ChatIntentEvent records a customer intent signal extracted from Sales Agent conversations.
// Part of the VCI (Vela Customer Intelligence) data flywheel.
type ChatIntentEvent struct {
	ID         uuid.UUID     `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID     uuid.UUID     `gorm:"index;not null"`
	CustomerID string        `gorm:"index"`     // anonymous cookie or Shopify customer ID
	SessionID  string        `gorm:"column:session_id"`  // Redis chat_session key
	EventType  string        `gorm:"column:event_type;index"` // intent_high|price_objection|size_preference|color_preference|discount_request|purchase_signal
	ProductID  string        `gorm:"column:product_id"` // related product PlatformID
	Metadata   datatypes.JSON
	AgentReply string        `gorm:"column:agent_reply;type:text"` // what the agent said in response
	CreatedAt  time.Time     `gorm:"autoCreateTime;index"`
}

// SalesAgentConfig stores per-shop Sales Agent configuration.
type SalesAgentConfig struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID         uuid.UUID `gorm:"uniqueIndex;not null"`
	Enabled        bool      `gorm:"default:true"`
	WelcomeMessage string    `gorm:"type:text;default:'Hi! Welcome to our store. What are you looking for today? 👋'"`
	BrandVoice     string    `gorm:"default:friendly"` // friendly | professional | luxury | playful
	CreatedAt      time.Time `gorm:"autoCreateTime"`
	UpdatedAt      time.Time `gorm:"autoUpdateTime"`
}

// CustomerChatProfile stores long-term customer preferences extracted from chat conversations.
// Persisted across sessions (vs ChatSession which is 24h Redis TTL).
type CustomerChatProfile struct {
	ID             uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID         uuid.UUID      `gorm:"uniqueIndex:idx_shop_customer;not null"`
	CustomerID     string         `gorm:"uniqueIndex:idx_shop_customer;not null"` // Shopify customer ID or anonymous device ID
	CustomerEmail  string

	// Preferences extracted from conversations
	SizePreference    string         `gorm:"column:size_preference"`    // "M", "L", "8"
	ColorPreferences  datatypes.JSON `gorm:"type:jsonb;default:'[]'"`  // ["blue","black","white"]
	StylePreferences  datatypes.JSON `gorm:"type:jsonb;default:'[]'"`  // ["casual","formal","bohemian"]
	BudgetRange       datatypes.JSON `gorm:"type:jsonb"`               // {"min":0,"max":50}
	CategoryInterests datatypes.JSON `gorm:"type:jsonb;default:'[]'"`  // ["dresses","shoes","accessories"]

	// Behavioral signals (computed from accumulated events)
	PriceSensitivity  string         `gorm:"column:price_sensitivity;default:U"` // L=low M=medium H=high U=unknown
	IntentStrength    string         `gorm:"column:intent_strength;default:U"`   // H=high M=medium L=low U=unknown
	DiscountRequests  int            `gorm:"column:discount_requests;default:0"`
	PurchasesViaChat  int            `gorm:"column:purchases_via_chat;default:0"`
	TotalChatSessions int            `gorm:"column:total_chat_sessions;default:0"`

	// Timestamps
	FirstSeenAt      *time.Time     `gorm:"column:first_seen_at"`
	LastSeenAt       *time.Time     `gorm:"column:last_seen_at"`
	LastPurchaseAt   *time.Time     `gorm:"column:last_purchase_at"`
	CreatedAt        time.Time      `gorm:"autoCreateTime"`
	UpdatedAt        time.Time      `gorm:"autoUpdateTime"`
}

// ProductChatInsight stores per-product aggregated chat analytics.
// Updated periodically from chat_intent_events, used for dashboard and search ranking.
type ProductChatInsight struct {
	ID           uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID       uuid.UUID      `gorm:"uniqueIndex:idx_shop_product_period;not null"`
	ProductID    string         `gorm:"uniqueIndex:idx_shop_product_period;not null"` // PlatformID
	ProductTitle string         `gorm:"column:product_title"`

	// Aggregated stats
	TimesRecommended int         `gorm:"column:times_recommended;default:0"`
	TimesQuestioned  int         `gorm:"column:times_questioned;default:0"`
	TimesViewed      int         `gorm:"column:times_viewed;default:0"`
	TimesPurchased   int         `gorm:"column:times_purchased;default:0"`
	TimesObjected    int         `gorm:"column:times_objected;default:0"`

	// Analysis
	TopObjections  datatypes.JSON `gorm:"column:top_objections;type:jsonb;default:'[]'"`
	ComparedWith   datatypes.JSON `gorm:"column:compared_with;type:jsonb;default:'[]'"`

	// Period
	PeriodStart    string         `gorm:"column:period_start;uniqueIndex:idx_shop_product_period;not null"` // "2026-01"
	PeriodEnd      string         `gorm:"column:period_end"`

	UpdatedAt      time.Time      `gorm:"autoUpdateTime"`
}

// ShopInsight stores AI-generated insights for a shop, persisted by InsightEngine.
// Upserted by (shop_id, type, title) to avoid duplicates from repeated events.
type ShopInsight struct {
	ID            uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ShopID        uuid.UUID  `gorm:"uniqueIndex:idx_shop_insight_key;not null"`
	Type          string     `gorm:"uniqueIndex:idx_shop_insight_key;not null"`
	Title         string     `gorm:"uniqueIndex:idx_shop_insight_key;not null"`
	Severity      string     `gorm:"not null"`
	Body          string
	Data          datatypes.JSON
	SampleSize    int
	DataFreshness string     `gorm:"default:''"`
	Confidence    string     `gorm:"default:''"`
	CreatedAt     time.Time  `gorm:"autoCreateTime;index"`
	UpdatedAt     time.Time  `gorm:"autoUpdateTime"`
}
