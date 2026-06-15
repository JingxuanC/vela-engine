package handler

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/middleware"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// FulfillmentHandler serves fulfillment tracking endpoints.
type FulfillmentHandler struct {
	db      *gorm.DB
	tracker *service.FulfillmentTracker
}

// NewFulfillmentHandler creates a new FulfillmentHandler.
func NewFulfillmentHandler(db *gorm.DB, tracker *service.FulfillmentTracker) *FulfillmentHandler {
	return &FulfillmentHandler{db: db, tracker: tracker}
}

// getShopIDFromRequest extracts the shop_id from the request context (set by Gateway middleware).
func getShopIDFromRequest(r *http.Request) (uuid.UUID, error) {
	shopIDStr := middleware.ShopIDFromContext(r.Context())
	if shopIDStr == "" {
		shopIDStr = r.URL.Query().Get("shop_id")
	}
	if shopIDStr == "" {
		return uuid.Nil, fmt.Errorf("shop_id is required")
	}
	return uuid.Parse(shopIDStr)
}

// Sync handles POST /api/fulfillment/sync/{order_id}
func (h *FulfillmentHandler) Sync(w http.ResponseWriter, r *http.Request) {
	if h.db == nil || h.tracker == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Service unavailable")
		return
	}

	shopID, err := getShopIDFromRequest(r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	platformID := chi.URLParam(r, "order_id")
	if platformID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "order_id is required")
		return
	}

	orderGID := fmt.Sprintf("gid://shopify/Order/%s", platformID)

	count, err := h.tracker.SyncOrderFulfillments(r.Context(), shopID, orderGID, platformID)
	if err != nil {
		slog.Error("fulfillment: sync failed", "shop_id", shopID, "platform_id", platformID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, fmt.Sprintf("Sync failed: %v", err))
		return
	}

	events, _ := h.tracker.GetFulfillmentEvents(r.Context(), shopID, platformID)

	httputil.WriteOK(w, map[string]interface{}{
		"success":      true,
		"new_events":   count,
		"total_events": len(events),
		"events":       eventsToJSON(events),
	})
}

// Poll handles POST /api/fulfillment/poll
func (h *FulfillmentHandler) Poll(w http.ResponseWriter, r *http.Request) {
	if h.db == nil || h.tracker == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Service unavailable")
		return
	}

	shopID, err := getShopIDFromRequest(r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	ordersChecked, eventsTotal, err := h.tracker.PollUnfulfilled(r.Context(), shopID)
	if err != nil {
		slog.Error("fulfillment: poll failed", "shop_id", shopID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, fmt.Sprintf("Poll failed: %v", err))
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success":        true,
		"orders_checked": ordersChecked,
		"new_events":     eventsTotal,
	})
}

// GetEvents handles GET /api/fulfillment/events/{order_id}
func (h *FulfillmentHandler) GetEvents(w http.ResponseWriter, r *http.Request) {
	if h.db == nil || h.tracker == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Service unavailable")
		return
	}

	shopID, err := getShopIDFromRequest(r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	platformID := chi.URLParam(r, "order_id")
	if platformID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "order_id is required")
		return
	}

	events, err := h.tracker.GetFulfillmentEvents(r.Context(), shopID, platformID)
	if err != nil {
		slog.Error("fulfillment: get events failed", "shop_id", shopID, "platform_id", platformID, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, fmt.Sprintf("Get events failed: %v", err))
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"events":  eventsToJSON(events),
	})
}

// GetTrackingSummary returns a human-readable tracking summary for the given order.
// Used by the AI shopping assistant to answer "where's my order?" questions.
func (h *FulfillmentHandler) GetTrackingSummary(r *http.Request, orderPlatformID string) (string, []model.FulfillmentEvent, error) {
	shopID, err := getShopIDFromRequest(r)
	if err != nil {
		return "", nil, err
	}

	events, err := h.tracker.GetFulfillmentEvents(r.Context(), shopID, orderPlatformID)
	if err != nil || len(events) == 0 {
		// Try synced_orders for tracking info even without events
		var order model.SyncedOrder
		if h.db != nil {
			h.db.Where("shop_id = ? AND platform_id = ?", shopID, orderPlatformID).First(&order)
		}
		if order.TrackingNumber != "" {
			summary := fmt.Sprintf("Tracking: %s via %s", order.TrackingNumber, order.Carrier)
			if order.TrackingURL != "" {
				summary += fmt.Sprintf(" (%s)", order.TrackingURL)
			}
			if order.LatestEventStatus != "" {
				summary += fmt.Sprintf(" | Status: %s", order.LatestEventStatus)
			}
			return summary, nil, nil
		}
		return "", nil, err
	}

	summary := ""
	latest := events[len(events)-1]
	switch latest.Status {
	case "LABEL_PRINTED":
		summary = "Label printed — awaiting carrier pickup"
	case "IN_TRANSIT":
		summary = fmt.Sprintf("In transit — currently in %s %s", latest.City, latest.Province)
	case "OUT_FOR_DELIVERY":
		summary = "Out for delivery today"
	case "DELIVERED":
		summary = "Delivered — package has been delivered"
	case "ATTEMPTED_DELIVERY":
		summary = "Delivery attempted — carrier was unable to deliver"
	case "FAILURE":
		summary = "Delivery issue — carrier reported a problem"
	default:
		summary = fmt.Sprintf("Status: %s — %s", latest.Status, latest.Message)
	}

	if latest.EstimatedDeliveryAt != nil {
		summary += fmt.Sprintf(" (ETA: %s)", latest.EstimatedDeliveryAt.Format("Jan 2"))
	}

	return summary, events, nil
}

// eventsToJSON converts FulfillmentEvent slice to JSON-friendly maps.
func eventsToJSON(events []model.FulfillmentEvent) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(events))
	for _, e := range events {
		m := map[string]interface{}{
			"status":    e.Status,
			"message":   e.Message,
			"city":      e.City,
			"province":  e.Province,
			"country":   e.Country,
			"happened_at": e.HappenedAt.Format("2006-01-02T15:04:05Z"),
		}
		if e.EstimatedDeliveryAt != nil {
			m["estimated_delivery_at"] = e.EstimatedDeliveryAt.Format("2006-01-02T15:04:05Z")
		}
		result = append(result, m)
	}
	if result == nil {
		result = []map[string]interface{}{}
	}
	return result
}
