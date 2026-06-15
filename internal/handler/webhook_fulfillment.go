package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// shopifyFulfillmentWebhook is the payload for Shopify's fulfillments/create webhook.
type shopifyFulfillmentWebhook struct {
	ID              int64  `json:"id"`
	OrderID         int64  `json:"order_id"`
	Status          string `json:"status"` // "success"
	TrackingCompany string `json:"tracking_company"`
	TrackingNumber  string `json:"tracking_number"`
	TrackingURL     string `json:"tracking_url"`
	LocationID      int64  `json:"location_id"`
	LineItems       []struct {
		ID       int64 `json:"id"`
		Quantity int   `json:"quantity"`
	} `json:"line_items"`
}

// handleFulfillmentCreate processes Shopify fulfillments/create webhooks.
// When a fulfillment is created in Shopify, it triggers a GraphQL call to pull
// the full fulfillment event timeline and persist it to fulfillment_events.
func (h *WebhookHandler) handleFulfillmentCreate(w http.ResponseWriter, r *http.Request, rawBody []byte, shopDomain string) {
	shopID, err := h.resolveShopID(r.Context(), shopDomain)
	if err != nil {
		slog.Warn("webhook: could not resolve shop for fulfillment", "shop_domain", shopDomain, "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	var fulfillment shopifyFulfillmentWebhook
	if err := json.Unmarshal(rawBody, &fulfillment); err != nil {
		slog.Error("webhook: failed to parse fulfillment body", "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	platformID := fmt.Sprintf("%d", fulfillment.OrderID)
	orderGID := fmt.Sprintf("gid://shopify/Order/%d", fulfillment.OrderID)

	slog.Info("webhook: fulfillments/create received",
		"shop_id", shopID,
		"order_id", platformID,
		"tracking_company", fulfillment.TrackingCompany,
		"tracking_number", fulfillment.TrackingNumber,
	)

	// If fulfillment tracker is available, trigger GraphQL sync to pull events
	if h.fulfillmentTracker != nil {
		count, err := h.fulfillmentTracker.SyncOrderFulfillments(r.Context(), shopID, orderGID, platformID)
		if err != nil {
			slog.Error("webhook: fulfillment sync failed",
				"shop_id", shopID,
				"order_id", platformID,
				"error", err,
			)
		} else {
			slog.Info("webhook: fulfillment synced",
				"shop_id", shopID,
				"order_id", platformID,
				"new_events", count,
			)
		}
	}

	httputil.WriteOK(w, map[string]string{"status": "ok"})
}
