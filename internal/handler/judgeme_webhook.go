// Package handler provides HTTP handlers for the Vela AI API.
package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/internal/service/judgeme"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// JudgeMeWebhookHandler handles incoming Judge.me webhooks.
type JudgeMeWebhookHandler struct {
	db          *gorm.DB
	cache       *service.CacheService
	syncService *judgeme.SyncService
}

// NewJudgeMeWebhookHandler creates a new JudgeMeWebhookHandler.
func NewJudgeMeWebhookHandler(db *gorm.DB, cache *service.CacheService, eb eventbus.EventBus) *JudgeMeWebhookHandler {
	return &JudgeMeWebhookHandler{
		db:          db,
		cache:       cache,
		syncService: judgeme.NewSyncService(db, eb),
	}
}

// Handle is the unified entry point: POST /webhooks/judgeme/{shop_id}
func (h *JudgeMeWebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	shopIDStr := chi.URLParam(r, "shop_id")
	if shopIDStr == "" {
		slog.Warn("judgeme_webhook: missing shop_id in URL")
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	shopID, err := database.ResolveShopID(h.db, shopIDStr)
	if err != nil {
		slog.Warn("judgeme_webhook: invalid shop_id", "shop_id", shopIDStr, "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	// 1. Read raw body
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("judgeme_webhook: failed to read body", "shop_id", shopID, "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	// 2. Verify HMAC-SHA256 signature if secret is available
	if err := h.verifySignature(r, rawBody, shopID); err != nil {
		slog.Error("judgeme_webhook: signature verification failed — rejecting",
			"shop_id", shopID,
			"error", err,
		)
		httputil.WriteError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	// 3. Parse webhook event
	var event judgeme.WebhookEvent
	if err := json.Unmarshal(rawBody, &event); err != nil {
		slog.Error("judgeme_webhook: failed to parse event body",
			"shop_id", shopID,
			"error", err,
		)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	slog.Info("judgeme_webhook: received event",
		"shop_id", shopID,
		"event", event.Event,
		"review_id", event.ReviewID,
	)

	// 4. Process the event via sync service (fast — only saves to DB + publishes EventBus)
	if err := h.syncService.SyncFromWebhook(r.Context(), shopID, &event); err != nil {
		slog.Error("judgeme_webhook: processing failed",
			"shop_id", shopID,
			"event", event.Event,
			"review_id", event.ReviewID,
			"error", err,
		)
	} else {
		slog.Info("judgeme_webhook: processing successful",
			"shop_id", shopID,
			"event", event.Event,
			"review_id", event.ReviewID,
		)
	}

	// 5. Always return 200 OK
	httputil.WriteOK(w, map[string]string{"status": "ok"})
}

// verifySignature validates the Judge.me HMAC-SHA256 signature.
// Returns an error if the signature is missing, the secret is not configured,
// or the signature does not match.
func (h *JudgeMeWebhookHandler) verifySignature(r *http.Request, rawBody []byte, shopID uuid.UUID) error {
	signature := r.Header.Get("X-Judgeme-Signature")
	if signature == "" {
		return &signatureError{message: "missing X-Judgeme-Signature header"}
	}

	// Look up the shop's webhook secret
	var setting model.JudgeMeSetting
	if err := h.db.WithContext(r.Context()).Where("shop_id = ? AND is_active = ?", shopID, true).First(&setting).Error; err != nil {
		return &signatureError{message: fmt.Sprintf("JudgeMe settings not found for shop: %v", err)}
	}

	if setting.WebhookSecret == "" {
		return &signatureError{message: "webhook secret not configured for this shop"}
	}

	if !judgeme.VerifyWebhookSignature(signature, rawBody, setting.WebhookSecret) {
		return &signatureError{message: "HMAC signature mismatch"}
	}

	return nil
}

// signatureError is a custom error for signature verification failures.
type signatureError struct {
	message string
}

func (e *signatureError) Error() string {
	return e.message
}
