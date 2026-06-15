package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// AISummaryHandler exposes aggregated AI value metrics.
type AISummaryHandler struct {
	db *gorm.DB
}

// NewAISummaryHandler creates a new handler.
func NewAISummaryHandler(db *gorm.DB) *AISummaryHandler {
	return &AISummaryHandler{db: db}
}

// AISummary aggregates daily AI activity across all features.
type AISummary struct {
	Date string `json:"date"`

	// Auto Reply
	RepliesGenerated  int64 `json:"replies_generated"`
	RepliesPublished   int64 `json:"replies_published"`
	RepliesFailed      int64 `json:"replies_failed"`
	DiscountCodesSent  int64 `json:"discount_codes_sent"`
	RecoveryRevenue    float64 `json:"recovery_revenue"` // attributed repurchase from auto reply

	// Sales Agent
	ChatConversations int64 `json:"chat_conversations"`
	PurchaseIntents   int64 `json:"purchase_intents"`
	DiscountsIssued   int64 `json:"discounts_issued"`
	ProductsRecommended int64 `json:"products_recommended"`

	// Content Factory
	ContentGenerated int64 `json:"content_generated"`
	ContentPublished int64 `json:"content_published"`

	// LLM Usage
	TotalAPICalls   int64   `json:"total_api_calls"`
	TotalTokensIn   int64   `json:"total_tokens_in"`
	TotalTokensOut  int64   `json:"total_tokens_out"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`

	// Returns & Exchange
	ReturnsHandled  int64 `json:"returns_handled"`
	ExchangeRecommendations int64 `json:"exchange_recommendations"`
}

