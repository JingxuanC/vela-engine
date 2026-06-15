package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// CartRecoveryHandler handles abandoned cart detection and recovery campaigns.
type CartRecoveryHandler struct {
	db *gorm.DB
}

// NewCartRecoveryHandler creates a new CartRecoveryHandler with DB integration.
func NewCartRecoveryHandler(db *gorm.DB) *CartRecoveryHandler {
	return &CartRecoveryHandler{db: db}
}

// CartRecoveryCampaign is the API request/response type.
type CartRecoveryCampaign struct {
	ID              string  `json:"id"`
	ShopID          string  `json:"shop_id,omitempty"`
	Name            string  `json:"name"`
	IsActive        bool    `json:"is_active"`
	DelayMinutes    int     `json:"delay_minutes"`
	EmailSubject    string  `json:"email_subject"`
	EmailBody       string  `json:"email_body"`
	DiscountPercent float64 `json:"discount_percent"`
	CodePrefix      string  `json:"code_prefix,omitempty"`
}

// List handles GET /api/cart-recovery/campaigns.
// Requires shop_id (via context, X-Shop-ID header, or query param) for isolation.
func (h *CartRecoveryHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	shopID, err := h.resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusForbidden, "shop_id is required to list campaigns: "+err.Error())
		return
	}

	var campaigns []model.CartRecoveryCampaign
	if err := h.db.WithContext(r.Context()).
		Where("shop_id = ?", shopID).
		Order("created_at DESC").
		Find(&campaigns).Error; err != nil {
		slog.Error("failed to fetch campaigns", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to fetch campaigns")
		return
	}

	resp := make([]CartRecoveryCampaign, 0, len(campaigns))
	for _, c := range campaigns {
		resp = append(resp, CartRecoveryCampaign{
			ID:              c.ID.String(),
			ShopID:          c.ShopID.String(),
			Name:            c.Name,
			IsActive:        c.IsActive,
			DelayMinutes:    c.DelayMinutes,
			EmailSubject:    c.EmailSubject,
			EmailBody:       c.EmailBody,
			DiscountPercent: c.DiscountPercent,
			CodePrefix:      c.CodePrefix,
		})
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success":   true,
		"campaigns": resp,
	})
}

// Create handles POST /api/cart-recovery/campaigns.
func (h *CartRecoveryHandler) Create(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	var req CartRecoveryCampaign
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if req.Name == "" {
		httputil.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.EmailSubject == "" {
		httputil.WriteError(w, http.StatusBadRequest, "email_subject is required")
		return
	}

	shopID := uuid.Nil
	if req.ShopID != "" {
		parsed, err := database.ResolveShopID(h.db, req.ShopID)
		if err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id format: "+err.Error())
			return
		}
		shopID = parsed
	}
	if shopID == uuid.Nil {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	campaign := model.CartRecoveryCampaign{
		ShopID:          shopID,
		Name:            req.Name,
		IsActive:        req.IsActive,
		DelayMinutes:    req.DelayMinutes,
		EmailSubject:    req.EmailSubject,
		EmailBody:       req.EmailBody,
		DiscountPercent: req.DiscountPercent,
		CodePrefix:      generateCodePrefix(),
		ScheduleEnabled: true,
	}

	if err := h.db.WithContext(r.Context()).Create(&campaign).Error; err != nil {
		slog.Error("failed to create campaign", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to create campaign")
		return
	}

	req.ID = campaign.ID.String()
	req.CodePrefix = campaign.CodePrefix
	httputil.WriteJSON(w, http.StatusCreated, map[string]interface{}{
		"success":  true,
		"campaign": req,
	})
}

// Get handles GET /api/cart-recovery/campaigns/{id}
func (h *CartRecoveryHandler) Get(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	id := chi.URLParam(r, "id")
	campaignID, err := uuid.Parse(id)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid campaign id")
		return
	}

	shopID, err := h.resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	var campaign model.CartRecoveryCampaign
	if err := h.db.WithContext(r.Context()).First(&campaign, "id = ? AND shop_id = ?", campaignID, shopID).Error; err != nil {
		httputil.WriteError(w, http.StatusNotFound, "Campaign not found")
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success":  true,
		"campaign": CartRecoveryCampaign{
			ID:              campaign.ID.String(),
			ShopID:          campaign.ShopID.String(),
			Name:            campaign.Name,
			IsActive:        campaign.IsActive,
			DelayMinutes:    campaign.DelayMinutes,
			EmailSubject:    campaign.EmailSubject,
			EmailBody:       campaign.EmailBody,
			DiscountPercent: campaign.DiscountPercent,
			CodePrefix:      campaign.CodePrefix,
		},
	})
}

