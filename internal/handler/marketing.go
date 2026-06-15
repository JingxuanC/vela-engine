package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/middleware"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// MarketingHandler handles marketing automation flow API endpoints.
type MarketingHandler struct {
	db     *gorm.DB
	engine *service.MarketingFlowEngine
}

func NewMarketingHandler(db *gorm.DB, engine *service.MarketingFlowEngine) *MarketingHandler {
	return &MarketingHandler{db: db, engine: engine}
}

type marketingFlowReq struct {
	Name        string                   `json:"name"`
	TriggerType string                   `json:"trigger_type"`
	Conditions  []map[string]interface{} `json:"conditions,omitempty"`
	Actions     []map[string]interface{} `json:"actions,omitempty"`
	Enabled     *bool                    `json:"enabled,omitempty"`
}

func marketingResolveShopID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	shopIDStr := middleware.ShopIDFromContext(r.Context())
	if shopIDStr == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid shop_id")
		return uuid.Nil, false
	}
	return id, true
}

// ListFlows handles GET /api/marketing/flows
func (h *MarketingHandler) ListFlows(w http.ResponseWriter, r *http.Request) {
	shopID, ok := marketingResolveShopID(w, r)
	if !ok {
		return
	}

	var flows []model.MarketingFlow
	if err := h.db.WithContext(r.Context()).
		Where("shop_id = ?", shopID).
		Order("created_at ASC").
		Find(&flows).Error; err != nil {
		slog.Error("marketing: failed to list flows", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to list flows")
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"flows":   flows,
	})
}

// CreateFlow handles POST /api/marketing/flows
func (h *MarketingHandler) CreateFlow(w http.ResponseWriter, r *http.Request) {
	shopID, ok := marketingResolveShopID(w, r)
	if !ok {
		return
	}

	var req marketingFlowReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	flow := model.MarketingFlow{
		ShopID:      shopID,
		Name:        req.Name,
		TriggerType: req.TriggerType,
		Enabled:     true,
	}

	if len(req.Conditions) > 0 {
		b, _ := json.Marshal(req.Conditions)
		flow.Conditions = datatypes.JSON(b)
	}
	if len(req.Actions) > 0 {
		b, _ := json.Marshal(req.Actions)
		flow.Actions = datatypes.JSON(b)
	}
	if req.Enabled != nil {
		flow.Enabled = *req.Enabled
	}

	if err := h.db.WithContext(r.Context()).Create(&flow).Error; err != nil {
		slog.Error("marketing: failed to create flow", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to create flow")
		return
	}

	httputil.WriteCreated(w, map[string]interface{}{
		"success": true,
		"flow":    flow,
	})
}

// UpdateFlow handles PUT /api/marketing/flows/{id}
func (h *MarketingHandler) UpdateFlow(w http.ResponseWriter, r *http.Request) {
	shopID, ok := marketingResolveShopID(w, r)
	if !ok {
		return
	}

	flowID := chi.URLParam(r, "id")
	if flowID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "flow id is required")
		return
	}

	var flow model.MarketingFlow
	if err := h.db.WithContext(r.Context()).
		Where("id = ? AND shop_id = ?", flowID, shopID).
		First(&flow).Error; err != nil {
		httputil.WriteError(w, http.StatusNotFound, "flow not found")
		return
	}

	var req marketingFlowReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	updates := map[string]interface{}{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if len(req.Actions) > 0 {
		b, _ := json.Marshal(req.Actions)
		updates["actions"] = datatypes.JSON(b)
	}

	if err := h.db.WithContext(r.Context()).Model(&flow).Updates(updates).Error; err != nil {
		slog.Error("marketing: failed to update flow", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to update flow")
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"flow":    flow,
	})
}

// GetFlowRuns handles GET /api/marketing/flows/{id}/runs
func (h *MarketingHandler) GetFlowRuns(w http.ResponseWriter, r *http.Request) {
	shopID, ok := marketingResolveShopID(w, r)
	if !ok {
		return
	}

	flowID := chi.URLParam(r, "id")
	if flowID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "flow id is required")
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	var runs []model.MarketingFlowRun
	if err := h.db.WithContext(r.Context()).
		Where("flow_id = ? AND shop_id = ?", flowID, shopID).
		Order("executed_at DESC").
		Limit(limit).
		Find(&runs).Error; err != nil {
		slog.Error("marketing: failed to list flow runs", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to list flow runs")
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"runs":    runs,
	})
}
