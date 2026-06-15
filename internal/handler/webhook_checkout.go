package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"gorm.io/gorm/clause"
)

type shopifyCheckoutWebhook struct {
	ID         int64                 `json:"id"`
	Token      string                `json:"token"`
	Email      string                `json:"email"`
	TotalPrice string                `json:"total_price"`
	Currency   string                `json:"currency"`
	LineItems  []shopifyLineItem     `json:"line_items"`
	Customer   *shopifyOrderCustomer `json:"customer,omitempty"`
}

func (h *WebhookHandler) handleCheckout(w http.ResponseWriter, r *http.Request, topic string, rawBody []byte, shopDomain string) {
	shopID, err := h.resolveShopID(r.Context(), shopDomain)
	if err != nil {
		slog.Warn("webhook: could not resolve shop for checkout", "topic", topic, "shop_domain", shopDomain, "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	var shopify shopifyCheckoutWebhook
	if err := json.Unmarshal(rawBody, &shopify); err != nil {
		slog.Error("webhook: failed to parse checkout body", "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	if shopify.Token == "" {
		slog.Warn("webhook: checkout with empty token, skipping")
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	totalPrice := 0.0
	if shopify.TotalPrice != "" {
		if price, err := strconv.ParseFloat(shopify.TotalPrice, 64); err != nil {
			slog.Warn("webhook: failed to parse checkout total_price", "token", shopify.Token, "value", shopify.TotalPrice, "error", err)
		} else { totalPrice = price }
	}

	customerName := ""
	if shopify.Customer != nil {
		customerName = strings.TrimSpace(shopify.Customer.FirstName + " " + shopify.Customer.LastName)
	}

	lineItemsJSON, _ := json.Marshal(shopify.LineItems)

	checkout := model.SyncedCheckout{
		ShopID: shopID, PlatformToken: shopify.Token, CustomerEmail: shopify.Email,
		CustomerName: customerName, TotalPrice: totalPrice, Currency: shopify.Currency,
		LineItems: lineItemsJSON,
	}

	if topic == "checkouts/create" { checkout.Status = "open" } else { checkout.Status = "ordered" }

	err = h.db.WithContext(r.Context()).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "shop_id"}, {Name: "platform_token"}},
		DoUpdates: clause.AssignmentColumns([]string{"customer_email", "customer_name", "total_price", "currency", "line_items", "updated_at"}),
	}).Create(&checkout).Error

	if err != nil {
		slog.Error("webhook: failed to upsert checkout", "token", shopify.Token, "error", err)
	} else {
		slog.Info("webhook: checkout synced", "topic", topic, "token", shopify.Token, "email", shopify.Email, "total_price", totalPrice, "line_items", len(shopify.LineItems))

		if h.eventBus != nil {
			ev, _ := eventbus.NewEvent(eventbus.EventCheckoutSynced, shopID, eventbus.PipelineEventPayload{
				EntityID: shopify.Token, ShopID: shopID.String(), Source: "webhook",
				Action: "upsert", Timestamp: time.Now().UTC().Format(time.RFC3339),
			}, "webhook/"+topic)
			pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := h.eventBus.Publish(pubCtx, ev); err != nil {
				slog.Error("webhook: failed to publish checkout event", "token", shopify.Token, "error", err)
			}
		}
	}

	httputil.WriteOK(w, map[string]string{"status": "ok"})
}
