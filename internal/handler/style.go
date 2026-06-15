// Package handler provides HTTP handlers for the Vela AI API.
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/internal/service/tryon"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// StyleHandler manages per-product and per-store style configurations.
type StyleHandler struct {
	cache *service.CacheService
}

// NewStyleHandler creates a new StyleHandler.
func NewStyleHandler(cache *service.CacheService) *StyleHandler {
	return &StyleHandler{cache: cache}
}

// ProductStyleConfig holds the style configuration for a single product.
type ProductStyleConfig struct {
	ProductID       string `json:"product_id"`
	Preset          string `json:"preset"`
	RawPrompt       string `json:"raw_prompt"`
	OptimizedPrompt string `json:"optimized_prompt"`
	EnhanceEnabled  bool   `json:"enhance_enabled"`
}

// StoreStyleConfig holds the default style configuration for a store.
type StoreStyleConfig struct {
	DefaultPreset  string `json:"default_preset"`
	DefaultPrompt  string `json:"default_prompt"`
	EnhanceEnabled bool   `json:"enhance_enabled"`
}

// GetProductStyle handles GET /api/style/product?shop_id=xxx&product_id=xxx
// Returns the per-product style config, falling back to store default if not set.
func (h *StyleHandler) GetProductStyle(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	productID := r.URL.Query().Get("product_id")
	if shopID == "" || productID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id and product_id are required")
		return
	}

	ctx := r.Context()

	// Try product-specific config first
	key := fmt.Sprintf("style:product:%s", productID)
	raw, err := h.cache.Get(ctx, key)
	if err == nil && raw != "" {
		var cfg ProductStyleConfig
		if json.Unmarshal([]byte(raw), &cfg) == nil {
			httputil.WriteOK(w, cfg)
			return
		}
	}

	// Fall back to store default
	storeKey := fmt.Sprintf("style:store:%s", shopID)
	raw, err = h.cache.Get(ctx, storeKey)
	if err == nil && raw != "" {
		var store StoreStyleConfig
		if json.Unmarshal([]byte(raw), &store) == nil {
			httputil.WriteOK(w, ProductStyleConfig{
				ProductID:      productID,
				Preset:         store.DefaultPreset,
				RawPrompt:      store.DefaultPrompt,
				EnhanceEnabled: store.EnhanceEnabled,
			})
			return
		}
	}

	// System default
	httputil.WriteOK(w, ProductStyleConfig{
		ProductID:      productID,
		Preset:         "studio",
		EnhanceEnabled: true,
	})
}

// SetProductStyle handles POST /api/style/product
// Saves per-product style config.
func (h *StyleHandler) SetProductStyle(w http.ResponseWriter, r *http.Request) {
	var req ProductStyleConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.ProductID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "product_id is required")
		return
	}
	if req.Preset == "" {
		req.Preset = "studio"
	}

	ctx := r.Context()
	key := fmt.Sprintf("style:product:%s", req.ProductID)
	raw, _ := json.Marshal(req)
	if err := h.cache.Set(ctx, key, string(raw), 0); err != nil { // no TTL = persistent
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to save style config")
		return
	}

	httputil.WriteOK(w, map[string]interface{}{"success": true})
}

// DeleteProductStyle handles DELETE /api/style/product?product_id=xxx
func (h *StyleHandler) DeleteProductStyle(w http.ResponseWriter, r *http.Request) {
	productID := r.URL.Query().Get("product_id")
	if productID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "product_id is required")
		return
	}

	ctx := r.Context()
	key := fmt.Sprintf("style:product:%s", productID)
	h.cache.Delete(ctx, key)

	httputil.WriteOK(w, map[string]interface{}{"success": true})
}

// GetStoreStyle handles GET /api/style/store?shop_id=xxx
func (h *StyleHandler) GetStoreStyle(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	ctx := r.Context()
	key := fmt.Sprintf("style:store:%s", shopID)
	raw, err := h.cache.Get(ctx, key)
	if err == nil && raw != "" {
		var store StoreStyleConfig
		if json.Unmarshal([]byte(raw), &store) == nil {
			httputil.WriteOK(w, store)
			return
		}
	}

	httputil.WriteOK(w, StoreStyleConfig{
		DefaultPreset:  "studio",
		EnhanceEnabled: true,
	})
}

// SetStoreStyle handles POST /api/style/store
func (h *StyleHandler) SetStoreStyle(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	var req StoreStyleConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.DefaultPreset == "" {
		req.DefaultPreset = "studio"
	}

	ctx := r.Context()
	key := fmt.Sprintf("style:store:%s", shopID)
	raw, _ := json.Marshal(req)
	if err := h.cache.Set(ctx, key, string(raw), 0); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to save store style")
		return
	}

	httputil.WriteOK(w, map[string]interface{}{"success": true})
}

// BatchGetProductStyles handles POST /api/style/products — batch fetch styles for multiple product IDs.
func (h *StyleHandler) BatchGetProductStyles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShopID     string   `json:"shop_id"`
		ProductIDs []string `json:"product_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if len(req.ProductIDs) == 0 {
		httputil.WriteOK(w, map[string]interface{}{"styles": []ProductStyleConfig{}})
		return
	}

	ctx := r.Context()
	results := make([]ProductStyleConfig, 0, len(req.ProductIDs))

	for _, pid := range req.ProductIDs {
		key := fmt.Sprintf("style:product:%s", pid)
		raw, err := h.cache.Get(ctx, key)
		if err != nil || raw == "" {
			// No per-product config, skip
			continue
		}
		var cfg ProductStyleConfig
		if json.Unmarshal([]byte(raw), &cfg) == nil {
			results = append(results, cfg)
		}
	}

	httputil.WriteOK(w, map[string]interface{}{"styles": results})
}

// GetStylePresets handles GET /api/style/presets — lists all available style presets.
func (h *StyleHandler) GetStylePresets(w http.ResponseWriter, r *http.Request) {
	presets := tryon.ListStylePresets()
	type PresetDTO struct {
		Key         string `json:"key"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	dto := make([]PresetDTO, len(presets))
	for i, p := range presets {
		dto[i] = PresetDTO{Key: p.Key, Name: p.Name, Description: p.Description}
	}
	httputil.WriteOK(w, map[string]interface{}{"success": true, "presets": dto})
}
