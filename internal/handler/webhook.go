package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/config"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/internal/platform/taskqueue"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// WebhookHandler handles incoming Shopify webhooks.
type WebhookHandler struct {
	cfg                    *config.Config
	db                     *gorm.DB
	cache                  *service.CacheService
	eventBus               eventbus.EventBus
	cartRecoveryAttributor *CartRecoveryAttributor
	contentAttributor      *ContentAttributor
	taskClient             *taskqueue.AsynqClient
	fulfillmentTracker     *service.FulfillmentTracker
	cleanup                *service.ShopCleanup
}

// SetTaskClient sets the task queue client for async pipeline execution.
func (h *WebhookHandler) SetTaskClient(tc *taskqueue.AsynqClient) {
	h.taskClient = tc
}

// SetFulfillmentTracker injects the fulfillment tracker for handling fulfillments/create webhooks.
func (h *WebhookHandler) SetFulfillmentTracker(ft *service.FulfillmentTracker) {
	h.fulfillmentTracker = ft
}

// SetShopCleanup injects the shop data cleanup service for GDPR compliance.
func (h *WebhookHandler) SetShopCleanup(c *service.ShopCleanup) {
	h.cleanup = c
}

// NewWebhookHandler creates a new WebhookHandler.
func NewWebhookHandler(db *gorm.DB, cache *service.CacheService, cfg *config.Config, bus eventbus.EventBus, attrib *CartRecoveryAttributor, contentAttr *ContentAttributor) *WebhookHandler {
	return &WebhookHandler{db: db, cache: cache, cfg: cfg, eventBus: bus, cartRecoveryAttributor: attrib, contentAttributor: contentAttr}
}

// Handle is the unified entry point: POST /webhooks/{topic}
func (h *WebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	topic := chi.URLParam(r, "topic")
	if topic == "" {
		httputil.WriteError(w, http.StatusBadRequest, "missing topic")
		return
	}

	// 1. Read raw body
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("webhook: failed to read body", "topic", topic, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "failed to read body")
		return
	}

	// 2. Verify Shopify HMAC signature (fail open if no signature provided, for dev)
	if err := h.verifyHMAC(r, rawBody); err != nil {
		slog.Warn("webhook: HMAC verification failed", "topic", topic, "error", err)
		if h.cfg.IsProduction() {
			httputil.WriteError(w, http.StatusUnauthorized, "invalid HMAC signature")
			return
		}
	}

	// 3. Idempotency check via Redis SETNX (X-Shopify-Webhook-Id header)
	webhookID := r.Header.Get("X-Shopify-Webhook-Id")
	if webhookID != "" && h.cache != nil {
		idempotentKey := fmt.Sprintf("webhook:idempotent:%s", webhookID)
		set, err := h.cache.Client().SetNX(r.Context(), idempotentKey, "1", 48*time.Hour).Result()
		if err != nil {
			slog.Warn("webhook: idempotency check failed, continuing", "webhook_id", webhookID, "error", err)
		} else if !set {
			slog.Info("webhook: duplicate webhook, returning 200", "webhook_id", webhookID, "topic", topic)
			httputil.WriteOK(w, map[string]string{"status": "ok"})
			return
		}
	}

	// 4. Extract shop domain from header
	shopDomain := r.Header.Get("X-Shopify-Shop-Domain")
	if shopDomain == "" {
		shopDomain = r.Header.Get("X-Shop-Domain")
	}
	if shopDomain == "" {
		slog.Warn("webhook: missing shop domain header, trying to parse from body", "topic", topic)
	}

	// 5. Route to specific handler
	switch topic {
	case "products/create", "products/update":
		h.handleProduct(w, r, topic, rawBody, shopDomain)
	case "orders/create", "orders/updated":
		h.handleOrder(w, r, topic, rawBody, shopDomain)
	case "customers/create", "customers/update":
		h.handleCustomer(w, r, rawBody, shopDomain)
	case "customers/redact":
		h.handleCustomerRedact(w, r, rawBody, shopDomain)
	case "shop/redact":
		h.handleShopRedact(w, r, rawBody, shopDomain)
	case "returns/request", "returns/approve":
		h.handleReturn(w, r, topic, rawBody, shopDomain)
	case "app/uninstalled":
		h.handleAppUninstalled(w, r, rawBody, shopDomain)
	case "checkouts/create", "checkouts/update":
		h.handleCheckout(w, r, topic, rawBody, shopDomain)
	case "app_subscriptions/update":
		h.handleAppSubscriptionUpdate(w, r, rawBody, shopDomain)
	case "fulfillments/create":
		h.handleFulfillmentCreate(w, r, rawBody, shopDomain)
	case "orders/fulfilled":
		h.handleOrder(w, r, topic, rawBody, shopDomain)
	default:
		slog.Warn("webhook: unknown topic", "topic", topic)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
	}
}

// verifyHMAC verifies the X-Shopify-Hmac-Sha256 header against the raw body.
func (h *WebhookHandler) verifyHMAC(r *http.Request, rawBody []byte) error {
	signature := r.Header.Get("X-Shopify-Hmac-Sha256")
	if signature == "" {
		return fmt.Errorf("missing X-Shopify-Hmac-Sha256 header")
	}

	shopDomain := r.Header.Get("X-Shopify-Shop-Domain")
	if shopDomain == "" {
		return fmt.Errorf("missing X-Shopify-Shop-Domain header for HMAC verification")
	}

	var shop model.Shop
	if err := h.db.WithContext(r.Context()).Where("shop_domain = ?", shopDomain).First(&shop).Error; err != nil {
		return fmt.Errorf("shop not found: %w", err)
	}

	secret := h.cfg.ShopifyAPISecret
	if secret == "" {
		return fmt.Errorf("no secret available for HMAC verification")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(rawBody)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return fmt.Errorf("HMAC mismatch")
	}

	return nil
}

// resolveShopID returns the shop UUID for a given shop domain.
func (h *WebhookHandler) resolveShopID(ctx context.Context, shopDomain string) (uuid.UUID, error) {
	if shopDomain == "" {
		return uuid.Nil, fmt.Errorf("shop domain is empty")
	}
	var shop model.Shop
	if err := h.db.WithContext(ctx).Where("shop_domain = ?", shopDomain).First(&shop).Error; err != nil {
		return uuid.Nil, fmt.Errorf("shop not found for domain %s: %w", shopDomain, err)
	}
	return shop.ID, nil
}

// publishEvent publishes an event to the EventBus with timeout and error logging.
func (h *WebhookHandler) publishEvent(evType eventbus.EventType, shopID uuid.UUID, payload interface{}) {
	if h.eventBus == nil {
		return
	}
	ev, err := eventbus.NewEvent(evType, shopID, payload, "webhook")
	if err != nil {
		slog.Error("webhook: failed to create event", "type", evType, "error", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.eventBus.Publish(ctx, ev); err != nil {
		slog.Error("webhook: failed to publish event", "type", evType, "shop_id", shopID, "error", err)
	}
}

// recoverAttribution recovers from panics in attribution goroutines.
// Attribution failures are non-fatal — they just leak revenue attribution,
// so we log them prominently but don't crash the process.
func recoverAttribution(kind, orderID string) {
	if r := recover(); r != nil {
		slog.Error("webhook: attribution goroutine panic",
			"kind", kind, "order_id", orderID, "panic", r)
	}
}
