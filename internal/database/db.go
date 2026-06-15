// Package database provides GORM database connection, migration, and seeding.
package database

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/JingxuanC/vela-engine/internal/model"
)

// Connect opens a GORM connection to PostgreSQL with connection pooling and retry.
func Connect(databaseURL string) (*gorm.DB, error) {
	var db *gorm.DB
	var err error

	slog.Info("database: connecting with retry (max 3 attempts)")

	for attempt := 1; attempt <= 3; attempt++ {
		db, err = gorm.Open(postgres.Open(databaseURL), &gorm.Config{
			SkipDefaultTransaction: true,
			PrepareStmt:            true,
			Logger:                 logger.Default.LogMode(logger.Warn),
		})
		if err == nil {
			break
		}

		if attempt < 3 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			slog.Warn("database: connection attempt failed, retrying",
				"attempt", attempt,
				"backoff", backoff,
				"error", err,
			)
			time.Sleep(backoff)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("database: connect after 3 retries: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(50)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)

	slog.Info("database connected",
		"max_idle", 10,
		"max_open", 50,
		"conn_max_lifetime", "30m",
		"conn_max_idle_time", "5m",
	)
	return db, nil
}

// AutoMigrate runs GORM AutoMigrate for each application model independently.
func AutoMigrate(db *gorm.DB) error {
	slog.Info("running auto-migration")

	models := []struct {
		name  string
		model interface{}
	}{
		{"shops", &model.Shop{}},
		{"tryon_records", &model.TryOnRecord{}},
		{"returns", &model.Return{}},
		{"return_items", &model.ReturnItem{}},
		{"notification_prefs", &model.NotificationPrefs{}},
		{"cart_recovery_campaigns", &model.CartRecoveryCampaign{}},
		{"synced_products", &model.SyncedProduct{}},
		{"synced_orders", &model.SyncedOrder{}},
		{"fulfillment_events", &model.FulfillmentEvent{}},
		{"synced_customers", &model.SyncedCustomer{}},
		{"reply_records", &model.ReplyRecord{}},
		{"exchange_orders", &model.ExchangeOrder{}},
		{"judgeme_settings", &model.JudgeMeSetting{}},
		{"judgeme_sync_logs", &model.JudgeMeSyncLog{}},
		{"synced_returns", &model.SyncedReturn{}},
		{"synced_reviews", &model.SyncedReview{}},
		{"auto_reply_logs", &model.AutoReplyLog{}},
		{"auto_reply_configs", &model.AutoReplyConfig{}},
		{"tryon_shares", &model.TryOnShare{}},
		{"in_app_notifications", &model.InAppNotification{}},
		{"contact_messages", &model.ContactMessage{}},
		{"token_usages", &model.TokenUsage{}},
		{"social_connections", &model.SocialConnection{}},
		{"social_posts", &model.SocialPost{}},
		{"content_jobs", &model.ContentJob{}},
		{"customer_churn_risks", &model.CustomerChurnRisk{}},
		{"customer_360_views", &model.Customer360View{}},
		{"automation_rules", &model.AutomationRule{}},
		{"rule_execution_logs", &model.RuleExecutionLog{}},
		{"exchange_ai_recommendations", &model.ExchangeAIRecommendation{}},
		{"store_groups", &model.StoreGroup{}},
		{"store_group_members", &model.StoreGroupMember{}},
		{"api_keys", &model.ApiKey{}},
		{"analytics_events", &model.AnalyticsEvent{}},
		{"attribution_models", &model.AttributionModel{}},
		{"predictive_scores", &model.PredictiveScore{}},
		{"chat_intent_events", &model.ChatIntentEvent{}},
		{"sales_agent_configs", &model.SalesAgentConfig{}},
		{"customer_chat_profiles", &model.CustomerChatProfile{}},
		{"product_chat_insights", &model.ProductChatInsight{}},
		{"content_pieces", &model.ContentPiece{}},
		{"content_performances", &model.ContentPerformance{}},
		{"content_platform_metrics", &model.ContentPlatformMetrics{}},
		{"review_reply_codes", &model.ReviewReplyCode{}},
		{"product_review_insights", &model.ProductReviewInsight{}},
		{"review_recovery_attributions", &model.ReviewRecoveryAttribution{}},
		{"review_invitations", &model.ReviewInvitation{}},
		{"review_revenue_attributions", &model.ReviewRevenueAttribution{}},
		{"shop_insights", &model.ShopInsight{}},
		{"shop_llm_configs", &model.ShopLLMConfig{}},
		{"synced_checkouts", &model.SyncedCheckout{}},
		{"cart_recovery_codes", &model.CartRecoveryCode{}},
		{"cart_recovery_sends", &model.CartRecoverySend{}},
		{"cart_recovery_stats", &model.CartRecoveryStats{}},
		{"customer_conversations", &model.CustomerConversation{}},
		{"customer_insights", &model.CustomerInsight{}},
		{"marketing_flows", &model.MarketingFlow{}},
		{"marketing_flow_runs", &model.MarketingFlowRun{}},
		{"audit_logs", &model.AuditLog{}},
		{"product_affinities", &model.ProductAffinity{}},
	}

	var lastErr error
	for _, m := range models {
		if err := db.AutoMigrate(m.model); err != nil {
			slog.Warn("migration failed for table, continuing",
				"table", m.name,
				"error", err,
			)
			lastErr = err
		} else {
			slog.Info("migration succeeded", "table", m.name)
		}
	}

	if lastErr != nil {
		slog.Warn("some migrations failed, see warnings above")
	}
	return lastErr
}

// Seed inserts sample data for local development if the shops table is empty.
func Seed(db *gorm.DB) {
	if db == nil {
		slog.Warn("seed: no database connection, skipping")
		return
	}

	var count int64
	db.Model(&model.Shop{}).Count(&count)
	if count > 0 {
		slog.Info("seed: shops already exist, skipping", "count", count)
		return
	}

	slog.Info("seed: inserting sample data...")

	now := time.Now().UTC()

	// --- Shops ---
	shopID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	shops := []model.Shop{
		{ID: shopID, Domain: "test-store.example.com", Plan: "growth", InstalledAt: now},
		{ID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), Domain: "demo-store.example.com", Plan: "pro", InstalledAt: now},
		{ID: uuid.MustParse("00000000-0000-0000-0000-000000000003"), Domain: "dev-store.example.com", Plan: "growth", InstalledAt: now},
	}
	for _, s := range shops {
		if err := db.Create(&s).Error; err != nil {
			slog.Warn("seed: failed to create shop", "domain", s.Domain, "error", err)
		}
	}
	slog.Info("seed: shops created", "count", len(shops))

	// --- TryOn Records ---
	records := []model.TryOnRecord{
		{ShopID: shopID, TaskID: "task-001", ProductID: "1001", Status: "completed", ProcessingMs: 4520, CostCNY: 0.804, CreatedAt: now.Add(-24 * time.Hour)},
		{ShopID: shopID, TaskID: "task-002", ProductID: "1002", Status: "completed", ProcessingMs: 3890, CostCNY: 0.804, CreatedAt: now.Add(-48 * time.Hour)},
		{ShopID: shopID, TaskID: "task-003", ProductID: "1003", Status: "completed", ProcessingMs: 5100, CostCNY: 0.660, CreatedAt: now.Add(-72 * time.Hour)},
		{ShopID: shopID, TaskID: "task-004", ProductID: "1001", Status: "completed", ProcessingMs: 4200, CostCNY: 0.804, CreatedAt: now.Add(-96 * time.Hour)},
		{ShopID: shopID, TaskID: "task-005", ProductID: "1004", Status: "failed", ProcessingMs: 0, CostCNY: 0.004, ErrorMessage: "Image validation failed", CreatedAt: now.Add(-120 * time.Hour)},
	}
	for _, r := range records {
		if err := db.Create(&r).Error; err != nil {
			slog.Warn("seed: failed to create tryon record", "task_id", r.TaskID, "error", err)
		}
	}
	slog.Info("seed: tryon records created", "count", len(records))

	// --- Returns ---
	retID := uuid.MustParse("00000000-0000-0000-0000-000000000010")
	retItemID := uuid.MustParse("00000000-0000-0000-0000-000000000020")
	returns := []model.Return{
		{
			ID:                 retID,
			ShopID:             shopID,
			OrderID:            "ORD-1234",
			OrderName:          "#1234",
			CustomerEmail:      "john@example.com",
			CustomerName:       "John Doe",
			CustomerAddress:    "123 Main St",
			CustomerCity:       "Louisville",
			CustomerState:      "KY",
			CustomerPostalCode: "40202",
			CustomerCountry:    "US",
			Status:             "pending",
			ReturnReason:       "Size too large",
			ReturnNote:         "Wants to exchange for M",
			CreatedAt:          now.Add(-12 * time.Hour),
			Items: []model.ReturnItem{
				{ID: retItemID, ReturnID: retID, ProductID: "1001", VariantID: "var-1001-m", ProductTitle: "Summer Floral Dress", Quantity: 1, Size: "L", Reason: "too_large"},
			},
		},
		{
			ID:            uuid.MustParse("00000000-0000-0000-0000-000000000011"),
			ShopID:        shopID,
			OrderID:       "ORD-1238",
			OrderName:     "#1238",
			CustomerEmail: "jane@example.com",
			CustomerName:  "Jane Doe",
			Status:        "approved",
			ReturnReason:  "Wrong color",
			CreatedAt:     now.Add(-48 * time.Hour),
		},
	}
	for _, r := range returns {
		if err := db.Create(&r).Error; err != nil {
			slog.Warn("seed: failed to create return", "id", r.ID, "error", err)
		}
	}
	slog.Info("seed: returns created", "count", len(returns))

	// --- Notification Preferences ---
	prefs := model.NotificationPrefs{
		ShopID:             shopID,
		EmailReturnUpdates: true,
		EmailUsageAlerts:   true,
		EmailMarketing:     false,
		InAppNotifications: true,
	}
	if err := db.Create(&prefs).Error; err != nil {
		slog.Warn("seed: failed to create notification prefs", "error", err)
	} else {
		slog.Info("seed: notification prefs created")
	}

	// --- Cart Recovery Campaigns ---
	campaigns := []model.CartRecoveryCampaign{
		{ShopID: shopID, Name: "1 Hour Abandoned Cart", IsActive: true, DelayMinutes: 60, EmailSubject: "You left something behind!", EmailBody: "Hey, your cart is waiting...", DiscountPercent: 10},
		{ShopID: shopID, Name: "24 Hour Abandoned Cart", IsActive: true, DelayMinutes: 1440, EmailSubject: "Don't miss out!", EmailBody: "Here's a special offer...", DiscountPercent: 15},
	}
	for _, c := range campaigns {
		if err := db.Create(&c).Error; err != nil {
			slog.Warn("seed: failed to create campaign", "name", c.Name, "error", err)
		}
	}
	slog.Info("seed: cart recovery campaigns created", "count", len(campaigns))

	slog.Info("seed: sample data inserted successfully",
		"shops", len(shops),
		"tryons", len(records),
		"returns", len(returns),
		"campaigns", len(campaigns),
	)
}

// SeedShopID returns the UUID of the seeded test shop, for use in dev/test code.
func SeedShopID() string {
	return "00000000-0000-0000-0000-000000000001"
}

// ResolveShopID converts a shop_id string (either UUID or domain) to a UUID.
// It first tries uuid.Parse, then falls back to looking up by shop_domain.
func ResolveShopID(db *gorm.DB, shopIDStr string) (uuid.UUID, error) {
	// Try UUID first
	id, err := uuid.Parse(shopIDStr)
	if err == nil {
		return id, nil
	}
	// Fall back to domain lookup
	var shop model.Shop
	if err := db.Where("shop_domain = ?", shopIDStr).First(&shop).Error; err != nil {
		return uuid.Nil, fmt.Errorf("shop not found for: %s", shopIDStr)
	}
	return shop.ID, nil
}

// formatDuration is a helper for human-readable durations.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%.1fm", d.Minutes())
}
