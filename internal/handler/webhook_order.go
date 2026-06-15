package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// Shared types used by both order and checkout webhooks.

type shopifyOrderCustomer struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type shopifyLineItem struct {
	ID        int64  `json:"id"`
	ProductID int64  `json:"product_id"`
	VariantID int64  `json:"variant_id"`
	Title     string `json:"title"`
	Quantity  int    `json:"quantity"`
	Price     string `json:"price"`
	Sku       string `json:"sku"`
}

type shopifyAddress struct {
	Address1  string `json:"address1"`
	Address2  string `json:"address2"`
	City      string `json:"city"`
	Province  string `json:"province"`
	Zip       string `json:"zip"`
	Country   string `json:"country"`
	Phone     string `json:"phone"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type shopifyDiscountCode struct {
	Code   string `json:"code"`
	Amount string `json:"amount"`
	Type   string `json:"type"`
}

// --- Order Webhook ---

type shopifyOrderWebhook struct {
	ID                int64                 `json:"id"`
	OrderNumber       int                   `json:"order_number"`
	Email             string                `json:"email"`
	Customer          *shopifyOrderCustomer `json:"customer"`
	TotalPrice        string                `json:"total_price"`
	Currency          string                `json:"currency"`
	FinancialStatus   string                `json:"financial_status"`
	FulfillmentStatus string                `json:"fulfillment_status"`
	LineItems         []shopifyLineItem     `json:"line_items"`
	DiscountCodes     []shopifyDiscountCode  `json:"discount_codes"`
	ShippingAddress   *shopifyAddress       `json:"shipping_address"`
}

func (h *WebhookHandler) handleOrder(w http.ResponseWriter, r *http.Request, topic string, rawBody []byte, shopDomain string) {
	shopID, err := h.resolveShopID(r.Context(), shopDomain)
	if err != nil {
		slog.Warn("webhook: could not resolve shop for order", "topic", topic, "shop_domain", shopDomain, "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	var shopify shopifyOrderWebhook
	if err := json.Unmarshal(rawBody, &shopify); err != nil {
		slog.Error("webhook: failed to parse order body", "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	platformID := fmt.Sprintf("%d", shopify.ID)
	customerName := ""
	if shopify.Customer != nil {
		customerName = strings.TrimSpace(shopify.Customer.FirstName + " " + shopify.Customer.LastName)
	}
	lineItemsJSON, _ := json.Marshal(shopify.LineItems)
	shippingJSON, _ := json.Marshal(shopify.ShippingAddress)

	totalPrice := 0.0
	if shopify.TotalPrice != "" {
		if price, err := strconv.ParseFloat(shopify.TotalPrice, 64); err != nil {
			slog.Warn("webhook: failed to parse order total_price", "order_id", shopify.ID, "value", shopify.TotalPrice, "error", err)
		} else {
			totalPrice = price
		}
	}

	order := model.SyncedOrder{
		ShopID: shopID, PlatformID: platformID, OrderNumber: shopify.OrderNumber,
		CustomerEmail: shopify.Email, CustomerName: customerName, TotalPrice: totalPrice,
		Currency: shopify.Currency, FinancialStatus: shopify.FinancialStatus,
		FulfillmentStatus: shopify.FulfillmentStatus, LineItems: lineItemsJSON,
		ShippingAddress: shippingJSON,
	}

	err = h.db.WithContext(r.Context()).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "shop_id"}, {Name: "platform_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"order_number", "customer_email", "customer_name", "total_price", "currency", "financial_status", "fulfillment_status", "line_items", "shipping_address"}),
	}).Create(&order).Error

	if err != nil {
		slog.Error("webhook: failed to upsert order", "platform_id", platformID, "error", err)
	} else {
		evType := eventbus.EventOrderUpdated
		if topic == "orders/create" { evType = eventbus.EventOrderCreated }
		h.publishEvent(evType, order.ShopID, order)
		h.publishEvent(eventbus.EventOrderSynced, order.ShopID, eventbus.PipelineEventPayload{
			EntityID: platformID, ShopID: order.ShopID.String(), Source: "webhook",
			Action: "upsert", Timestamp: time.Now().UTC().Format(time.RFC3339),
		})

		// If order is fulfilled, publish review generation event
		if shopify.FulfillmentStatus == "fulfilled" {
			for _, li := range shopify.LineItems {
				if li.ProductID == 0 {
					continue
				}
				h.publishEvent(eventbus.EventOrderFulfilled, order.ShopID, struct {
					OrderID       int64  `json:"order_id"`
					ProductID     int64  `json:"product_id"`
					ProductTitle  string `json:"product_title"`
					ProductImage  string `json:"product_image"`
					CustomerEmail string `json:"customer_email"`
					CustomerName  string `json:"customer_name"`
				}{
					OrderID:       shopify.ID,
					ProductID:     li.ProductID,
					ProductTitle:  li.Title,
					CustomerEmail: order.CustomerEmail,
					CustomerName:  order.CustomerName,
				})
			}
		}

		if h.cartRecoveryAttributor != nil && topic == "orders/create" && len(shopify.DiscountCodes) > 0 {
			go func() {
				defer recoverAttribution("cart_recovery", platformID)
				campaignIDs, err := h.cartRecoveryAttributor.AttributeOrder(bgTimeout(30*time.Second), shopID, platformID, totalPrice, shopify.DiscountCodes)
				if err != nil {
					slog.Error("webhook: cart recovery attribution failed", "order_id", platformID, "error", err)
				} else if len(campaignIDs) > 0 {
					slog.Info("webhook: cart recovery attribution matched", "order_id", platformID, "campaigns", len(campaignIDs))
				}
			}()
		}

		if h.contentAttributor != nil && topic == "orders/create" {
			go func() {
				defer recoverAttribution("content", platformID)
				result, err := h.contentAttributor.AttributeOrder(bgTimeout(30*time.Second), shopID, platformID, totalPrice)
				if err != nil {
					slog.Error("webhook: content attribution failed", "order_id", platformID, "error", err)
				} else if result != nil && result.Attributed {
					slog.Info("webhook: content attribution matched", "order_id", platformID, "short_id", result.ShortID, "content_piece_id", result.ContentPieceID, "revenue", result.Revenue)
				}
			}()
		}

		if topic == "orders/create" && len(shopify.DiscountCodes) > 0 {
			go func() {
				defer recoverAttribution("review_recovery", platformID)
				h.attributeReviewRecovery(bgTimeout(30*time.Second), shopID, platformID, totalPrice, shopify.DiscountCodes)
			}()
		}

		if topic == "orders/create" && shopify.Email != "" {
			go func() {
				defer recoverAttribution("repurchase", platformID)
				h.attributeRepurchase(bgTimeout(30*time.Second), shopID, platformID, totalPrice, shopify.Email)
			}()
		}
	}

	httputil.WriteOK(w, map[string]string{"status": "ok"})
}

// attributeReviewRecovery matches RV-* discount codes in an order to auto reply records.
func (h *WebhookHandler) attributeReviewRecovery(ctx context.Context, shopID uuid.UUID, platformID string, totalPrice float64, codes []shopifyDiscountCode) {
	for _, dc := range codes {
		if !strings.HasPrefix(strings.ToUpper(dc.Code), "RV-") { continue }
		var rrc model.ReviewReplyCode
		err := h.db.WithContext(ctx).Where("code = ? AND shop_id = ? AND is_used = false", strings.ToUpper(dc.Code), shopID).First(&rrc).Error
		if err != nil { continue }
		now := time.Now()
		h.db.WithContext(ctx).Model(&rrc).Updates(map[string]interface{}{"is_used": true, "used_at": now, "order_total": totalPrice})
		h.db.WithContext(ctx).Model(&model.ReplyRecord{}).Where("id = ?", rrc.ReplyRecordID).Update("discount_used", true)
		slog.Info("webhook: auto reply attribution", "order_id", platformID, "code", dc.Code, "revenue", totalPrice)
	}
}

// attributeRepurchase checks if an order came from a customer who previously received an auto-reply.
func (h *WebhookHandler) attributeRepurchase(ctx context.Context, shopID uuid.UUID, orderID string, totalPrice float64, customerEmail string) {
	var rr model.ReplyRecord
	err := h.db.WithContext(ctx).Where("shop_id = ? AND customer_email = ? AND status = ?", shopID, customerEmail, "sent").Order("sent_at DESC").First(&rr).Error
	if err != nil { return }

	var existing model.ReviewRecoveryAttribution
	err = h.db.WithContext(ctx).Where("shop_id = ? AND customer_email = ? AND repurchase_order_id = ?", shopID, customerEmail, orderID).First(&existing).Error
	if err == nil { return }

	attr := model.ReviewRecoveryAttribution{
		ShopID: shopID, CustomerEmail: customerEmail, ReplyRecordID: rr.ID,
		OriginalReviewID: rr.PlatformID, RepurchaseOrderID: orderID,
		RepurchaseAmount: totalPrice, RepurchasedAt: time.Now(),
	}
	if createErr := h.db.WithContext(ctx).Create(&attr).Error; createErr != nil {
		slog.Warn("webhook: failed to create repurchase attribution", "order_id", orderID, "error", createErr)
		return
	}
	slog.Info("webhook: repurchase attributed to auto reply", "order_id", orderID, "customer", customerEmail, "reply_record_id", rr.ID, "amount", totalPrice)
}
