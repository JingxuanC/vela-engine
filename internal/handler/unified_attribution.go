package handler

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"gorm.io/gorm"
)

// UnifiedAttributionHandler handles unified cross-channel attribution API endpoints.
type UnifiedAttributionHandler struct {
	db      *gorm.DB
	service *service.UnifiedAttributionService
}

// NewUnifiedAttributionHandler creates a new UnifiedAttributionHandler.
func NewUnifiedAttributionHandler(db *gorm.DB, svc *service.UnifiedAttributionService) *UnifiedAttributionHandler {
	return &UnifiedAttributionHandler{db: db, service: svc}
}

// GetUnified handles GET /api/analytics/attribution/unified?days=30
func (h *UnifiedAttributionHandler) GetUnified(w http.ResponseWriter, r *http.Request) {
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

	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 && parsed <= 365 {
			days = parsed
		}
	}

	if h.service == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Unified attribution service not available")
		return
	}

	result, err := h.service.GetUnifiedAttribution(r.Context(), shopID, days)
	if err != nil {
		slog.Error("unified_attribution: query failed", "shop_id", shopID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to compute unified attribution")
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success":                 true,
		"total_actual_revenue":    result.TotalActualRevenue,
		"total_attributed_revenue": result.TotalAttributedRevenue,
		"overlap_rate":            result.OverlapRate,
		"channels":                result.Channels,
		"overlap_matrix":          result.OverlapMatrix,
	})
}
