package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/platform/sync"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

type shopifyAppUninstalledWebhook struct {
	ID        int64  `json:"id"`
	Domain    string `json:"domain"`
	ShopOwner string `json:"shop_owner"`
	Email     string `json:"email"`
}

// ActivateTheme handles POST /api/shop/activate-theme — one-shot install initialization:
// 1. Save Shopify access token
// 2. Enable App Embed Block in theme
// 3. Create default LLM config (DeepSeek)
// 4. Register Shopify webhooks for product/order/customer sync
// 5. Trigger initial product sync
func (h *WebhookHandler) ActivateTheme(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShopDomain  string `json:"shop_domain"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ShopDomain == "" || req.AccessToken == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_domain and access_token required")
		return
	}

	// 1. Save real Shopify access token to shop record synchronously.
	if h.db != nil {
		if err := h.db.WithContext(r.Context()).Model(&model.Shop{}).
			Where("shop_domain = ?", req.ShopDomain).
			Update("access_token", req.AccessToken).Error; err != nil {
			slog.Warn("webhook: failed to save access token for shop", "shop", req.ShopDomain, "error", err)
		} else {
			slog.Info("webhook: saved Shopify access token", "shop", req.ShopDomain)
		}

		// 2. Create default LLM config (DeepSeek) if not exists
		var shop model.Shop
		if err := h.db.WithContext(r.Context()).Where("shop_domain = ?", req.ShopDomain).First(&shop).Error; err == nil {
			var count int64
			h.db.WithContext(r.Context()).Model(&model.ShopLLMConfig{}).Where("shop_id = ?", shop.ID).Count(&count)
			if count == 0 {
				h.db.WithContext(r.Context()).Create(&model.ShopLLMConfig{
					ShopID:   shop.ID,
					Provider: "deepseek",
					Model:    "deepseek-chat",
				})
				slog.Info("webhook: created default LLM config", "shop", req.ShopDomain)
			}
		}
	}

	// 3. Enable App Embed Block (async)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		activator := service.NewThemeActivator(h.cfg.ShopifyAPIVersion)
		if err := activator.EnableAppEmbedBlock(ctx, req.ShopDomain, req.AccessToken, "vtron-theme"); err != nil {
			slog.Warn("webhook: theme auto-activation failed", "shop", req.ShopDomain, "error", err)
		}
	}()

	// 4. Register Shopify webhooks for real-time sync (async)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		registrar := sync.NewWebhookRegistrar(h.cfg.ShopifyAPIVersion)
		if err := registrar.RegisterSyncWebhooks(ctx, req.ShopDomain, req.AccessToken); err != nil {
			slog.Warn("webhook: webhook registration failed", "shop", req.ShopDomain, "error", err)
		} else {
			slog.Info("webhook: registered Shopify webhooks", "shop", req.ShopDomain)
		}
	}()

	// 5. Trigger initial data sync (async)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		// Resolve shop ID
		var shop model.Shop
		if h.db == nil || h.db.WithContext(ctx).Where("shop_domain = ?", req.ShopDomain).First(&shop).Error != nil {
			slog.Warn("webhook: initial sync skipped — shop not found", "shop", req.ShopDomain)
			return
		}
		syncClient := sync.NewShopifyRESTClient(h.cfg.ShopifyAPIVersion)
		opts := sync.SyncOptions{ShopID: shop.ID, FullSync: true, Limit: 250}
		productSyncer := sync.NewProductSyncer(h.db, syncClient, h.eventBus)
		orderSyncer := sync.NewOrderSyncer(h.db, syncClient, h.eventBus)
		customerSyncer := sync.NewCustomerSyncer(h.db, syncClient, h.eventBus)
		if _, err := productSyncer.Sync(ctx, opts); err != nil {
			slog.Warn("webhook: initial product sync failed", "shop", req.ShopDomain, "error", err)
		}
		if _, err := orderSyncer.Sync(ctx, opts); err != nil {
			slog.Warn("webhook: initial order sync failed", "shop", req.ShopDomain, "error", err)
		}
		if _, err := customerSyncer.Sync(ctx, opts); err != nil {
			slog.Warn("webhook: initial customer sync failed", "shop", req.ShopDomain, "error", err)
		}
		slog.Info("webhook: initial sync triggered", "shop", req.ShopDomain)
	}()

	httputil.WriteOK(w, map[string]string{"status": "installing"})
}

func (h *WebhookHandler) handleAppUninstalled(w http.ResponseWriter, r *http.Request, rawBody []byte, shopDomain string) {
	if shopDomain == "" {
		var shopify shopifyAppUninstalledWebhook
		if err := json.Unmarshal(rawBody, &shopify); err == nil && shopify.Domain != "" { shopDomain = shopify.Domain }
	}
	if shopDomain == "" {
		slog.Error("webhook: app/uninstalled missing shop domain")
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	now := time.Now().UTC()
	if err := h.db.WithContext(r.Context()).Model(&model.Shop{}).Where("shop_domain = ?", shopDomain).Updates(map[string]interface{}{"uninstalled_at": &now, "billing_status": "cancelled"}).Error; err != nil {
		slog.Error("webhook: failed to mark shop as uninstalled", "shop_domain", shopDomain, "error", err)
	}

	// GDPR: immediately delete all shop data
	var shop model.Shop
	if err := h.db.WithContext(r.Context()).Where("shop_domain = ?", shopDomain).First(&shop).Error; err == nil {
		if h.cleanup != nil {
			_ = h.cleanup.CleanupAll(r.Context(), shop.ID)
		}
	}

	slog.Info("webhook: shop uninstalled", "shop_domain", shopDomain)
	httputil.WriteOK(w, map[string]string{"status": "ok"})
}

// ShopUninstall handles POST /api/shop/uninstall — called by Remix webhook handler.
func (h *WebhookHandler) ShopUninstall(w http.ResponseWriter, r *http.Request) {
	var req struct{ ShopID string `json:"shop_id"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ShopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "missing shop_id")
		return
	}
	if h.db == nil { httputil.WriteOK(w, map[string]string{"status": "ok"}); return }

	now := time.Now().UTC()
	query := h.db.WithContext(r.Context()).Model(&model.Shop{})
	if _, err := uuid.Parse(req.ShopID); err == nil {
		query = query.Where("id = ?", req.ShopID)
	} else {
		query = query.Where("shop_domain = ?", req.ShopID)
	}
	_ = query.Updates(map[string]interface{}{"uninstalled_at": &now, "billing_status": "cancelled"})

	// GDPR: immediately delete all shop data
	var shop model.Shop
	if _, err := uuid.Parse(req.ShopID); err == nil {
		h.db.WithContext(r.Context()).Where("id = ?", req.ShopID).First(&shop)
	} else {
		h.db.WithContext(r.Context()).Where("shop_domain = ?", req.ShopID).First(&shop)
	}
	if shop.ID != uuid.Nil && h.cleanup != nil {
		if err := h.cleanup.CleanupAll(r.Context(), shop.ID); err != nil {
			slog.Error("webhook: cleanup failed after uninstall", "shop_id", shop.ID, "error", err)
		}
	}

	slog.Info("shop_uninstall: shop cleaned up", "shop_id", req.ShopID)
	httputil.WriteOK(w, map[string]string{"status": "ok"})
}

func (h *WebhookHandler) handleAppSubscriptionUpdate(w http.ResponseWriter, r *http.Request, rawBody []byte, shopDomain string) {
	var payload struct {
		AppSubscription struct {
			ID     string `json:"admin_graphql_api_id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"app_subscription"`
	}
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		slog.Error("webhook: failed to parse app_subscriptions/update", "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	status := strings.ToLower(payload.AppSubscription.Status)
	var billingStatus string
	switch status {
	case "active": billingStatus = "active"
	case "cancelled", "declined": billingStatus = "cancelled"
	case "frozen", "past_due": billingStatus = "past_due"
	default: billingStatus = status
	}

	updates := map[string]interface{}{"subscription_id": payload.AppSubscription.ID, "billing_status": billingStatus}
	if err := h.db.WithContext(r.Context()).Model(&model.Shop{}).Where("shop_domain = ?", shopDomain).Updates(updates).Error; err != nil {
		slog.Error("webhook: failed to update subscription status", "shop", shopDomain, "error", err)
	} else {
		slog.Info("webhook: subscription status updated", "shop", shopDomain, "status", billingStatus)
	}
	httputil.WriteOK(w, map[string]string{"status": "ok"})
}
