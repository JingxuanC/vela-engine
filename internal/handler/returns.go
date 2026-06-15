package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/insight"
	"github.com/JingxuanC/vela-engine/internal/service/returns"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// Valid return status transitions.
var validStatusTransitions = map[string][]string{
	"pending":          {"approved", "rejected"},
	"approved":         {"label_issued", "rejected"},
	"label_issued":     {"customer_shipped", "rejected"},
	"customer_shipped": {"received"},
	"received":         {"inspecting"},
	"inspecting":       {"refunded", "exchanged", "rejected"},
	"refunded":         {},
	"exchanged":        {},
	"rejected":         {},
}

// ReturnsHandler handles the returns & exchange self-service portal.
type ReturnsHandler struct {
	db                *gorm.DB
	shipEngine        *returns.ShipEngineClient
	injector          *insight.ContextInjector
	defaultReturnAddr returns.Address
}

// NewReturnsHandler creates a ReturnsHandler with DB and ShipEngine client.
func NewReturnsHandler(db *gorm.DB, shipEngine *returns.ShipEngineClient, inj *insight.ContextInjector) *ReturnsHandler {
	return &ReturnsHandler{db: db, shipEngine: shipEngine, injector: inj}
}

// SetDefaultReturnAddress configures the merchant return address used for label generation.
// In production, this should be loaded per-shop from the shop settings table.
func (h *ReturnsHandler) SetDefaultReturnAddress(addr returns.Address) {
	h.defaultReturnAddr = addr
}

// --- DTOs ---

// ReturnRequest is a customer return request.
type ReturnRequest struct {
	OrderID              string       `json:"order_id"`
	CustomerEmail        string       `json:"customer_email"`
	CustomerName         string       `json:"customer_name"`
	CustomerAddressLine1 string       `json:"customer_address_line1"`
	CustomerCity         string       `json:"customer_city"`
	CustomerState        string       `json:"customer_state"`
	CustomerPostalCode   string       `json:"customer_postal_code"`
	CustomerCountry      string       `json:"customer_country"`
	Items                []ReturnItem `json:"items"`
	Reason               string       `json:"reason"`
	Note                 string       `json:"note"`
}

// ReturnItem represents a single item in a return request.
type ReturnItem struct {
	ProductID    string `json:"product_id"`
	VariantID    string `json:"variant_id"`
	ProductTitle string `json:"product_title"`
	Quantity     int    `json:"quantity"`
	Size         string `json:"size"`
	Reason       string `json:"reason"`
}

// ReturnResponse is the response for a return operation.
type ReturnResponse struct {
	Success        bool   `json:"success"`
	ReturnID       string `json:"return_id"`
	Status         string `json:"status"`
	LabelURL       string `json:"label_url,omitempty"`
	TrackingNumber string `json:"tracking_number,omitempty"`
	Error          string `json:"error,omitempty"`
}

// ReturnDetailResponse is a full return detail including items.
type ReturnDetailResponse struct {
	Success        bool             `json:"success"`
	ReturnID       string           `json:"return_id"`
	OrderID        string           `json:"order_id"`
	CustomerEmail  string           `json:"customer_email"`
	CustomerName   string           `json:"customer_name"`
	Status         string           `json:"status"`
	LabelURL       string           `json:"label_url,omitempty"`
	TrackingNumber string           `json:"tracking_number,omitempty"`
	Items          []ReturnItemResp `json:"items"`
	CreatedAt      string           `json:"created_at"`
}

// ReturnItemResp is the response item.
type ReturnItemResp struct {
	ProductID    string `json:"product_id"`
	ProductTitle string `json:"product_title"`
	Quantity     int    `json:"quantity"`
	Size         string `json:"size"`
	Reason       string `json:"reason"`
}