// GetSummary handles GET /api/analytics/ai-summary?shop_id=xxx&days=7
func (h *AISummaryHandler) GetSummary(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("shop_id")
	if sid == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id required")
		return
	}
	shopID, err := uuid.Parse(sid)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid shop_id")
		return
	}

	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 7
	}

	ctx := r.Context()
	since := time.Now().UTC().AddDate(0, 0, -days)
	today := time.Now().UTC().Format("2006-01-02")

	var s AISummary
	s.Date = today

	type row struct{ Count int64 }
	var rr row

	// ── Auto Reply ──
	if err := h.db.WithContext(ctx).Table("auto_reply_logs").
		Where("created_at >= ?", since).Where("shop_id = ?", shopID).
		Select("COUNT(*) as count").Scan(&rr).Error; err != nil { slog.Error("ai_summary: query failed", "error", err) } else if true {
		s.RepliesGenerated = rr.Count
	}
	rr.Count = 0
	if err := h.db.WithContext(ctx).Table("auto_reply_logs").
		Where("created_at >= ?", since).Where("shop_id = ?", shopID).
		Where("status = 'published'").
		Select("COUNT(*) as count").Scan(&rr).Error; err != nil { slog.Error("ai_summary: query failed", "error", err) } else if true {
		s.RepliesPublished = rr.Count
	}
	rr.Count = 0
	if err := h.db.WithContext(ctx).Table("auto_reply_logs").
		Where("created_at >= ?", since).Where("shop_id = ?", shopID).
		Where("status = 'failed'").
		Select("COUNT(*) as count").Scan(&rr).Error; err != nil { slog.Error("ai_summary: query failed", "error", err) } else if true {
		s.RepliesFailed = rr.Count
	}
	rr.Count = 0
	if err := h.db.WithContext(ctx).Table("review_reply_codes").
		Where("created_at >= ?", since).Where("shop_id = ?", shopID).
		Select("COUNT(*) as count").Scan(&rr).Error; err != nil { slog.Error("ai_summary: query failed", "error", err) } else if true {
		s.DiscountCodesSent = rr.Count
	}

	// Recovery revenue from review_recovery_attributions
	var rev struct{ Sum float64 }
	if err := h.db.WithContext(ctx).Table("review_recovery_attributions").
		Where("attributed_at >= ?", since).Where("shop_id = ?", shopID).
		Select("COALESCE(SUM(order_total), 0) as sum").Scan(&rev).Error; err == nil {
		s.RecoveryRevenue = rev.Sum
	}

	// ── Sales Agent ──
	rr.Count = 0
	if err := h.db.WithContext(ctx).Table("chat_intent_events").
		Where("created_at >= ?", since).Where("shop_id = ?", shopID).
		Select("COUNT(*) as count").Scan(&rr).Error; err != nil { slog.Error("ai_summary: query failed", "error", err) } else if true {
		s.ChatConversations = rr.Count
	}
	rr.Count = 0
	if err := h.db.WithContext(ctx).Table("chat_intent_events").
		Where("created_at >= ?", since).Where("shop_id = ?", shopID).
		Where("event_type LIKE '%purchase_intent%' OR event_type LIKE '%intent_high%'").
		Select("COUNT(*) as count").Scan(&rr).Error; err != nil { slog.Error("ai_summary: query failed", "error", err) } else if true {
		s.PurchaseIntents = rr.Count
	}
	rr.Count = 0
	if err := h.db.WithContext(ctx).Table("chat_intent_events").
		Where("created_at >= ?", since).Where("shop_id = ?", shopID).
		Where("event_type LIKE '%discount%'").
		Select("COUNT(*) as count").Scan(&rr).Error; err != nil { slog.Error("ai_summary: query failed", "error", err) } else if true {
		s.DiscountsIssued = rr.Count
	}
	rr.Count = 0
	if err := h.db.WithContext(ctx).Table("chat_intent_events").
		Where("created_at >= ?", since).Where("shop_id = ?", shopID).
		Where("event_type LIKE '%product%' OR event_type LIKE '%recommend%'").
		Select("COUNT(*) as count").Scan(&rr).Error; err != nil { slog.Error("ai_summary: query failed", "error", err) } else if true {
		s.ProductsRecommended = rr.Count
	}

	// ── Content Factory ──
	rr.Count = 0
	if err := h.db.WithContext(ctx).Table("content_pieces").
		Where("created_at >= ?", since).Where("shop_id = ?", shopID).
		Select("COUNT(*) as count").Scan(&rr).Error; err != nil { slog.Error("ai_summary: query failed", "error", err) } else if true {
		s.ContentGenerated = rr.Count
	}
	rr.Count = 0
	if err := h.db.WithContext(ctx).Table("content_pieces").
		Where("created_at >= ?", since).Where("shop_id = ?", shopID).
		Where("status = 'published'").
		Select("COUNT(*) as count").Scan(&rr).Error; err != nil { slog.Error("ai_summary: query failed", "error", err) } else if true {
		s.ContentPublished = rr.Count
	}

	// ── LLM Token Usage ──
	var tokRow struct {
		Calls     int64
		TokensIn  int64
		TokensOut int64
	}
	if err := h.db.WithContext(ctx).Table("token_usages").
		Where("recorded_at >= ?", since).Where("shop_id = ?", shopID).
		Select("COUNT(*) as calls, COALESCE(SUM(count), 0) as tokens_in, 0 as tokens_out").Scan(&tokRow).Error; err == nil {
		s.TotalAPICalls = tokRow.Calls
		s.TotalTokensIn = tokRow.TokensIn
		s.TotalTokensOut = tokRow.TokensOut
		s.EstimatedCostUSD = float64(tokRow.TokensIn)*0.000001 + float64(tokRow.TokensOut)*0.000002 // ~$1/1M tokens
	}

	// ── Returns & Exchange ──
	rr.Count = 0
	if err := h.db.WithContext(ctx).Table("returns").
		Where("created_at >= ?", since).Where("shop_id = ?", shopID).Where("status IN ('approved','pending')").
		Select("COUNT(*) as count").Scan(&rr).Error; err != nil { slog.Error("ai_summary: query failed", "error", err) } else if true {
		s.ReturnsHandled = rr.Count
	}
	rr.Count = 0
	if err := h.db.WithContext(ctx).Table("exchange_ai_recommendations").
		Where("created_at >= ?", since).Where("shop_id = ?", shopID).
		Select("COUNT(*) as count").Scan(&rr).Error; err != nil { slog.Error("ai_summary: query failed", "error", err) } else if true {
		s.ExchangeRecommendations = rr.Count
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"summary": s,
	})
}
