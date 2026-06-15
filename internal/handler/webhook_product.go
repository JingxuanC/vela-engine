package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"gorm.io/gorm/clause"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// --- Product Webhook ---

type shopifyProductWebhook struct {
	ID          int64                   `json:"id"`
	Title       string                  `json:"title"`
	BodyHTML    string                  `json:"body_html"`
	Vendor      string                  `json:"vendor"`
	ProductType string                  `json:"product_type"`
	Status      string                  `json:"status"`
	Variants    []shopifyProductVariant `json:"variants"`
	Images      []shopifyProductImage   `json:"images"`
	Options     []shopifyProductOption  `json:"options"`
	Tags        string                  `json:"tags"`
}

type shopifyProductVariant struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Price    string `json:"price"`
	Sku      string `json:"sku"`
	Position int    `json:"position"`
}

type shopifyProductImage struct {
	ID       int64  `json:"id"`
	Src      string `json:"src"`
	Position int    `json:"position"`
	Alt      string `json:"alt"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type shopifyProductOption struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Values   []string `json:"values"`
	Position int      `json:"position"`
}

func (h *WebhookHandler) handleProduct(w http.ResponseWriter, r *http.Request, topic string, rawBody []byte, shopDomain string) {
	shopID, err := h.resolveShopID(r.Context(), shopDomain)
	if err != nil {
		slog.Warn("webhook: could not resolve shop for product", "topic", topic, "shop_domain", shopDomain, "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	var shopify shopifyProductWebhook
	if err := json.Unmarshal(rawBody, &shopify); err != nil {
		slog.Error("webhook: failed to parse product body", "error", err)
		httputil.WriteOK(w, map[string]string{"status": "ok"})
		return
	}

	platformID := fmt.Sprintf("%d", shopify.ID)
	variantsJSON, _ := json.Marshal(shopify.Variants)
	imagesJSON, _ := json.Marshal(shopify.Images)
	optionsJSON, _ := json.Marshal(shopify.Options)

	product := model.SyncedProduct{
		ShopID: shopID, PlatformID: platformID,
		Title: shopify.Title, Description: shopify.BodyHTML,
		Vendor: shopify.Vendor, ProductType: shopify.ProductType,
		Status: shopify.Status, Variants: variantsJSON,
		Images: imagesJSON, Options: optionsJSON,
		Tags: shopify.Tags,
	}

	err = h.db.WithContext(r.Context()).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "shop_id"}, {Name: "platform_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"title", "description", "vendor", "product_type", "status", "variants", "images", "options", "tags", "currency_normalized", "region", "size_normalized", "category_path", "materials"}),
	}).Create(&product).Error

	if err != nil {
		slog.Error("webhook: failed to upsert product", "platform_id", platformID, "error", err)
	} else {
		evType := eventbus.EventProductUpdated
		if topic == "products/create" { evType = eventbus.EventProductCreated }
		h.publishEvent(evType, product.ShopID, product)
		h.publishEvent(eventbus.EventProductSynced, product.ShopID, eventbus.PipelineEventPayload{
			EntityID: platformID, ShopID: product.ShopID.String(), Source: "webhook",
			Action: "upsert", Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		h.enqueueEnrichment(r.Context(), shopID, platformID, product)
	}

	httputil.WriteOK(w, map[string]string{"status": "ok"})
}