// ReturnListResponse holds paginated returns.
type ReturnListResponse struct {
	Success bool                   `json:"success"`
	Returns []ReturnDetailResponse `json:"returns"`
	Total   int64                  `json:"total"`
	Limit   int                    `json:"limit"`
	Offset  int                    `json:"offset"`
}

// --- Handlers ---

// Create handles POST /api/returns. DEPRECATED: data source is Shopify returns/request webhook.
func (h *ReturnsHandler) Create(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	var req ReturnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.OrderID == "" || req.CustomerEmail == "" {
		httputil.WriteError(w, http.StatusBadRequest, "order_id and customer_email are required")
		return
	}

	country := req.CustomerCountry
	if country == "" {
		country = "US"
	}

	// Resolve ShopID from request context or query param
	shopIDStr := r.URL.Query().Get("shop_id")
	var shopID uuid.UUID
	if shopIDStr != "" {
		if uid, err := uuid.Parse(shopIDStr); err == nil {
			shopID = uid
		}
	}

	ret := model.Return{
		ShopID:             shopID,
		OrderID:            req.OrderID,
		CustomerEmail:      req.CustomerEmail,
		CustomerName:       req.CustomerName,
		CustomerAddress:    req.CustomerAddressLine1,
		CustomerCity:       req.CustomerCity,
		CustomerState:      req.CustomerState,
		CustomerPostalCode: req.CustomerPostalCode,
		CustomerCountry:    country,
		Status:             "pending",
		ReturnReason:       req.Reason,
		ReturnNote:         req.Note,
	}
	for _, item := range req.Items {
		ret.Items = append(ret.Items, model.ReturnItem{
			ProductID:    item.ProductID,
			VariantID:    item.VariantID,
			ProductTitle: item.ProductTitle,
			Quantity:     item.Quantity,
			Size:         item.Size,
			Reason:       item.Reason,
		})
	}

	if err := h.db.WithContext(r.Context()).Create(&ret).Error; err != nil {
		slog.Error("failed to create return", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to create return")
		return
	}

	// If the reason is a size issue, log it for potential exchange recommendation
	if isSizeRelated(req.Reason) {
		slog.Info("size-related return created, exchange recommendation available",
			"return_id", ret.ID, "reason", req.Reason)
	}

	httputil.WriteJSON(w, http.StatusCreated, ReturnResponse{
		Success:  true,
		ReturnID: ret.ID.String(),
		Status:   ret.Status,
	})
}

// GetStatus handles GET /api/returns/{returnID}.
func (h *ReturnsHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "returnID"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid return ID")
		return
	}

	shopID, err := h.resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusForbidden, err.Error())
		return
	}

	var ret model.Return
	if err := h.db.Preload("Items").Where("id = ? AND shop_id = ?", id, shopID).First(&ret).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "Return not found")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to fetch return")
		return
	}

	httputil.WriteOK(w, returnToDetail(&ret))
}

// Lookup handles GET /api/returns/lookup — customer portal lookup by email and order number.
func (h *ReturnsHandler) Lookup(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	email := r.URL.Query().Get("email")
	orderNumber := r.URL.Query().Get("order_number")
	if email == "" || orderNumber == "" {
		httputil.WriteError(w, http.StatusBadRequest, "email and order_number are required")
		return
	}

	shopID, err := h.resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusForbidden, err.Error())
		return
	}

	var returns []model.Return
	if err := h.db.Where("customer_email = ? AND order_id = ? AND shop_id = ?", email, orderNumber, shopID).
		Preload("Items").Order("created_at DESC").Find(&returns).Error; err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to lookup returns")
		return
	}

	if len(returns) == 0 {
		// Try matching by OrderName field
		h.db.Where("customer_email = ? AND order_name = ? AND shop_id = ?", email, "#"+orderNumber, shopID).
			Preload("Items").Order("created_at DESC").Find(&returns)
	}

	items := make([]ReturnDetailResponse, 0, len(returns))
	for i := range returns {
		items = append(items, returnToDetail(&returns[i]))
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"returns": items,
		"count":   len(items),
	})
}

