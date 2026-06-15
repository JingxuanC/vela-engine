package handler

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// ReviewAnalyticsHandler exposes auto-reply performance dashboards.
type ReviewAnalyticsHandler struct {
	db *gorm.DB
}

// NewReviewAnalyticsHandler creates a new ReviewAnalyticsHandler.
func NewReviewAnalyticsHandler(db *gorm.DB) *ReviewAnalyticsHandler {
	return &ReviewAnalyticsHandler{db: db}
}

// ReviewOverviewResponse is the auto-reply analytics overview.
type ReviewOverviewResponse struct {
	Success bool `json:"success"`

	ReviewsHandled   int64   `json:"reviews_handled"`
	RepliesSent      int64   `json:"replies_sent"`
	CodesGenerated   int64   `json:"codes_generated"`
	CodesUsed        int64   `json:"codes_used"`
	OrdersRecovered  int64   `json:"orders_recovered"`
	RevenueRecovered float64 `json:"revenue_recovered"`

	Period string `json:"period"`
}

// ReviewValueResponse is the "value proof" dashboard — shows merchants
// what AI actually did for them in human terms.
type ReviewValueResponse struct {
	Success bool `json:"success"`

	// Time saved
	AutoRepliesSent int64   `json:"auto_replies_sent"` // replies sent automatically
	HoursSaved      float64 `json:"hours_saved"`       // auto_replies × 3 min

	// Repurchase impact — customers who came back after an auto-reply
	RepliedCustomers      int64   `json:"replied_customers"`       // unique customers who got a reply
	CustomersRepurchased  int64   `json:"customers_repurchased"`   // of those, how many came back
	RepurchaseRate        float64 `json:"repurchase_rate"`         // percentage
	RepurchaseRevenue     float64 `json:"repurchase_revenue"`      // total repurchase amount

	// Monthly revenue trend
	MonthlyTrend []MonthlyTrendPoint `json:"monthly_trend"`

	// Comparison: avg rating before vs after auto-reply
	AvgRatingBefore float64 `json:"avg_rating_before"` // avg rating of replied reviews
	AvgRatingAfter  float64 `json:"avg_rating_after"`  // avg rating of reviews AFTER first reply sent (if available)

	Period string `json:"period"`
}

// MonthlyTrendPoint is one month of revenue trend.
type MonthlyTrendPoint struct {
	Month   string  `json:"month"`   // "2026-06"
	Revenue float64 `json:"revenue"` // discount code + repurchase revenue
}

// Value handles GET /api/review/analytics/value.
func (h *ReviewAnalyticsHandler) Value(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteOK(w, ReviewValueResponse{Success: true})
		return
	}

	shopIDStr := r.URL.Query().Get("shop_id")
	shopID, err := database.ResolveShopID(h.db, shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	period := r.URL.Query().Get("period")
	if period == "" {
		period = "30d"
	}
	days := 30
	if d, err := parseInt(period); err == nil && d > 0 {
		days = d
	}
	since := time.Now().UTC().AddDate(0, 0, -days)

	resp := ReviewValueResponse{Success: true, Period: period}

	// ── Time saved ──
	h.db.WithContext(r.Context()).Model(&model.ReplyRecord{}).
		Where("shop_id = ? AND send_mode = 'auto' AND status = 'sent' AND sent_at >= ?", shopID, since).
		Count(&resp.AutoRepliesSent)
	resp.HoursSaved = float64(resp.AutoRepliesSent) * 3.0 / 60.0

	// ── Repurchase impact ──
	// Unique customers who got a reply
	h.db.WithContext(r.Context()).Model(&model.ReplyRecord{}).
		Select("COUNT(DISTINCT customer_email)").
		Where("shop_id = ? AND status = 'sent' AND sent_at >= ?", shopID, since).
		Scan(&resp.RepliedCustomers)

	// Of those, how many came back
	h.db.WithContext(r.Context()).Model(&model.ReviewRecoveryAttribution{}).
		Where("shop_id = ? AND repurchased_at >= ?", shopID, since).
		Count(&resp.CustomersRepurchased)

	if resp.RepliedCustomers > 0 {
		resp.RepurchaseRate = float64(resp.CustomersRepurchased) / float64(resp.RepliedCustomers) * 100
	}

	// Total repurchase revenue
	type sumRow struct{ Total float64 }
	var repSum sumRow
	h.db.WithContext(r.Context()).Model(&model.ReviewRecoveryAttribution{}).
		Select("COALESCE(SUM(repurchase_amount), 0) AS total").
		Where("shop_id = ? AND repurchased_at >= ?", shopID, since).
		Scan(&repSum)
	resp.RepurchaseRevenue = repSum.Total

	// ── Rating snapshot ──
	type ratingRow struct{ Avg float64 }
	var beforeRating ratingRow
	h.db.WithContext(r.Context()).Model(&model.ReplyRecord{}).
		Select("COALESCE(AVG(rating), 0) AS avg").
		Where("shop_id = ? AND created_at >= ?", shopID, since).
		Scan(&beforeRating)
	resp.AvgRatingBefore = beforeRating.Avg

	// ── Monthly revenue trend (last 6 months) ──
	sixMonthsAgo := time.Now().UTC().AddDate(0, -6, 0)
	var trendRows []struct {
		Month   string
		Revenue float64
	}
	// Union: discount code revenue + repurchase revenue per month
	h.db.WithContext(r.Context()).Raw(`
		SELECT month, COALESCE(SUM(revenue), 0) AS revenue FROM (
			SELECT TO_CHAR(used_at, 'YYYY-MM') AS month, COALESCE(order_total, 0) AS revenue
			FROM review_reply_codes
			WHERE shop_id = ? AND is_used = true AND used_at >= ?
			UNION ALL
			SELECT TO_CHAR(repurchased_at, 'YYYY-MM') AS month, repurchase_amount AS revenue
			FROM review_recovery_attributions
			WHERE shop_id = ? AND repurchased_at >= ?
		) t GROUP BY month ORDER BY month
	`, shopID, sixMonthsAgo, shopID, sixMonthsAgo).Scan(&trendRows)

	for _, tr := range trendRows {
		resp.MonthlyTrend = append(resp.MonthlyTrend, MonthlyTrendPoint{
			Month:   tr.Month,
			Revenue: tr.Revenue,
		})
	}
	if resp.MonthlyTrend == nil {
		resp.MonthlyTrend = []MonthlyTrendPoint{}
	}

	httputil.WriteOK(w, resp)
}

