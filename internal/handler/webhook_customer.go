package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"gorm.io/gorm/clause"
)

// --- Customer Webhook ---

type shopifyCustomerWebhook struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	OrdersCount int    `json:"orders_count"`
	TotalSpent  string `json:"total_spent"`
	Tags        string `json:"tags"`
	State       string `json:"state"`
}

func (h *WebhookHandler) handleCustomer(w http.ResponseWriter, r *http.Request, rawBody []byte, shopDomain string) {
	shopID, err := h.resolveShopID(r.Context(), shopDomain)
	if err != nil {
		slog.Warn("webhook: could not resolve shop for customer", "shop_domain", shopDomain, "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	var shopify shopifyCustomerWebhook
	if err := json.Unmarshal(rawBody, &shopify); err != nil {
		slog.Error("webhook: failed to parse customer body", "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	if shopify.State != "enabled" && shopify.State != "invited" {
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	platformID := fmt.Sprintf("%d", shopify.ID)
	totalSpent := 0.0
	if shopify.TotalSpent != "" {
		if price, err := strconv.ParseFloat(shopify.TotalSpent, 64); err != nil {
			slog.Warn("webhook: failed to parse customer total_spent", "value", shopify.TotalSpent, "error", err)
		} else {
			totalSpent = price
		}
	}

	customer := model.SyncedCustomer{
		ShopID: shopID, PlatformID: platformID, Email: shopify.Email,
		FirstName: shopify.FirstName, LastName: shopify.LastName,
		OrdersCount: shopify.OrdersCount, TotalSpent: totalSpent, Tags: shopify.Tags,
	}

	err = h.db.WithContext(r.Context()).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "shop_id"}, {Name: "platform_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"email", "first_name", "last_name", "orders_count", "total_spent", "tags"}),
	}).Create(&customer).Error

	if err != nil {
		slog.Error("webhook: failed to upsert customer", "platform_id", platformID, "error", err)
	} else {
		h.publishEvent(eventbus.EventCustomerSynced, customer.ShopID, eventbus.PipelineEventPayload{
			EntityID: platformID, ShopID: customer.ShopID.String(), Source: "webhook",
			Action: "upsert", Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	}

	httputil.WriteOK(w, map[string]string{"status": "ok"})
}

// ── Redact Handlers ──────────────────────────────────────────────────────────

type shopifyCustomerRedactWebhook struct {
	ShopID       int64  `json:"shop_id"`
	ShopDomain   string `json:"shop_domain"`
	Customer     struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
		Phone string `json:"phone"`
	} `json:"customer"`
	OrdersToRedact []int64 `json:"orders_to_redact"`
}

func (h *WebhookHandler) handleCustomerRedact(w http.ResponseWriter, r *http.Request, rawBody []byte, shopDomain string) {
	shopID, err := h.resolveShopID(r.Context(), shopDomain)
	if err != nil {
		slog.Warn("webhook: customers/redact — could not resolve shop", "shop_domain", shopDomain, "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	var req shopifyCustomerRedactWebhook
	if err := json.Unmarshal(rawBody, &req); err != nil {
		slog.Error("webhook: customers/redact — failed to parse body", "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	customerID := fmt.Sprintf("%d", req.Customer.ID)
	customerEmail := req.Customer.Email

	tables := []struct {
		name  string
		model interface{}
		where string
		args  []interface{}
	}{
		{"reply_records", &model.ReplyRecord{}, "shop_id = ? AND customer_id = ?", []interface{}{shopID, customerID}},
		{"review_reply_codes", &model.ReviewReplyCode{}, "shop_id = ?", []interface{}{shopID}},
		{"cart_recovery_sends", &model.CartRecoverySend{}, "shop_id = ? AND customer_email = ?", []interface{}{shopID, customerEmail}},
		{"synced_orders", &model.SyncedOrder{}, "shop_id = ? AND customer_email = ?", []interface{}{shopID, customerEmail}},
		{"synced_checkouts", &model.SyncedCheckout{}, "shop_id = ? AND customer_email = ?", []interface{}{shopID, customerEmail}},
		{"customer_chat_profiles", &model.CustomerChatProfile{}, "shop_id = ? AND customer_id = ?", []interface{}{shopID, customerID}},
		{"synced_customers", &model.SyncedCustomer{}, "shop_id = ? AND platform_id = ?", []interface{}{shopID, customerID}},
		{"customer_insights", &model.CustomerInsight{}, "shop_id = ? AND customer_email = ?", []interface{}{shopID, customerEmail}},
	}

	var totalRows int64
	for _, t := range tables {
		r := h.db.WithContext(r.Context()).Where(t.where, t.args...).Delete(t.model)
		if r.Error != nil {
			slog.Warn("webhook: customers/redact — delete failed", "table", t.name, "error", r.Error)
		}
		totalRows += r.RowsAffected
	}

	slog.Info("webhook: customers/redact — data deleted", "shop_id", shopID, "customer_id", customerID, "email", customerEmail, "total_rows", totalRows)
	httputil.WriteOK(w, map[string]string{"status": "ok"})
}

func (h *WebhookHandler) handleShopRedact(w http.ResponseWriter, r *http.Request, rawBody []byte, shopDomain string) {
	shopID, err := h.resolveShopID(r.Context(), shopDomain)
	if err != nil {
		slog.Warn("webhook: shop/redact — could not resolve shop", "shop_domain", shopDomain, "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	tables := []struct {
		name  string
		model interface{}
	}{
		{"synced_products", &model.SyncedProduct{}}, {"synced_orders", &model.SyncedOrder{}},
		{"synced_customers", &model.SyncedCustomer{}}, {"synced_checkouts", &model.SyncedCheckout{}},
		{"synced_reviews", &model.SyncedReview{}}, {"reply_records", &model.ReplyRecord{}},
		{"review_reply_codes", &model.ReviewReplyCode{}}, {"auto_reply_configs", &model.AutoReplyConfig{}},
		{"cart_recovery_campaigns", &model.CartRecoveryCampaign{}}, {"cart_recovery_codes", &model.CartRecoveryCode{}},
		{"cart_recovery_sends", &model.CartRecoverySend{}}, {"customer_chat_profiles", &model.CustomerChatProfile{}},
		{"content_pieces", &model.ContentPiece{}}, {"content_performances", &model.ContentPerformance{}},
		{"social_connections", &model.SocialConnection{}}, {"social_posts", &model.SocialPost{}},
		{"sales_agent_configs", &model.SalesAgentConfig{}},
	}

	for _, t := range tables {
		r := h.db.WithContext(r.Context()).Where("shop_id = ?", shopID).Delete(t.model)
		if r.Error != nil {
			slog.Warn("webhook: shop/redact — delete failed", "table", t.name, "error", r.Error)
		}
	}

	slog.Info("webhook: shop/redact — all data purged", "shop_id", shopID, "shop_domain", shopDomain)
	httputil.WriteOK(w, map[string]string{"status": "ok"})
}
