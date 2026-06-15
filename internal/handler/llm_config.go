package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// LLMConfigHandler serves LLM configuration CRUD and model listing.
type LLMConfigHandler struct {
	db        *gorm.DB
	llmRouter *service.LLMRouter
}

// NewLLMConfigHandler creates a new LLMConfigHandler.
func NewLLMConfigHandler(db *gorm.DB, llmRouter *service.LLMRouter) *LLMConfigHandler {
	return &LLMConfigHandler{db: db, llmRouter: llmRouter}
}

// GetConfig handles GET /api/llm/config?shop_id=xxx
func (h *LLMConfigHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	shopID, err := parseShopUUID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	provider, cfg, err := h.llmRouter.GetProvider(r.Context(), shopID)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var shop model.Shop
	plan := "free"
	if h.db != nil {
		if err := h.db.WithContext(r.Context()).Where("id = ?", shopID).First(&shop).Error; err == nil {
			plan = shop.Plan
		}
	}

	providers := []string{"deepseek", "dashscope", "openai"}
	models := h.llmRouter.ListModels(cfg.Provider, plan)

	httputil.WriteOK(w, map[string]interface{}{
		"success":          true,
		"provider":         cfg.Provider,
		"model":            cfg.Model,
		"temperature":      cfg.Temperature,
		"max_tokens":       cfg.MaxTokens,
		"api_key_masked":   maskKey(cfg.APIKey),
		"has_custom_key":   cfg.APIKey != "",
		"available_providers": providers,
		"available_models": models,
		"plan":            plan,
		"provider_name":    provider.ProviderName(),
	})
}

// UpdateConfig handles PUT /api/llm/config
func (h *LLMConfigHandler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	var req struct {
		ShopID      string  `json:"shop_id"`
		Provider    string  `json:"provider"`
		Model       string  `json:"model"`
		APIKey      string  `json:"api_key"`
		Temperature float64 `json:"temperature"`
		MaxTokens   int     `json:"max_tokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	shopID, err := uuid.Parse(req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid shop_id")
		return
	}

	// Validate provider
	validProviders := map[string]bool{"dashscope": true, "deepseek": true, "openai": true}
	if req.Provider != "" && !validProviders[req.Provider] {
		httputil.WriteError(w, http.StatusBadRequest, "unsupported provider: "+req.Provider)
		return
	}

	// Enforce billing plan allowlist (skip if BYOK)
	if req.APIKey == "" && req.Model != "" {
		var shop model.Shop
		plan := "free"
		if err := h.db.WithContext(r.Context()).Where("id = ?", shopID).First(&shop).Error; err == nil {
			plan = shop.Plan
		}
		allowlist, ok := service.PlanModelAllowlist(plan)
		if ok && !allowlist.Allows(req.Model) {
			httputil.WriteError(w, http.StatusForbidden, "model "+req.Model+" not available on "+plan+" plan")
			return
		}
	}

	// Upsert config
	var cfg model.ShopLLMConfig
	err = h.db.WithContext(r.Context()).
		Where("shop_id = ?", shopID).
		First(&cfg).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		cfg = model.ShopLLMConfig{ShopID: shopID}
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		httputil.WriteError(w, http.StatusInternalServerError, "db error: "+err.Error())
		return
	}

	if req.Provider != "" {
		cfg.Provider = req.Provider
	}
	if req.Model != "" {
		cfg.Model = req.Model
	}
	cfg.APIKey = req.APIKey // empty = remove BYOK
	if req.Temperature > 0 {
		cfg.Temperature = req.Temperature
	}
	if req.MaxTokens > 0 {
		cfg.MaxTokens = req.MaxTokens
	}
	cfg.IsActive = true

	if err := h.db.WithContext(r.Context()).Save(&cfg).Error; err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
		return
	}

	// Invalidate cache
	h.llmRouter.InvalidateCache(r.Context(), shopID)

	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"message": "LLM config saved",
	})
}

// ListModels handles GET /api/llm/models?provider=xxx&shop_id=xxx
func (h *LLMConfigHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		provider = "dashscope"
	}

	shopID, _ := parseShopUUID(r)
	plan := "free"
	if h.db != nil && shopID != uuid.Nil {
		var shop model.Shop
		if err := h.db.WithContext(r.Context()).Where("id = ?", shopID).First(&shop).Error; err == nil {
			plan = shop.Plan
		}
	}

	models := h.llmRouter.ListModels(provider, plan)
	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"models":  models,
		"plan":    plan,
	})
}

// parseShopUUID extracts shop_id from query param and returns a UUID.
func parseShopUUID(r *http.Request) (uuid.UUID, error) {
	sid := r.URL.Query().Get("shop_id")
	if sid == "" {
		return uuid.Nil, http.ErrNoLocation // sentinel
	}
	return uuid.Parse(sid)
}

// maskKey returns a masked version of an API key for display.
func maskKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "••••••••"
	}
	return key[:4] + "••••••••" + key[len(key)-4:]
}