// Overview handles GET /api/review/analytics/overview.
func (h *ReviewAnalyticsHandler) Overview(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteOK(w, ReviewOverviewResponse{Success: true})
		return
	}

	shopIDStr := r.URL.Query().Get("shop_id")
	shopID, err := database.ResolveShopID(h.db, shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	period := r.URL.Query().Get("period")
	if period == "" {
		period = "30d"
	}
	days := 30
	if d, err := parseInt(period); err == nil && d > 0 {
		days = d
	}
	since := time.Now().UTC().AddDate(0, 0, -days)

	resp := ReviewOverviewResponse{Success: true, Period: period}

	// Reviews handled (sent replies)
	h.db.WithContext(r.Context()).Model(&model.ReplyRecord{}).
		Where("shop_id = ? AND status = 'sent' AND sent_at >= ?", shopID, since).
		Count(&resp.RepliesSent)

	// Total replies (including pending)
	h.db.WithContext(r.Context()).Model(&model.ReplyRecord{}).
		Where("shop_id = ? AND created_at >= ?", shopID, since).
		Count(&resp.ReviewsHandled)

	// Codes generated
	h.db.WithContext(r.Context()).Model(&model.ReviewReplyCode{}).
		Where("shop_id = ? AND created_at >= ?", shopID, since).
		Count(&resp.CodesGenerated)

	// Codes used
	h.db.WithContext(r.Context()).Model(&model.ReviewReplyCode{}).
		Where("shop_id = ? AND is_used = true AND used_at >= ?", shopID, since).
		Count(&resp.CodesUsed)

	resp.OrdersRecovered = resp.CodesUsed

	// Revenue recovered — sum order totals from attribution
	type revenueRow struct {
		Total float64
	}
	var rev revenueRow
	h.db.WithContext(r.Context()).Model(&model.ReviewReplyCode{}).
		Select("COALESCE(SUM(order_total), 0) AS total").
		Where("shop_id = ? AND is_used = true AND used_at >= ?", shopID, since).
		Scan(&rev)
	resp.RevenueRecovered = rev.Total

	httputil.WriteOK(w, resp)
}

// Products handles GET /api/review/analytics/products.
func (h *ReviewAnalyticsHandler) Products(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteOK(w, map[string]interface{}{"success": true, "products": []interface{}{}})
		return
	}

	shopIDStr := r.URL.Query().Get("shop_id")
	shopID, err := uuid.Parse(shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	var insights []model.ProductReviewInsight
	h.db.WithContext(r.Context()).
		Where("shop_id = ?", shopID).
		Order("negative_reviews DESC").
		Limit(10).
		Find(&insights)

	httputil.WriteOK(w, map[string]interface{}{"success": true, "products": insights})
}

// parseInt extracts the integer prefix from a string like "30d".
func parseInt(s string) (int, error) {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