// Update handles PUT /api/cart-recovery/campaigns/{id}
func (h *CartRecoveryHandler) Update(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	id := chi.URLParam(r, "id")
	campaignID, err := uuid.Parse(id)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid campaign id")
		return
	}

	var req CartRecoveryCampaign
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	shopID, err := h.resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	updates := map[string]interface{}{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.EmailSubject != "" {
		updates["email_subject"] = req.EmailSubject
	}
	if req.EmailBody != "" {
		updates["email_body"] = req.EmailBody
	}
	updates["is_active"] = req.IsActive
	updates["delay_minutes"] = req.DelayMinutes
	updates["discount_percent"] = req.DiscountPercent

	result := h.db.WithContext(r.Context()).Model(&model.CartRecoveryCampaign{}).
		Where("id = ? AND shop_id = ?", campaignID, shopID).Updates(updates)
	if result.Error != nil {
		slog.Error("failed to update campaign", "error", result.Error)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to update campaign")
		return
	}
	if result.RowsAffected == 0 {
		httputil.WriteError(w, http.StatusNotFound, "Campaign not found")
		return
	}

	httputil.WriteOK(w, map[string]interface{}{"success": true})
}

// Delete handles DELETE /api/cart-recovery/campaigns/{id}
func (h *CartRecoveryHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	id := chi.URLParam(r, "id")
	campaignID, err := uuid.Parse(id)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid campaign id")
		return
	}

	shopID, err := h.resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	result := h.db.WithContext(r.Context()).
		Where("id = ? AND shop_id = ?", campaignID, shopID).
		Delete(&model.CartRecoveryCampaign{})
	if result.Error != nil {
		slog.Error("failed to delete campaign", "error", result.Error)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to delete campaign")
		return
	}
	if result.RowsAffected == 0 {
		httputil.WriteError(w, http.StatusNotFound, "Campaign not found")
		return
	}

	httputil.WriteOK(w, map[string]interface{}{"success": true})
}

// resolveShopID extracts and validates the ShopID from the request.
// Priority: context (set by gateway middleware) > X-Shop-ID header > shop_id query param.
func (h *CartRecoveryHandler) resolveShopID(r *http.Request) (uuid.UUID, error) {
	// 1. Try context (set by gateway middleware)
	if ctxShopID := r.Context().Value(ctxKeyShopID); ctxShopID != nil {
		if s, ok := ctxShopID.(string); ok && s != "" {
			if id, err := uuid.Parse(s); err == nil {
				return id, nil
			}
		}
	}

	// 2. Try X-Shop-ID header
	if shopFromHeader := r.Header.Get("X-Shop-ID"); shopFromHeader != "" {
		if id, err := uuid.Parse(shopFromHeader); err == nil {
			return id, nil
		}
	}

	// 3. Try shop_id query param
	if shopFromQuery := r.URL.Query().Get("shop_id"); shopFromQuery != "" {
		if id, err := uuid.Parse(shopFromQuery); err == nil {
			return id, nil
		}
	}

	return uuid.Nil, fmt.Errorf("shop_id is required (set X-Shop-ID header or shop_id query param)")
}

// generateCodePrefix creates a unique discount code prefix like "RECOVER-A1B2".
func generateCodePrefix() string {
	b := make([]byte, 2)
	rand.Read(b)
	return "RECOVER-" + strings.ToUpper(hex.EncodeToString(b))
}