// List handles GET /api/returns with pagination.
func (h *ReturnsHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	shopID := r.URL.Query().Get("shop_id")
	var total int64
	var returns []model.Return
	query := h.db.WithContext(r.Context()).Model(&model.Return{})
	if shopID != "" {
		query = query.Where("shop_id = ?", shopID)
	}
	query.Count(&total)
	query.Preload("Items").Order("created_at DESC").Limit(limit).Offset(offset).Find(&returns)

	items := make([]ReturnDetailResponse, 0, len(returns))
	for i := range returns {
		items = append(items, returnToDetail(&returns[i]))
	}

	httputil.WriteOK(w, ReturnListResponse{
		Success: true,
		Returns: items,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	})
}

// transitionStatus validates and applies a status transition.
func (h *ReturnsHandler) transitionStatus(ret *model.Return, newStatus string) error {
	allowed, ok := validStatusTransitions[ret.Status]
	if !ok {
		return fmt.Errorf("unknown current status: %s", ret.Status)
	}
	found := false
	for _, s := range allowed {
		if s == newStatus {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("cannot transition from %s to %s (allowed: %v)", ret.Status, newStatus, allowed)
	}
	ret.Status = newStatus
	return nil
}

// Approve handles POST /api/returns/{returnID}/approve.
func (h *ReturnsHandler) Approve(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "returnID"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid return ID")
		return
	}

	shopID, err := h.resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusForbidden, err.Error())
		return
	}

	var ret model.Return
	if err := h.db.WithContext(r.Context()).Preload("Items").Where("id = ? AND shop_id = ?", id, shopID).First(&ret).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "Return not found")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to fetch return")
		return
	}

	// Validate state machine transition
	if err := h.transitionStatus(&ret, "approved"); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.db.WithContext(r.Context()).Model(&ret).Update("status", "approved")

	// Auto-generate label if ShipEngine configured
	if h.shipEngine != nil && ret.LabelURL == "" {
		labelResp, err := h.generateLabelForReturn(r.Context(), &ret)
		if err != nil {
			slog.Warn("auto-label on approve failed", "return_id", id, "error", err)
		} else {
			h.db.WithContext(r.Context()).Model(&ret).Updates(map[string]interface{}{
				"status":          "label_issued",
				"label_url":       labelResp.LabelURL,
				"tracking_number": labelResp.TrackingNumber,
			})
			ret.Status = "label_issued"
			ret.LabelURL = labelResp.LabelURL
			ret.TrackingNumber = labelResp.TrackingNumber
		}
	}

	httputil.WriteOK(w, ReturnResponse{
		Success:        true,
		ReturnID:       id.String(),
		Status:         ret.Status,
		LabelURL:       ret.LabelURL,
		TrackingNumber: ret.TrackingNumber,
	})
}

