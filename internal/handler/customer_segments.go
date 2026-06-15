package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/middleware"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// CustomerSegmentsHandler handles RFM customer segmentation endpoints.
type CustomerSegmentsHandler struct {
	rfm *service.RFMEngine
}

// NewCustomerSegmentsHandler creates a new CustomerSegmentsHandler.
func NewCustomerSegmentsHandler(rfm *service.RFMEngine) *CustomerSegmentsHandler {
	return &CustomerSegmentsHandler{rfm: rfm}
}

// GetSegments handles GET /api/customers/segments
// Returns the 5 segment breakdown with counts and percentages.
func (h *CustomerSegmentsHandler) GetSegments(w http.ResponseWriter, r *http.Request) {
	shopIDStr := middleware.ShopIDFromContext(r.Context())
	if shopIDStr == "" {
		httputil.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	shopID, err := uuid.Parse(shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid shop_id")
		return
	}

	segments, err := h.rfm.ComputeSegments(r.Context(), shopID)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to compute segments: "+err.Error())
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success":  true,
		"segments": segments,
	})
}

// GetSegmentCustomers handles GET /api/customers/segments/{segment}
// Returns the list of customers in a given RFM segment.
func (h *CustomerSegmentsHandler) GetSegmentCustomers(w http.ResponseWriter, r *http.Request) {
	shopIDStr := middleware.ShopIDFromContext(r.Context())
	if shopIDStr == "" {
		httputil.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	shopID, err := uuid.Parse(shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid shop_id")
		return
	}

	segment := r.PathValue("segment")
	if segment == "" {
		httputil.WriteError(w, http.StatusBadRequest, "segment is required")
		return
	}

	customers, err := h.rfm.GetSegmentCustomers(r.Context(), shopID, segment)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to get customers: "+err.Error())
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success":   true,
		"segment":   segment,
		"customers": customers,
	})
}
