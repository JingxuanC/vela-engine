package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
)

// ShopCleanup handles GDPR-compliant data deletion when a shop uninstalls.
type ShopCleanup struct{ db *gorm.DB }

func NewShopCleanup(db *gorm.DB) *ShopCleanup { return &ShopCleanup{db: db} }

// CleanupAll deletes ALL scoped data for a shop across all tables.
// Called immediately on app/uninstalled webhook.
// Keeps the Shop row itself (audit trail with uninstalled_at).
func (c *ShopCleanup) CleanupAll(ctx context.Context, shopID uuid.UUID) error {
	tables := []struct {
		name  string
		model interface{}
	}{
		// Sync pipeline
		{"synced_products", &model.SyncedProduct{}},
		{"synced_orders", &model.SyncedOrder{}},
		{"synced_customers", &model.SyncedCustomer{}},
		{"synced_reviews", &model.SyncedReview{}},
		{"synced_returns", &model.SyncedReturn{}},

		// Fulfillment
		{"fulfillment_events", &model.FulfillmentEvent{}},

		// Returns & exchanges
		{"returns", &model.Return{}},
		{"exchange_orders", &model.ExchangeOrder{}},

		// Cart recovery
		{"cart_recovery_campaigns", &model.CartRecoveryCampaign{}},
		{"cart_recovery_codes", &model.CartRecoveryCode{}},
		{"cart_recovery_sends", &model.CartRecoverySend{}},

		// Reviews
		{"reply_records", &model.ReplyRecord{}},
		{"auto_reply_configs", &model.AutoReplyConfig{}},
		{"auto_reply_logs", &model.AutoReplyLog{}},
		{"review_auto_reply_settings", &model.ReviewAutoReplySetting{}},

		// Marketing
		{"marketing_flows", &model.MarketingFlow{}},
		{"marketing_flow_runs", &model.MarketingFlowRun{}},

		// Content
		{"content_pieces", &model.ContentPiece{}},
		{"review_recovery_attribution", &model.ReviewRecoveryAttribution{}},

		// Inbox
		{"customer_conversations", &model.CustomerConversation{}},
		{"customer_insights", &model.CustomerInsight{}},

		// Discounts
		{"discount_coupons", &model.DiscountCoupon{}},

		// AI / chat
		{"chat_intent_events", &model.ChatIntentEvent{}},
		{"customer_chat_profiles", &model.CustomerChatProfile{}},
		{"product_chat_insights", &model.ProductChatInsight{}},
		{"sales_agent_configs", &model.SalesAgentConfig{}},

		// Try-on
		{"tryon_records", &model.TryOnRecord{}},
		{"tryon_shares", &model.TryOnShare{}},

		// Notifications
		{"in_app_notifications", &model.InAppNotification{}},
		{"notification_prefs", &model.NotificationPrefs{}},

		// Insights + LLM config
		{"shop_insights", &model.ShopInsight{}},
		{"shop_llm_configs", &model.ShopLLMConfig{}},
	}

	done := 0
	failed := 0
	total := len(tables)

	for _, t := range tables {
		if err := c.db.WithContext(ctx).Where("shop_id = ?", shopID).Delete(t.model).Error; err != nil {
			slog.Error("shop_cleanup: delete failed", "table", t.name, "shop_id", shopID, "error", err)
			failed++
		} else {
			done++
		}
	}

	slog.Info("shop_cleanup: done", "shop_id", shopID, "ok", done, "failed", failed, "total", total)
	if failed > 0 {
		return fmt.Errorf("shop_cleanup: %d/%d tables failed", failed, total)
	}
	return nil
}

// CleanupStale runs as a cron job: cleanup shops uninstalled 30+ days ago.
func (c *ShopCleanup) CleanupStale(ctx context.Context) (int, error) {
	var shops []model.Shop
	if err := c.db.WithContext(ctx).
		Where("uninstalled_at IS NOT NULL AND uninstalled_at < NOW() - INTERVAL '30 days'").
		Find(&shops).Error; err != nil {
		return 0, fmt.Errorf("cleanup_stale: %w", err)
	}

	count := 0
	for _, shop := range shops {
		if err := c.CleanupAll(ctx, shop.ID); err != nil {
			slog.Error("cleanup_stale: failed", "shop_id", shop.ID, "error", err)
			continue
		}
		count++
	}
	return count, nil
}
