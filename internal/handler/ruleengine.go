package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	chi "github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"gorm.io/gorm"
)

type RuleEngineHandler struct{ db *gorm.DB }

func NewRuleEngineHandler(db *gorm.DB) *RuleEngineHandler { return &RuleEngineHandler{db: db} }
func (h *RuleEngineHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteOK(w, map[string]interface{}{"success": true, "rules": []interface{}{}})
		return
	}
	sid := shopIDFromRequest(r)
	if sid == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id required")
		return
	}
	shopID, _ := uuid.Parse(sid)
	var rules []model.AutomationRule
	h.db.Where("shop_id=?", shopID).Order("priority DESC").Find(&rules)
	items := make([]map[string]interface{}, len(rules))
	for i, r := range rules {
		items[i] = map[string]interface{}{"id": r.ID, "name": r.Name, "trigger": r.TriggerType, "enabled": r.Enabled, "priority": r.Priority, "created": r.CreatedAt}
	}
	httputil.WriteOK(w, map[string]interface{}{"success": true, "rules": items})
}
func (h *RuleEngineHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShopID      string `json:"shop_id"`
		Name        string `json:"name"`
		TriggerType string `json:"trigger_type"`
		Description string `json:"description"`
		Priority    int    `json:"priority"`
		Enabled     bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	shopID, err := database.ResolveShopID(h.db, req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid shop_id: "+err.Error())
		return
	}
	tc, _ := json.Marshal(map[string]interface{}{})
	cc, _ := json.Marshal(map[string]interface{}{})
	ac, _ := json.Marshal([]interface{}{})
	rule := model.AutomationRule{ShopID: shopID, Name: req.Name, TriggerType: req.TriggerType, Description: req.Description, Priority: req.Priority, Enabled: req.Enabled, TriggerConfig: tc, Conditions: cc, Actions: ac}
	if err := h.db.Create(&rule).Error; err != nil {
		slog.Error("ruleengine: create rule failed", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to create rule")
		return
	}
	httputil.WriteJSON(w, 201, rule)
}
func (h *RuleEngineHandler) Update(w http.ResponseWriter, r *http.Request) {
	sid := shopIDFromRequest(r)
	if sid == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id required")
		return
	}
	shopID, _ := uuid.Parse(sid)
	rid := chi.URLParam(r, "id")
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		TriggerType string `json:"trigger_type"`
		Priority    *int   `json:"priority"`
		Enabled     *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	updates := map[string]interface{}{"updated_at": time.Now()}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.Description != "" {
		updates["description"] = req.Description
	}
	if req.TriggerType != "" {
		updates["trigger_type"] = req.TriggerType
	}
	if req.Priority != nil {
		updates["priority"] = *req.Priority
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	h.db.Model(&model.AutomationRule{}).Where("shop_id=? AND id=?", shopID, rid).Updates(updates)
	httputil.WriteOK(w, map[string]string{"success": "true"})
}
func (h *RuleEngineHandler) Delete(w http.ResponseWriter, r *http.Request) {
	sid := shopIDFromRequest(r)
	if sid == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id required")
		return
	}
	shopID, _ := uuid.Parse(sid)
	rid := chi.URLParam(r, "id")
	h.db.Where("shop_id=? AND id=?", shopID, rid).Delete(&model.AutomationRule{})
	httputil.WriteOK(w, map[string]string{"success": "true"})
}
