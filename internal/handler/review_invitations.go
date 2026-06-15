package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// ReviewInvitationsHandler handles review invitation API endpoints.
type ReviewInvitationsHandler struct {
	db      *gorm.DB
	inviter *service.ReviewInviter
}

// NewReviewInvitationsHandler creates a new ReviewInvitationsHandler.
func NewReviewInvitationsHandler(db *gorm.DB, inviter *service.ReviewInviter) *ReviewInvitationsHandler {
	return &ReviewInvitationsHandler{db: db, inviter: inviter}
}

// InvitationResponse is the DTO for a single review invitation.
type InvitationResponse struct {
	ID            string  `json:"id"`
	ShopID        string  `json:"shop_id"`
	OrderID       int64   `json:"order_id"`
	ProductID     int64   `json:"product_id"`
	CustomerEmail string  `json:"customer_email"`
	CustomerName  string  `json:"customer_name"`
	ProductTitle  string  `json:"product_title"`
	ProductImage  string  `json:"product_image"`
	Status        string  `json:"status"`
	SendAt        *string `json:"send_at,omitempty"`
	SentAt        *string `json:"sent_at,omitempty"`
	ReviewID      int64   `json:"review_id"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

// InvitationListResponse is the paginated list response.
type InvitationListResponse struct {
	Success     bool                 `json:"success"`
	Invitations []InvitationResponse `json:"invitations"`
	Total       int64                `json:"total"`
	Limit       int                  `json:"limit"`
	Offset      int                  `json:"offset"`
}

// InvitationStatsResponse is the aggregated stats response.
type InvitationStatsResponse struct {
	Success          bool    `json:"success"`
	SentCount        int64   `json:"sent_count"`
	ConvertedCount   int64   `json:"converted_count"`
	ConversionRate   float64 `json:"conversion_rate"`
	ReviewCount      int64   `json:"review_count"`
	AvgRating        float64 `json:"avg_rating"`
	AttributedRevenue float64 `json:"attributed_revenue"`
	AttributedOrders  int64   `json:"attributed_orders"`
}

// List handles GET /api/reviews/invitations?days=30&limit=20&offset=0
func (h *ReviewInvitationsHandler) List(w http.ResponseWriter, r *http.Request) {
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

	// Default to 30 days
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 && parsed <= 365 {
			days = parsed
		}
	}
	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)

	query := h.db.WithContext(r.Context()).Model(&model.ReviewInvitation{}).
		Where("shop_id = ? AND created_at >= ?", shopID, since)

	// Optional status filter
	if status := r.URL.Query().Get("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	// Pagination
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	var total int64
	query.Count(&total)

	var invitations []model.ReviewInvitation
	query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&invitations)

	items := make([]InvitationResponse, 0, len(invitations))
	for _, inv := range invitations {
		items = append(items, invitationToResponse(&inv))
	}

	httputil.WriteOK(w, InvitationListResponse{
		Success:     true,
		Invitations: items,
		Total:       total,
		Limit:       limit,
		Offset:      offset,
	})
}

// Stats handles GET /api/reviews/invitations/stats?days=30
func (h *ReviewInvitationsHandler) Stats(w http.ResponseWriter, r *http.Request) {
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

	// Default to 30 days
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 && parsed <= 365 {
			days = parsed
		}
	}
	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)

	ctx := r.Context()
	db := h.db.WithContext(ctx)

	var sentCount int64
	db.Model(&model.ReviewInvitation{}).
		Where("shop_id = ? AND status = ? AND created_at >= ?", shopID, "sent", since).
		Count(&sentCount)

	var convertedCount int64
	db.Model(&model.ReviewInvitation{}).
		Where("shop_id = ? AND status = ? AND created_at >= ?", shopID, "converted", since).
		Count(&convertedCount)

	var reviewCount int64
	db.Model(&model.ReviewInvitation{}).
		Where("shop_id = ? AND review_id > 0 AND created_at >= ?", shopID, since).
		Count(&reviewCount)

	// Average rating from synced_reviews (if available)
	var avgRating float64
	db.Model(&model.SyncedReview{}).
		Where("shop_id = ? AND created_at >= ?", shopID, since).
		Select("COALESCE(AVG(rating), 0)").
		Scan(&avgRating)

	// Conversion rate: converted / sent (avoid division by zero)
	conversionRate := 0.0
	if sentCount > 0 {
		conversionRate = float64(convertedCount) / float64(sentCount) * 100
	}

	// Attributed revenue from review invitations
	var attributedRevenue float64
	db.Model(&model.ReviewInvitation{}).
		Where("shop_id = ? AND created_at >= ?", shopID, since).
		Select("COALESCE(SUM(attributed_revenue), 0)").
		Scan(&attributedRevenue)

	var attributedOrders int64
	db.Model(&model.ReviewInvitation{}).
		Where("shop_id = ? AND created_at >= ?", shopID, since).
		Select("COALESCE(SUM(attributed_orders), 0)").
		Scan(&attributedOrders)

	httputil.WriteOK(w, InvitationStatsResponse{
		Success:          true,
		SentCount:        sentCount,
		ConvertedCount:   convertedCount,
		ConversionRate:   conversionRate,
		ReviewCount:      reviewCount,
		AvgRating:        avgRating,
		AttributedRevenue: attributedRevenue,
		AttributedOrders:  attributedOrders,
	})
}

// Resend handles POST /api/reviews/invitations/{id}/resend
func (h *ReviewInvitationsHandler) Resend(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid invitation ID")
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

	var inv model.ReviewInvitation
	if err := h.db.WithContext(r.Context()).Where("id = ? AND shop_id = ?", id, shopID).First(&inv).Error; err != nil {
		httputil.WriteError(w, http.StatusNotFound, "Invitation not found")
		return
	}

	// Only resend pending or sent invitations
	if inv.Status != "pending" && inv.Status != "sent" {
		httputil.WriteError(w, http.StatusBadRequest, "Can only resend pending or sent invitations")
		return
	}

	// Execute the send immediately
	if h.inviter != nil {
		if err := h.inviter.ExecuteTask(r.Context(), inv.ID.String()); err != nil {
			slog.Error("review_invitations: resend failed", "invitation_id", id, "error", err)
			httputil.WriteError(w, http.StatusInternalServerError, "Failed to resend invitation: "+err.Error())
			return
		}
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"message": "Invitation resent",
	})
}

func invitationToResponse(inv *model.ReviewInvitation) InvitationResponse {
	resp := InvitationResponse{
		ID:            inv.ID.String(),
		ShopID:        inv.ShopID.String(),
		OrderID:       inv.OrderID,
		ProductID:     inv.ProductID,
		CustomerEmail: inv.CustomerEmail,
		CustomerName:  inv.CustomerName,
		ProductTitle:  inv.ProductTitle,
		ProductImage:  inv.ProductImage,
		Status:        inv.Status,
		ReviewID:      inv.ReviewID,
		CreatedAt:     inv.CreatedAt.Format(time.RFC3339),
		UpdatedAt:     inv.UpdatedAt.Format(time.RFC3339),
	}

	if inv.SendAt != nil {
		s := inv.SendAt.Format(time.RFC3339)
		resp.SendAt = &s
	}
	if inv.SentAt != nil {
		s := inv.SentAt.Format(time.RFC3339)
		resp.SentAt = &s
	}

	return resp
}