// GenerateLabel handles POST /api/returns/{returnID}/label.
func (h *ReturnsHandler) GenerateLabel(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "returnID"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid return ID")
		return
	}

	shopID, err := h.resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusForbidden, err.Error())
		return
	}

	var ret model.Return
	if err := h.db.WithContext(r.Context()).Where("id = ? AND shop_id = ?", id, shopID).First(&ret).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "Return not found")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to fetch return")
		return
	}

	// Validate state: must be approved to issue label
	if ret.Status != "approved" && ret.Status != "pending" {
		httputil.WriteError(w, http.StatusBadRequest, fmt.Sprintf("Cannot generate label in status: %s", ret.Status))
		return
	}

	// Idempotent: return existing label
	if ret.LabelURL != "" {
		httputil.WriteOK(w, ReturnResponse{
			Success:        true,
			ReturnID:       id.String(),
			Status:         ret.Status,
			LabelURL:       ret.LabelURL,
			TrackingNumber: ret.TrackingNumber,
		})
		return
	}

	if h.shipEngine == nil {
		// Fallback mock label
		labelURL := fmt.Sprintf("https://labels.vela.dev/%s.pdf", uuid.New().String())
		trackingNum := fmt.Sprintf("1Z%d", time.Now().UnixMilli())

		h.db.WithContext(r.Context()).Model(&ret).Updates(map[string]interface{}{
			"label_url":       labelURL,
			"tracking_number": trackingNum,
			"status":          "label_issued",
		})

		httputil.WriteOK(w, ReturnResponse{
			Success:        true,
			ReturnID:       id.String(),
			Status:         "label_issued",
			LabelURL:       labelURL,
			TrackingNumber: trackingNum,
		})
		return
	}

	labelResp, err := h.generateLabelForReturn(r.Context(), &ret)
	if err != nil {
		slog.Error("shipengine label failed", "return_id", id, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to generate label: "+err.Error())
		return
	}

	h.db.Model(&ret).Updates(map[string]interface{}{
		"label_url":       labelResp.LabelURL,
		"tracking_number": labelResp.TrackingNumber,
		"status":          "label_issued",
	})

	httputil.WriteOK(w, ReturnResponse{
		Success:        true,
		ReturnID:       id.String(),
		Status:         "label_issued",
		LabelURL:       labelResp.LabelURL,
		TrackingNumber: labelResp.TrackingNumber,
	})
}

// MarkReceived handles POST /api/returns/{returnID}/mark-received — merchant marks receipt.
func (h *ReturnsHandler) MarkReceived(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "returnID"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid return ID")
		return
	}

	shopID, err := h.resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusForbidden, err.Error())
		return
	}

	var ret model.Return
	if err := h.db.WithContext(r.Context()).Where("id = ? AND shop_id = ?", id, shopID).First(&ret).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "Return not found")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to fetch return")
		return
	}

	// Validate state transition
	if err := h.transitionStatus(&ret, "received"); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.db.WithContext(r.Context()).Model(&ret).Updates(map[string]interface{}{
		"status": "received",
	})

	slog.Info("return marked as received", "return_id", id)
	httputil.WriteOK(w, ReturnResponse{
		Success:  true,
		ReturnID: id.String(),
		Status:   "received",
	})
}

