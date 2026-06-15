package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// ReviewAutoReplySettingHandler handles GET/PUT /api/review/settings.
type ReviewAutoReplySettingHandler struct {
	db    *gorm.DB
	cache *service.CacheService
}

// NewReviewAutoReplySettingHandler creates a new ReviewAutoReplySettingHandler.
func NewReviewAutoReplySettingHandler(db *gorm.DB, cache *service.CacheService) *ReviewAutoReplySettingHandler {
	return &ReviewAutoReplySettingHandler{db: db, cache: cache}
}

// --- DTOs ---

// AutoReplySettingResponse is the response body for GET /api/review/settings.
type AutoReplySettingResponse struct {
	Success           bool    `json:"success"`
	Mode              string  `json:"mode"`               // "auto" | "manual"
	AutoSendThreshold float64 `json:"auto_send_threshold"` // rating ≤ this = auto-send (legacy)
	RequireApproval   bool    `json:"require_approval"`    // legacy — mapped to Mode
	ReplyLanguage     string  `json:"reply_language"`
	DefaultReplyStyle string  `json:"default_reply_style"`
}

// UpdateAutoReplySettingRequest is the request body for PUT /api/review/settings.
type UpdateAutoReplySettingRequest struct {
	ShopID            string  `json:"shop_id"`
	Mode              string  `json:"mode"`               // "auto" | "manual"
	AutoSendThreshold float64 `json:"auto_send_threshold"`
	RequireApproval   bool    `json:"require_approval"`   // legacy
	ReplyLanguage     string  `json:"reply_language"`
	DefaultReplyStyle string  `json:"default_reply_style"`
}

// --- Defaults ---

func defaultSettingResponse() AutoReplySettingResponse {
	return AutoReplySettingResponse{
		Success:           true,
		Mode:              "manual",
		AutoSendThreshold: 3,
		RequireApproval:   true,
		ReplyLanguage:     "auto",
		DefaultReplyStyle: "professional",
	}
}

// --- Handlers ---

// Get handles GET /api/review/settings?shop_id=xxx
func (h *ReviewAutoReplySettingHandler) Get(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	shopIDStr := r.URL.Query().Get("shop_id")
	if shopIDStr == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	shopID, err := database.ResolveShopID(h.db, shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	var setting model.ReviewAutoReplySetting
	result := h.db.WithContext(r.Context()).Where("shop_id = ?", shopID).First(&setting)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			// Return defaults when no setting exists
			slog.Info("review_settings: no setting found, returning defaults", "shop_id", shopIDStr)
			httputil.WriteOK(w, defaultSettingResponse())
			return
		}
		slog.Error("review_settings: failed to get setting", "shop_id", shopIDStr, "error", result.Error)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to get settings")
		return
	}

	httputil.WriteOK(w, settingToResponse(&setting))
}

// Update handles PUT /api/review/settings
func (h *ReviewAutoReplySettingHandler) Update(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	var req UpdateAutoReplySettingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.ShopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	shopID, err := database.ResolveShopID(h.db, req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	// Upsert: find or create by shop_id
	var setting model.ReviewAutoReplySetting
	result := h.db.WithContext(r.Context()).Where("shop_id = ?", shopID).First(&setting)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			setting = model.ReviewAutoReplySetting{
				ShopID: shopID,
			}
		} else {
			slog.Error("review_settings: failed to find setting", "shop_id", req.ShopID, "error", result.Error)
			httputil.WriteError(w, http.StatusInternalServerError, "Failed to find settings")
			return
		}
	}

	// Assign updates (zero values are fine — user explicitly set them)
	setting.AutoSendThreshold = req.AutoSendThreshold
	setting.RequireApproval = req.RequireApproval
	if req.ReplyLanguage != "" {
		setting.ReplyLanguage = req.ReplyLanguage
	}
	if req.DefaultReplyStyle != "" {
		setting.DefaultReplyStyle = req.DefaultReplyStyle
	}

	if err := h.db.WithContext(r.Context()).Save(&setting).Error; err != nil {
		slog.Error("review_settings: failed to save setting", "shop_id", req.ShopID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to save settings")
		return
	}

	slog.Info("review_settings: setting updated", "shop_id", req.ShopID)
	httputil.WriteOK(w, settingToResponse(&setting))
}

// settingToResponse converts a model.ReviewAutoReplySetting to response DTO.
func settingToResponse(s *model.ReviewAutoReplySetting) AutoReplySettingResponse {
	mode := "manual"
	if !s.RequireApproval {
		mode = "auto"
	}
	return AutoReplySettingResponse{
		Success:           true,
		Mode:              mode,
		AutoSendThreshold: s.AutoSendThreshold,
		RequireApproval:   s.RequireApproval,
		ReplyLanguage:     s.ReplyLanguage,
		DefaultReplyStyle: s.DefaultReplyStyle,
	}
}
