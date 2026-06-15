package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// SalesAgentSettingsHandler serves Sales Agent configuration and analytics.
type SalesAgentSettingsHandler struct {
	db *gorm.DB
}

// NewSalesAgentSettingsHandler creates a new handler.
func NewSalesAgentSettingsHandler(db *gorm.DB) *SalesAgentSettingsHandler {
	return &SalesAgentSettingsHandler{db: db}
}

// ── Config ───────────────────────────────────────────────────────────────────

// GetConfig returns the Sales Agent config for a shop (creates default if missing).
func (h *SalesAgentSettingsHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	var cfg model.SalesAgentConfig
	err := h.db.WithContext(r.Context()).
		Where("shop_id = ?", shopID).
		First(&cfg).Error

	if err != nil {
		// Return defaults if no config exists yet
		httputil.WriteOK(w, map[string]interface{}{
			"enabled":         true,
			"welcome_message": "Hi! Welcome to our store. What are you looking for today? 👋",
			"brand_voice":     "friendly",
		})
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"enabled":         cfg.Enabled,
		"welcome_message": cfg.WelcomeMessage,
		"brand_voice":     cfg.BrandVoice,
	})
}

// UpdateConfigRequest is the request body for PUT /api/shop/sales-agent/config.
type UpdateConfigRequest struct {
	ShopID         string `json:"shop_id"`
	Enabled        *bool  `json:"enabled"`
	WelcomeMessage string `json:"welcome_message"`
	BrandVoice     string `json:"brand_voice"`
}

// UpdateConfig saves or updates the Sales Agent config for a shop.
func (h *SalesAgentSettingsHandler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req UpdateConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ShopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	// Validate brand_voice value
	if req.BrandVoice != "" && req.BrandVoice != "friendly" && req.BrandVoice != "professional" && req.BrandVoice != "luxury" && req.BrandVoice != "playful" {
		httputil.WriteError(w, http.StatusBadRequest, "brand_voice must be one of: friendly, professional, luxury, playful")
		return
	}
	if len(req.WelcomeMessage) > 200 {
		httputil.WriteError(w, http.StatusBadRequest, "welcome_message must be 200 characters or less")
		return
	}

	// Upsert: find existing or create new
	var cfg model.SalesAgentConfig
	err := h.db.WithContext(r.Context()).
		Where("shop_id = ?", req.ShopID).
		First(&cfg).Error

	if err != nil {
		// Create new config with defaults + provided values
		cfg = model.SalesAgentConfig{
			WelcomeMessage: "Hi! Welcome to our store. What are you looking for today? 👋",
			BrandVoice:     "friendly",
			Enabled:        true,
		}
		// Parse shop_id string to UUID
		var shop model.Shop
		if err := h.db.WithContext(r.Context()).Where("id = ?", req.ShopID).Select("id").First(&shop).Error; err != nil {
			httputil.WriteError(w, http.StatusNotFound, "shop not found")
			return
		}
		cfg.ShopID = shop.ID
	}

	// Apply updates
	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled
	}
	if req.WelcomeMessage != "" {
		cfg.WelcomeMessage = req.WelcomeMessage
	}
	if req.BrandVoice != "" {
		cfg.BrandVoice = req.BrandVoice
	}

	if err := h.db.WithContext(r.Context()).Save(&cfg).Error; err != nil {
		slog.Error("sales_agent_settings: failed to save config", "shop_id", req.ShopID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
	})
}

// ── Stats ────────────────────────────────────────────────────────────────────

// StatsResponse is the response for GET /api/shop/sales-agent/stats.
type StatsResponse struct {
	TotalConversations  int64               `json:"totalConversations"`
	ProductsRecommended int64               `json:"productsRecommended"`
	DiscountsGenerated  int64               `json:"discountsGenerated"`
	IntentToBuy         int64               `json:"intentToBuy"`
	TopProducts         []ProductChatStat   `json:"topProducts"`
	TopObjections       []ObjectionStat     `json:"topObjections"`
}

// ProductChatStat is per-product chat statistics.
type ProductChatStat struct {
	Name        string `json:"name"`
	Recommended int64  `json:"recommended"`
	Purchased   int64  `json:"purchased"`
}

// ObjectionStat is per-objection-type statistics.
type ObjectionStat struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

// GetStats returns aggregated Sales Agent statistics from chat_intent_events.
func (h *SalesAgentSettingsHandler) GetStats(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}
	if h.db == nil {
		httputil.WriteOK(w, StatsResponse{})
		return
	}

	ctx := r.Context()
	var resp StatsResponse

	// Total unique conversations (sessions with at least one intent event)
	h.db.WithContext(ctx).
		Table("chat_intent_events").
		Where("shop_id = ?", shopID).
		Select("COUNT(DISTINCT session_id)").
		Scan(&resp.TotalConversations)

	// Products recommended (purchase_signal events with a product_id)
	h.db.WithContext(ctx).
		Table("chat_intent_events").
		Where("shop_id = ? AND event_type = ? AND product_id != ''", shopID, "purchase_signal").
		Count(&resp.ProductsRecommended)

	// Discounts generated
	h.db.WithContext(ctx).
		Table("chat_intent_events").
		Where("shop_id = ? AND event_type IN ?", shopID, []string{"discount_request", "price_objection"}).
		Count(&resp.DiscountsGenerated)

	// Intent-to-buy events (purchase_signal)
	h.db.WithContext(ctx).
		Table("chat_intent_events").
		Where("shop_id = ? AND event_type = ?", shopID, "purchase_signal").
		Count(&resp.IntentToBuy)

	// Top objections
	h.db.WithContext(ctx).
		Table("chat_intent_events").
		Where("shop_id = ?", shopID).
		Select("event_type as reason, COUNT(*) as count").
		Group("event_type").
		Order("count DESC").
		Limit(5).
		Scan(&resp.TopObjections)

	// Top recommended products — query with temp struct then enrich
	type topProductRow struct {
		ProductID   string `gorm:"column:product_id"`
		Recommended int64  `gorm:"column:recommended"`
	}
	var topRows []topProductRow
	h.db.WithContext(ctx).
		Table("chat_intent_events").
		Where("shop_id = ? AND product_id != ''", shopID).
		Select("product_id, COUNT(*) as recommended").
		Group("product_id").
		Order("recommended DESC").
		Limit(5).
		Scan(&topRows)

	// Batch-fetch product titles (avoid N+1)
	if len(topRows) > 0 {
		productIDs := make([]string, len(topRows))
		for i, row := range topRows {
			productIDs[i] = row.ProductID
		}
		var products []model.SyncedProduct
		h.db.WithContext(ctx).
			Where("platform_id IN ? AND shop_id = ?", productIDs, shopID).
			Select("platform_id, title").
			Find(&products)
		titleMap := make(map[string]string, len(products))
		for _, p := range products {
			titleMap[p.PlatformID] = p.Title
		}
		for _, row := range topRows {
			name := titleMap[row.ProductID]
			if name == "" {
				name = fmt.Sprintf("Product %s", row.ProductID)
			}
			resp.TopProducts = append(resp.TopProducts, ProductChatStat{
				Name:        name,
				Recommended: row.Recommended,
			})
		}
	}

	if resp.TopProducts == nil {
		resp.TopProducts = []ProductChatStat{}
	}
	if resp.TopObjections == nil {
		resp.TopObjections = []ObjectionStat{}
	}

	httputil.WriteOK(w, resp)
}
