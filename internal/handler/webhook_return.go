package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm/clause"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

type shopifyReturnWebhook struct {
	ID       int64                          `json:"id"`
	Name     string                         `json:"name"`
	Status   string                         `json:"status"`
	OrderID  int64                          `json:"order_id"`
	Customer *shopifyReturnCustomer         `json:"customer,omitempty"`
	ReturnLineItems []shopifyReturnLineItem `json:"return_line_items"`
}

type shopifyReturnCustomer struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type shopifyReturnLineItem struct {
	ID                    int64  `json:"id"`
	Quantity              int    `json:"quantity"`
	ReturnReason          string `json:"return_reason"`
	ReturnReasonNote      string `json:"return_reason_note"`
	LineItemID            int64  `json:"line_item_id"`
	FulfillmentLineItemID int64  `json:"fulfillment_line_item_id"`
}

func (h *WebhookHandler) handleReturn(w http.ResponseWriter, r *http.Request, topic string, rawBody []byte, shopDomain string) {
	shopID, err := h.resolveShopID(r.Context(), shopDomain)
	if err != nil {
		slog.Warn("webhook: could not resolve shop for return", "topic", topic, "shop_domain", shopDomain, "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	var shopify shopifyReturnWebhook
	if err := json.Unmarshal(rawBody, &shopify); err != nil {
		slog.Error("webhook: failed to parse return body", "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	platformID := fmt.Sprintf("%d", shopify.ID)
	lineItemsJSON, _ := json.Marshal(shopify.ReturnLineItems)

	customerEmail, customerName, customerID := "", "", ""
	if shopify.Customer != nil {
		customerEmail = shopify.Customer.Email
		customerName = shopify.Customer.FirstName + " " + shopify.Customer.LastName
		customerID = fmt.Sprintf("%d", shopify.Customer.ID)
	}

	record := model.SyncedReturn{
		ShopID: shopID, Platform: "shopify", PlatformID: platformID,
		Name: shopify.Name, Status: shopify.Status, OrderID: fmt.Sprintf("%d", shopify.OrderID),
		CustomerID: customerID, CustomerEmail: customerEmail, CustomerName: customerName,
		LineItems: datatypes.JSON(lineItemsJSON),
	}

	if err := h.db.WithContext(r.Context()).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "shop_id"}, {Name: "platform_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"status", "line_items", "customer_email", "customer_name", "updated_at"}),
	}).Create(&record).Error; err != nil {
		slog.Error("webhook: failed to upsert return", "platform_id", platformID, "error", err)
		return
	}

	slog.Info("webhook: return synced", "topic", topic, "return_name", shopify.Name, "status", shopify.Status, "line_items", len(shopify.ReturnLineItems))

	if h.eventBus != nil {
		ev, _ := eventbus.NewEvent(eventbus.EventReturnSynced, shopID, eventbus.PipelineEventPayload{
			EntityID: platformID, ShopID: shopID.String(), Source: "webhook",
			Action: "upsert", Timestamp: time.Now().UTC().Format(time.RFC3339),
		}, "webhook/"+topic)
		pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.eventBus.Publish(pubCtx, ev); err != nil {
			slog.Error("webhook: failed to publish return event", "platform_id", platformID, "error", err)
		}
	}

	if len(shopify.ReturnLineItems) > 0 {
		slog.Info("webhook: return for exchange consideration", "return_name", shopify.Name,
			"reason", shopify.ReturnLineItems[0].ReturnReason, "note", shopify.ReturnLineItems[0].ReturnReasonNote)
	}

	httputil.WriteOK(w, map[string]string{"status": "ok"})
}
