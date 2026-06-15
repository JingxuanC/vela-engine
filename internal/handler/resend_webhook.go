// Package handler provides HTTP handlers for the Vela AI API.
package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// ResendWebhookHandler handles incoming Resend email webhooks.
type ResendWebhookHandler struct {
	db *gorm.DB
}

// NewResendWebhookHandler creates a new ResendWebhookHandler.
func NewResendWebhookHandler(db *gorm.DB) *ResendWebhookHandler {
	return &ResendWebhookHandler{db: db}
}

// ResendWebhookEvent is the top-level Resend webhook payload.
// https://resend.com/docs/dashboard/webhooks/introduction
type ResendWebhookEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// ResendEmailOpenedData is the data payload for type="email.opened".
type ResendEmailOpenedData struct {
	EmailID   string `json:"email_id"`
	CreatedAt string `json:"created_at"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Subject   string `json:"subject,omitempty"`
}

// Handle is the unified entry point: POST /webhooks/resend/{shop_id}
func (h *ResendWebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	shopIDStr := chi.URLParam(r, "shop_id")
	if shopIDStr == "" {
		slog.Warn("resend_webhook: missing shop_id in URL")
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	shopID, err := database.ResolveShopID(h.db, shopIDStr)
	if err != nil {
		slog.Warn("resend_webhook: invalid shop_id", "shop_id", shopIDStr, "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	// 1. Read raw body
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("resend_webhook: failed to read body", "shop_id", shopID, "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	// 2. Parse webhook event
	var event ResendWebhookEvent
	if err := json.Unmarshal(rawBody, &event); err != nil {
		slog.Error("resend_webhook: failed to parse event body",
			"shop_id", shopID,
			"error", err,
		)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	slog.Info("resend_webhook: received event",
		"shop_id", shopID,
		"event_type", event.Type,
	)

	// 3. Only handle email.opened events
	if event.Type != "email.opened" {
		slog.Debug("resend_webhook: ignoring non-open event",
			"shop_id", shopID,
			"event_type", event.Type,
		)
		httputil.WriteOK(w, map[string]string{"status": "ok", "ignored": event.Type})
		return
	}

	// 4. Parse the data payload
	var data ResendEmailOpenedData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		slog.Error("resend_webhook: failed to parse email.opened data",
			"shop_id", shopID,
			"error", err,
		)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	if data.EmailID == "" {
		slog.Warn("resend_webhook: email.opened event missing email_id",
			"shop_id", shopID,
		)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	// 5. Query CartRecoverySend by resend_message_id AND shop_id
	var send model.CartRecoverySend
	err = h.db.WithContext(r.Context()).
		Where("resend_message_id = ? AND shop_id = ?", data.EmailID, shopID).
		First(&send).Error
	if err != nil {
		// Not found — could be a different email type, not an error
		slog.Debug("resend_webhook: no matching cart recovery send found",
			"resend_message_id", data.EmailID,
			"shop_id", shopID,
			"error", err,
		)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	// 6. Update status to "opened" and set opened_at
	now := time.Now()
	updates := map[string]interface{}{
		"status":    "opened",
		"opened_at": now,
	}
	if err := h.db.WithContext(r.Context()).
		Model(&send).
		Updates(updates).Error; err != nil {
		slog.Error("resend_webhook: failed to update send record",
			"send_id", send.ID,
			"resend_message_id", data.EmailID,
			"shop_id", shopID,
			"error", err,
		)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	slog.Info("resend_webhook: marked email as opened",
		"send_id", send.ID,
		"resend_message_id", data.EmailID,
		"shop_id", shopID,
		"customer_email", send.CustomerEmail,
	)

	// 7. Always return 200 OK
	httputil.WriteOK(w, map[string]string{"status": "ok"})
}

