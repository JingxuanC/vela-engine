package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/internal/service/review"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// ReviewAutoReplyHandler handles the bad review auto-reply workflow.
type ReviewAutoReplyHandler struct {
	db        *gorm.DB
	cache     *service.CacheService
	autoReply *review.AutoReplyService
	eventBus  eventbus.EventBus
}

// NewReviewAutoReplyHandler creates a new ReviewAutoReplyHandler.
func NewReviewAutoReplyHandler(db *gorm.DB, cache *service.CacheService, autoReply *review.AutoReplyService, bus eventbus.EventBus) *ReviewAutoReplyHandler {
	return &ReviewAutoReplyHandler{db: db, cache: cache, autoReply: autoReply, eventBus: bus}
}

// --- DTOs ---

// ReplyListResponse is the paginated reply list.
type ReplyListResponse struct {
	Success bool                  `json:"success"`
	Replies []ReplyRecordResponse `json:"replies"`
	Total   int64                 `json:"total"`
	Limit   int                   `json:"limit"`
	Offset  int                   `json:"offset"`
}

// ReplyRecordResponse is a single reply record.
type ReplyRecordResponse struct {
	ID             string   `json:"id"`
	ShopID         string   `json:"shop_id"`
	ReviewID       string   `json:"review_id"`
	ProductID      string   `json:"product_id"`
	CustomerName   string   `json:"customer_name"`
	CustomerEmail  string   `json:"customer_email"`
	Rating         float64  `json:"rating"`
	ReviewTitle    string   `json:"review_title"`
	ReviewBody     string   `json:"review_body"`
	SentimentScore float64  `json:"sentiment_score"`
	AiReplies      []string `json:"ai_replies"`
	FinalReply     string   `json:"final_reply"`
	Status         string   `json:"status"`
	SendMode       string   `json:"send_mode"`
	SentAt         *string  `json:"sent_at,omitempty"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}

// GenerateReplyRequest is the request to manually trigger a bad review reply.
type GenerateReplyRequest struct {
	ShopID        string  `json:"shop_id"`
	ReviewID      string  `json:"review_id"`
	ProductID     string  `json:"product_id"`
	ProductName   string  `json:"product_name"`
	CustomerName  string  `json:"customer_name"`
	CustomerEmail string  `json:"customer_email"`
	Rating        float64 `json:"rating"`
	Title         string  `json:"title"`
	Body          string  `json:"body"`
}

// EditReplyRequest is the request to edit a final reply.
type EditReplyRequest struct {
	FinalReply string `json:"final_reply"`
	Status     string `json:"status"`
}

// --- Handlers ---

// List handles GET /api/review/replies — paginated list of reply records.
func (h *ReviewAutoReplyHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	// shop_id is required
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

	query := h.db.WithContext(r.Context()).Model(&model.ReplyRecord{}).Where("shop_id = ?", shopID)

	// Filters
	if status := r.URL.Query().Get("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	if ratingStr := r.URL.Query().Get("min_rating"); ratingStr != "" {
		if minRating, err := strconv.ParseFloat(ratingStr, 64); err == nil {
			query = query.Where("rating >= ?", minRating)
		}
	}
	if ratingStr := r.URL.Query().Get("max_rating"); ratingStr != "" {
		if maxRating, err := strconv.ParseFloat(ratingStr, 64); err == nil {
			query = query.Where("rating <= ?", maxRating)
		}
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

	var records []model.ReplyRecord
	query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&records)

	replies := make([]ReplyRecordResponse, 0, len(records))
	for i := range records {
		replies = append(replies, replyRecordToResponse(&records[i]))
	}

	httputil.WriteOK(w, ReplyListResponse{
		Success: true,
		Replies: replies,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	})
}

// Approve handles POST /api/review/replies/{id}/approve
func (h *ReviewAutoReplyHandler) Approve(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid reply ID")
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

	var record model.ReplyRecord
	if err := h.db.WithContext(r.Context()).Where("id = ? AND shop_id = ?", id, shopID).First(&record).Error; err != nil {
		httputil.WriteError(w, http.StatusNotFound, "Reply record not found")
		return
	}

	now := time.Now().UTC()
	record.Status = "sent"
	record.SentAt = &now

	if record.FinalReply == "" {
		// If no final reply selected, use the first AI-generated reply
		if len(record.AiReplies) > 0 {
			var replies []string
			if err := json.Unmarshal(record.AiReplies, &replies); err == nil && len(replies) > 0 {
				record.FinalReply = replies[0]
			}
		}
	}

	if err := h.db.WithContext(r.Context()).Model(&record).Updates(map[string]interface{}{
		"status":      "sent",
		"sent_at":     &now,
		"final_reply": record.FinalReply,
	}).Error; err != nil {
		slog.Error("review_auto_reply: failed to approve reply", "id", id, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to approve reply")
		return
	}

	// Post reply to the review platform via adapter (default: judgeme)
	platform := "judgeme"
	if h.autoReply != nil && record.PlatformID != "" && record.FinalReply != "" {
		if err := h.autoReply.PostReply(r.Context(), platform, shopID.String(), record.PlatformID, record.FinalReply); err != nil {
			slog.Error("review_auto_reply: failed to post reply", "id", id, "platform", platform, "error", err)
			httputil.WriteError(w, http.StatusInternalServerError, "Reply saved but failed to post to platform: "+err.Error())
			return
		}
	}

	slog.Info("review_auto_reply: reply approved and posted", "id", id, "platform", platform)

	// Publish event for VCI pipeline (customer profile + product insights)
	if h.eventBus != nil && record.CustomerEmail != "" {
		evt, err := eventbus.NewEvent(
			eventbus.EventReviewReplied,
			shopID,
			map[string]interface{}{
				"platform_id":      record.PlatformID,
				"customer_email":   record.CustomerEmail,
				"customer_id":      record.CustomerID,
				"severity":         record.Severity,
				"sentiment":        "negative",
				"issues":           record.Issues,
				"product_id":       record.ProductID,
				"discount_code":    record.DiscountCode,
				"discount_percent": record.DiscountPercent,
			},
			"auto_reply",
		)
		if err == nil {
			if err := h.eventBus.Publish(r.Context(), evt); err != nil {
				slog.Warn("review_auto_reply: failed to publish event", "id", id, "err", err)
			}
		}
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"status":  "sent",
	})
}

// Reject handles POST /api/review/replies/{id}/reject
func (h *ReviewAutoReplyHandler) Reject(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid reply ID")
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

	if err := h.db.WithContext(r.Context()).Model(&model.ReplyRecord{}).Where("id = ? AND shop_id = ?", id, shopID).Update("status", "rejected").Error; err != nil {
		slog.Error("review_auto_reply: failed to reject reply", "id", id, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to reject reply")
		return
	}

	slog.Info("review_auto_reply: reply rejected", "id", id)
	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"status":  "rejected",
	})
}

// Edit handles PUT /api/review/replies/{id}
func (h *ReviewAutoReplyHandler) Edit(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid reply ID")
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

	var req EditReplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.FinalReply == "" && req.Status == "" {
		httputil.WriteError(w, http.StatusBadRequest, "final_reply or status is required")
		return
	}

	updates := map[string]interface{}{}
	if req.FinalReply != "" {
		updates["final_reply"] = req.FinalReply
	}
	if req.Status != "" {
		updates["status"] = req.Status
	}
	if err := h.db.WithContext(r.Context()).Model(&model.ReplyRecord{}).Where("id = ? AND shop_id = ?", id, shopID).Updates(updates).Error; err != nil {
		slog.Error("review_auto_reply: failed to edit reply", "id", id, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to edit reply")
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
	})
}

// Generate handles POST /api/review/replies — manually trigger bad review reply generation.
func (h *ReviewAutoReplyHandler) Generate(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	var req GenerateReplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.ShopID == "" || req.ReviewID == "" || req.Body == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id, review_id, and body are required")
		return
	}

	shopID, err := database.ResolveShopID(h.db, req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	// 1. Analyze the review
	analysis := review.AnalyzeBadReview(review.ReviewInput{
		ID:     req.ReviewID,
		Author: req.CustomerName,
		Rating: req.Rating,
		Title:  req.Title,
		Body:   req.Body,
	})

	// 2. Generate reply drafts via LLM
	productName := req.ProductName
	if productName == "" {
		productName = "this product"
	}

	replies := review.GenerateReplies(analysis, productName)

	// Marshal AI replies to JSON
	repliesJSON, _ := json.Marshal(replies)

	// Create reply record (manual trigger always defaults to manual mode)

	record := model.ReplyRecord{
		ShopID:         shopID,
		PlatformID:     req.ReviewID,
		ProductID:      req.ProductID,
		CustomerName:   req.CustomerName,
		CustomerEmail:  req.CustomerEmail,
		Rating:         req.Rating,
		ReviewTitle:    req.Title,
		ReviewBody:     req.Body,
		AiReplies:      repliesJSON,
		SentimentScore: analysis.SeverityScore,
		Status:         "pending",
		SendMode:       "manual", // manual trigger defaults to manual
	}

	if err := h.db.WithContext(r.Context()).Create(&record).Error; err != nil {
		slog.Error("review_auto_reply: failed to create reply record", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to create reply record")
		return
	}

	slog.Info("review_auto_reply: reply generated",
		"review_id", req.ReviewID,
		"shop_id", shopID,
		"actionable", analysis.IsActionable,
		"severity", analysis.Severity,
	)

	httputil.WriteCreated(w, map[string]interface{}{
		"success":    true,
		"record_id":  record.ID.String(),
		"ai_replies": replies,
		"analysis":   analysis,
		"status":     record.Status,
	})
}

// replyRecordToResponse converts a model.ReplyRecord to a response DTO.
func replyRecordToResponse(r *model.ReplyRecord) ReplyRecordResponse {
	resp := ReplyRecordResponse{
		ID:             r.ID.String(),
		ShopID:         r.ShopID.String(),
		ReviewID:       r.PlatformID,
		ProductID:      r.ProductID,
		CustomerName:   r.CustomerName,
		CustomerEmail:  r.CustomerEmail,
		Rating:         r.Rating,
		ReviewTitle:    r.ReviewTitle,
		ReviewBody:     r.ReviewBody,
		SentimentScore: r.SentimentScore,
		FinalReply:     r.FinalReply,
		Status:         r.Status,
		SendMode:       r.SendMode,
		CreatedAt:      r.CreatedAt.Format(time.RFC3339),
		UpdatedAt:      r.UpdatedAt.Format(time.RFC3339),
	}

	// Unmarshal AI replies
	if len(r.AiReplies) > 0 {
		var replies []string
		if err := json.Unmarshal(r.AiReplies, &replies); err == nil {
			resp.AiReplies = replies
		}
	}

	if r.SentAt != nil {
		s := r.SentAt.Format(time.RFC3339)
		resp.SentAt = &s
	}

	return resp
}