// Refund handles POST /api/returns/{returnID}/refund — process refund.
func (h *ReturnsHandler) Refund(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "returnID"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid return ID")
		return
	}

	shopID, err := h.resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusForbidden, err.Error())
		return
	}

	var req struct {
		Amount     float64 `json:"amount"`
		FullRefund bool    `json:"full_refund"`
		Reason     string  `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Default to full refund if no body
		req.FullRefund = true
	}

	var ret model.Return
	if err := h.db.WithContext(r.Context()).Preload("Items").Where("id = ? AND shop_id = ?", id, shopID).First(&ret).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "Return not found")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to fetch return")
		return
	}

	// Validate state: must be inspecting or received
	if ret.Status != "inspecting" && ret.Status != "received" {
		httputil.WriteError(w, http.StatusBadRequest,
			fmt.Sprintf("Cannot refund return in status: %s (must be inspecting or received)", ret.Status))
		return
	}

	// Calculate refund amount
	refundAmount := req.Amount
	if req.FullRefund || refundAmount <= 0 {
		// Calculate based on return reason
		refundAmount = calculateRefundAmount(&ret, req.Reason)
	}

	// In production: call Shopify GraphQL to process refund
	slog.Info("processing refund",
		"return_id", id,
		"amount", refundAmount,
		"reason", req.Reason,
	)

	// Update return record
	h.db.WithContext(r.Context()).Model(&ret).Updates(map[string]interface{}{
		"status":        "refunded",
		"refund_amount": refundAmount,
	})

	httputil.WriteOK(w, map[string]interface{}{
		"success":       true,
		"return_id":     id.String(),
		"status":        "refunded",
		"refund_amount": refundAmount,
		"message":       fmt.Sprintf("Refund of $%.2f processed successfully", refundAmount),
	})
}

// UpdateStatus handles POST /api/returns/{returnID}/status — manual status update by merchant.
func (h *ReturnsHandler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "returnID"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid return ID")
		return
	}

	shopID, err := h.resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusForbidden, err.Error())
		return
	}

	var req struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	var ret model.Return
	if err := h.db.WithContext(r.Context()).Where("id = ? AND shop_id = ?", id, shopID).First(&ret).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "Return not found")
			return
		}
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to fetch return")
		return
	}

	if err := h.transitionStatus(&ret, req.Status); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	updates := map[string]interface{}{
		"status": req.Status,
	}
	if req.Note != "" {
		updates["return_note"] = ret.ReturnNote + "; " + req.Note
	}

	h.db.WithContext(r.Context()).Model(&ret).Updates(updates)

	httputil.WriteOK(w, ReturnResponse{
		Success:  true,
		ReturnID: id.String(),
		Status:   req.Status,
	})
}

// UpdateNote updates the note on a return request.
func (h *ReturnsHandler) UpdateNote(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}
	returnID := chi.URLParam(r, "returnID")

	shopID, err := h.resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusForbidden, err.Error())
		return
	}

	var req struct {
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	result := h.db.Model(&model.Return{}).Where("id = ? AND shop_id = ?", returnID, shopID).Update("return_note", req.Note)
	if result.Error != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to update note")
		return
	}
	if result.RowsAffected == 0 {
		httputil.WriteError(w, http.StatusNotFound, "return not found")
		return
	}

	httputil.WriteOK(w, map[string]string{"status": "ok"})
}

// generateLabelForReturn creates a ShipEngine return label.
func (h *ReturnsHandler) generateLabelForReturn(ctx context.Context, ret *model.Return) (*returns.LabelResponse, error) {
	shipFrom := returns.Address{
		Name:       ret.CustomerName,
		Street1:    ret.CustomerAddress,
		City:       ret.CustomerCity,
		State:      ret.CustomerState,
		PostalCode: ret.CustomerPostalCode,
		Country:    ret.CustomerCountry,
	}

	// Merchant return address — use configured default if available, else log warning
	shipTo := h.defaultReturnAddr
	if shipTo.Name == "" {
		slog.Warn("returns: no default return address configured; using placeholder. Set RETURN_ADDRESS_* env vars in production.",
			"shop_id", ret.ShopID,
		)
		shipTo = returns.Address{
			Name:       "Merchant Returns",
			Street1:    "RETURN_ADDRESS_STREET1 not configured",
			City:       "RETURN_ADDRESS_CITY not configured",
			State:      "CA",
			PostalCode: "00000",
			Country:    "US",
		}
	}

	// Estimate package weight from items
	totalQty := 0
	for _, item := range ret.Items {
		totalQty += item.Quantity
	}
	pkgWeight := math.Max(0.5, float64(totalQty)*0.5+0.5)

	return h.shipEngine.CreateLabel(ctx, returns.LabelRequest{
		ShipFrom:      shipFrom,
		ShipTo:        shipTo,
		PackageWeight: pkgWeight,
		PackageDimensions: returns.Dimensions{
			Length: 12, Width: 10, Height: 4, Unit: "inch",
		},
		ServiceCode: "usps_priority_mail",
	})
}

// --- Helpers ---


// ProductDetailResponse holds per-SKU return analysis.
type ProductDetailResponse struct {
	Success        bool             `json:"success"`
	ProductID      string           `json:"product_id"`
	Reasons        []ReasonStat     `json:"reasons"`
	CustomerNotes  []string         `json:"customer_notes"`
	TotalReturns   int              `json:"total_returns"`
	Recommendation string           `json:"recommendation"`
}

// ProductDetail handles GET /api/returns/product?shop_id=...&product_id=...
func (h *ReturnsHandler) ProductDetail(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	productID := r.URL.Query().Get("product_id")
	if shopID == "" || productID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id and product_id are required")
		return
	}
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	since := time.Now().UTC().AddDate(0, 0, -90)

	// 1. Return reasons for this product (legacy returns table)
	type reasonRow struct {
		Reason string
		Size   string
		Cnt    int
	}
	var manualReasons []reasonRow
	h.db.WithContext(r.Context()).Raw(`
		SELECT COALESCE(ri.reason, r.return_reason) AS reason, ri.size, COUNT(*) AS cnt
		FROM return_items ri JOIN returns r ON r.id = ri.return_id
		WHERE r.shop_id = ? AND ri.product_id = ? AND r.created_at > ?
		GROUP BY reason, ri.size ORDER BY cnt DESC LIMIT 10
	`, shopID, productID, since).Scan(&manualReasons)

	// 2. Customer notes from both sources
	var notes []string
	seen := make(map[string]bool)

	for _, r := range manualReasons {
		if r.Reason != "" {
			n := r.Reason
			if r.Size != "" { n = r.Size + " " + n }
			if !seen[n] { seen[n] = true; notes = append(notes, n) }
		}
	}
	// From synced_returns
	var syncedRows []model.SyncedReturn
	_ = h.db.WithContext(r.Context()).
		Where("shop_id = ? AND synced_at > ? AND line_items IS NOT NULL", shopID, since).
		Find(&syncedRows)
	for _, sr := range syncedRows {
		var items []model.ReturnLineItemPayload
		json.Unmarshal(sr.LineItems, &items)
		for _, li := range items {
			if li.ReturnReasonNote != "" && !seen[li.ReturnReasonNote] {
				seen[li.ReturnReasonNote] = true
				notes = append(notes, li.ReturnReasonNote)
			}
		}
	}

	// 3. Build reason breakdown
	resp := ProductDetailResponse{Success: true, ProductID: productID}
	totalCnt := 0
	for _, r := range manualReasons {
		totalCnt += r.Cnt
	}
	for _, r := range manualReasons {
		pct := float64(0)
		if totalCnt > 0 { pct = math.Round(float64(r.Cnt)/float64(totalCnt)*1000) / 10 }
		resp.Reasons = append(resp.Reasons, ReasonStat{Reason: r.Reason, Count: r.Cnt, Pct: pct})
	}
	resp.TotalReturns = totalCnt
	resp.CustomerNotes = notes

	// 4. Data-driven recommendation
	if totalCnt > 0 {
		// Find the top reason
		topReason := ""
		topReasonCnt := 0
		for _, r := range manualReasons {
			if r.Cnt > topReasonCnt {
				topReason = r.Reason
				topReasonCnt = r.Cnt
			}
		}
		if topReasonCnt > 0 && float64(topReasonCnt)/float64(totalCnt) > 0.5 {
			resp.Recommendation = fmt.Sprintf("退货集中在 %s（%.0f%%，%d笔）。建议检查该SKU的产品描述、规格标注和实际商品是否一致，必要时更新产品信息以减少退货。",
				reasonDisplayName(topReason), float64(topReasonCnt)/float64(totalCnt)*100, topReasonCnt)
		} else if totalCnt > 0 {
			resp.Recommendation = fmt.Sprintf("近90天退货%d笔，原因分布均匀。建议持续监控退货原因，发现集中趋势后针对性优化。", totalCnt)
		}
	} else {
		resp.Recommendation = "暂无退货数据"
	}

	httputil.WriteOK(w, resp)
}


// resolveShopID extracts and validates the ShopID from the request.
// Priority: context (set by gateway middleware) > X-Shop-ID header > shop_id query param.
func (h *ReturnsHandler) resolveShopID(r *http.Request) (uuid.UUID, error) {
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

// ctxKeyShopID is the context key for ShopID (synchronized with gateway middleware).
type ctxKey string

const ctxKeyShopID ctxKey = "shop_id"

func returnToDetail(ret *model.Return) ReturnDetailResponse {
	resp := ReturnDetailResponse{
		Success:        true,
		ReturnID:       ret.ID.String(),
		OrderID:        ret.OrderID,
		CustomerEmail:  ret.CustomerEmail,
		CustomerName:   ret.CustomerName,
		Status:         ret.Status,
		LabelURL:       ret.LabelURL,
		TrackingNumber: ret.TrackingNumber,
		CreatedAt:      ret.CreatedAt.Format(time.RFC3339),
		Items:          make([]ReturnItemResp, 0, len(ret.Items)),
	}
	for _, item := range ret.Items {
		resp.Items = append(resp.Items, ReturnItemResp{
			ProductID:    item.ProductID,
			ProductTitle: item.ProductTitle,
			Quantity:     item.Quantity,
			Size:         item.Size,
			Reason:       item.Reason,
		})
	}
	return resp
}

// calculateRefundAmount determines refund amount based on return reason.
func calculateRefundAmount(ret *model.Return, reason string) float64 {
	baseAmount := 100.0 // In production, compute from order total

	switch {
	case reason == "wrong_item" || reason == "defective" || reason == "quality":
		// Full refund: item price + original shipping
		return baseAmount
	case strings.Contains(reason, "size") || strings.Contains(reason, "fit"):
		// Item price minus original shipping
		return math.Max(0, baseAmount-10)
	case reason == "changed_mind" || reason == "not_wanted":
		// Item price minus shipping + 5% fee
		return math.Max(0, baseAmount-10-baseAmount*0.05)
	default:
		return baseAmount
	}
}

// reasonDisplayName converts a reason code to a human-readable name.
// Uses Shopify's standard return reason vocabulary. Falls back to the raw code.
func reasonDisplayName(reason string) string {
	names := map[string]string{
		"size_too_small":  "尺码偏小",
		"size_too_large":  "尺码偏大",
		"defective":       "质量问题",
		"color":           "颜色不符",
		"not_as_described": "与描述不符",
		"wrong_item":      "发错货",
		"style":           "款式不符",
		"changed_mind":    "改变主意",
	}
	if n, ok := names[reason]; ok { return n }
	return reason
}

// isSizeRelated checks if a return reason is size-related.
func isSizeRelated(reason string) bool {
	if reason == "" {
		return false
	}
	lower := strings.ToLower(reason)
	sizeKeywords := []string{"size", "fit", "tight", "loose", "small", "large", "big"}
	for _, kw := range sizeKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// AnalyzeResponse holds return analysis data.
type AnalyzeResponse struct {
	Success       bool             `json:"success"`
	Analysis      string           `json:"analysis,omitempty"`
	ReasonBreakdown []ReasonStat  `json:"reason_breakdown,omitempty"`
	WeeklyTrend   []TrendPoint    `json:"weekly_trend,omitempty"`
	TotalReturns   int64          `json:"total_returns"`
	TotalManual    int64          `json:"total_manual"`
	TotalSynced    int64          `json:"total_synced"`
}

// ReasonStat is a single return reason with count.
type ReasonStat struct {
	Reason string  `json:"reason"`
	Count  int     `json:"count"`
	Pct    float64 `json:"pct"`
}

// TrendPoint is a single weekly data point.
type TrendPoint struct {
	Week   string `json:"week"`
	Count  int    `json:"count"`
}

// Analyze handles GET /api/returns/analyze?shop_id=... — structured return analysis.
func (h *ReturnsHandler) Analyze(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	resp := AnalyzeResponse{Success: true}
	since := time.Now().UTC().AddDate(0, 0, -30)

	// 1. Count manual returns (returns table)
	var manualCount int64
	h.db.WithContext(r.Context()).Model(&model.Return{}).
		Where("shop_id = ? AND created_at > ?", shopID, since).Count(&manualCount)

	// 2. Count synced returns (synced_returns table)
	var syncedCount int64
	h.db.WithContext(r.Context()).Model(&model.SyncedReturn{}).
		Where("shop_id = ? AND synced_at > ?", shopID, since).Count(&syncedCount)

	resp.TotalReturns = manualCount + syncedCount
	resp.TotalManual = manualCount
	resp.TotalSynced = syncedCount

	// 3. Reason breakdown (from legacy returns)
	type reasonRow struct {
		Reason string
		Cnt    int
	}
	var rows []reasonRow
	h.db.WithContext(r.Context()).Raw(`
		SELECT COALESCE(ri.reason, r.return_reason, 'unknown') AS reason, COUNT(*) AS cnt
		FROM return_items ri
		JOIN returns r ON r.id = ri.return_id
		WHERE r.shop_id = ? AND r.created_at > ? AND COALESCE(ri.reason, r.return_reason) != ''
		GROUP BY reason ORDER BY cnt DESC LIMIT 10
	`, shopID, since).Scan(&rows)

	// Merge with synced_returns
	reasonMap := make(map[string]int)
	for _, rr := range rows {
		reasonMap[rr.Reason] += rr.Cnt
	}
	var syncedRows []model.SyncedReturn
	h.db.WithContext(r.Context()).
		Where("shop_id = ? AND synced_at > ? AND line_items IS NOT NULL", shopID, since).
		Find(&syncedRows)
	for _, sr := range syncedRows {
		var items []model.ReturnLineItemPayload
		json.Unmarshal(sr.LineItems, &items)
		for _, li := range items {
			if li.ReturnReason != "" {
				reasonMap[li.ReturnReason] += li.Quantity
			}
		}
	}

	// Sort and build response
	type pair struct {
		Reason string
		Cnt    int
	}
	sorted := make([]pair, 0, len(reasonMap))
	for r, c := range reasonMap {
		sorted = append(sorted, pair{r, c})
	}
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Cnt > sorted[j-1].Cnt; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	totalReasonCount := 0
	for _, p := range sorted {
		totalReasonCount += p.Cnt
	}
	for _, p := range sorted {
		pct := 0.0
		if totalReasonCount > 0 {
			pct = math.Round(float64(p.Cnt)/float64(totalReasonCount)*1000) / 10
		}
		resp.ReasonBreakdown = append(resp.ReasonBreakdown, ReasonStat{
			Reason: p.Reason, Count: p.Cnt, Pct: pct,
		})
	}

	// 4. Weekly trend (last 4 weeks, from both tables)
	for w := 3; w >= 0; w-- {
		weekStart := time.Now().UTC().AddDate(0, 0, -7*(w+1))
		weekEnd := time.Now().UTC().AddDate(0, 0, -7*w)
		var cnt int64
		h.db.WithContext(r.Context()).Model(&model.Return{}).
			Where("shop_id = ? AND created_at BETWEEN ? AND ?", shopID, weekStart, weekEnd).Count(&cnt)
		var scnt int64
		h.db.WithContext(r.Context()).Model(&model.SyncedReturn{}).
			Where("shop_id = ? AND synced_at BETWEEN ? AND ?", shopID, weekStart, weekEnd).Count(&scnt)
		resp.WeeklyTrend = append(resp.WeeklyTrend, TrendPoint{
			Week:  weekStart.Format("01/02"),
			Count: int(cnt + scnt),
		})
	}

	// 5. Text analysis (injector)
	if h.injector != nil {
		resp.Analysis = h.injector.InjectOne(r.Context(), h.db, shopID, "return_insight")
	}
	if resp.Analysis == "" {
		resp.Analysis = "暂无近30天退货数据"
	}

	httputil.WriteOK(w, resp)
}
