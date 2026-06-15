package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// CartRecoveryAnalyticsHandler provides analytics endpoints for cart recovery.
type CartRecoveryAnalyticsHandler struct {
	db *gorm.DB
}

// NewCartRecoveryAnalyticsHandler creates a new CartRecoveryAnalyticsHandler.
func NewCartRecoveryAnalyticsHandler(db *gorm.DB) *CartRecoveryAnalyticsHandler {
	return &CartRecoveryAnalyticsHandler{db: db}
}

// ── Overview ──────────────────────────────────────────────────────────────────

// OverviewMetrics is the response for GET /api/cart-recovery/analytics/overview.
// Flat response matching frontend expectations.
type OverviewMetrics struct {
	RevenueRecovered float64 `json:"revenue_recovered"`
	OrdersRecovered  int64   `json:"orders_recovered"`
	EmailsSent       int64   `json:"emails_sent"`
	RecoveryRate     float64 `json:"recovery_rate"`
	TotalAbandoned   int64   `json:"total_abandoned"`
}

// Overview handles GET /api/cart-recovery/analytics/overview.
func (h *CartRecoveryAnalyticsHandler) Overview(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	shopID, err := resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	period := r.URL.Query().Get("period")
	days := parsePeriodDays(period)

	ctx := r.Context()
	since := time.Now().UTC().AddDate(0, 0, -days)

	metrics := OverviewMetrics{}

	// Abandoned carts from synced_checkouts
	var abandoned int64
	h.db.WithContext(ctx).Model(&model.SyncedCheckout{}).
		Where("shop_id = ? AND created_at >= ? AND status = ?", shopID, since, "abandoned").
		Count(&abandoned)

	// Emails sent from cart_recovery_sends
	h.db.WithContext(ctx).Model(&model.CartRecoverySend{}).
		Where("shop_id = ? AND sent_at >= ? AND status IN ?", shopID, since, []string{"sent", "opened"}).
		Count(&metrics.EmailsSent)

	// Orders recovered: count of synced_checkouts with status='recovered'
	h.db.WithContext(ctx).Model(&model.SyncedCheckout{}).
		Where("shop_id = ? AND created_at >= ? AND status = ?", shopID, since, "recovered").
		Count(&metrics.OrdersRecovered)

	// Revenue recovered: sum of order_total from cart_recovery_codes where is_used=true
	h.db.WithContext(ctx).Model(&model.CartRecoveryCode{}).
		Where("shop_id = ? AND created_at >= ? AND is_used = ?", shopID, since, true).
		Select("COALESCE(SUM(order_total), 0)").
		Scan(&metrics.RevenueRecovered)

	// Recovery rate: recovered orders / abandoned carts
	if abandoned > 0 {
		metrics.RecoveryRate = float64(metrics.OrdersRecovered) / float64(abandoned) * 100.0
	}

	metrics.TotalAbandoned = abandoned

	httputil.WriteOK(w, metrics)
}

// ── Funnel ────────────────────────────────────────────────────────────────────

// FunnelResp is the flat funnel response matching frontend expectations.
type FunnelResp struct {
	Abandoned int64 `json:"abandoned"`
	Sent      int64 `json:"sent"`
	Opened    int64 `json:"opened"`
	Used      int64 `json:"used"`
	Recovered int64 `json:"recovered"`
}

// Funnel handles GET /api/cart-recovery/analytics/funnel.
func (h *CartRecoveryAnalyticsHandler) Funnel(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	shopID, err := resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	period := r.URL.Query().Get("period")
	days := parsePeriodDays(period)
	ctx := r.Context()
	since := time.Now().UTC().AddDate(0, 0, -days)

	var abandoned, sent, opened, used, recovered int64

	// Stage 1: Abandoned carts
	h.db.WithContext(ctx).Model(&model.SyncedCheckout{}).
		Where("shop_id = ? AND created_at >= ? AND status = ?", shopID, since, "abandoned").
		Count(&abandoned)

	// Stage 2: Emails sent
	h.db.WithContext(ctx).Model(&model.CartRecoverySend{}).
		Where("shop_id = ? AND sent_at >= ?", shopID, since).
		Count(&sent)

	// Stage 3: Opened
	h.db.WithContext(ctx).Model(&model.CartRecoverySend{}).
		Where("shop_id = ? AND sent_at >= ? AND status = ?", shopID, since, "opened").
		Count(&opened)

	// Stage 4: Codes used
	h.db.WithContext(ctx).Model(&model.CartRecoveryCode{}).
		Where("shop_id = ? AND created_at >= ? AND is_used = ?", shopID, since, true).
		Count(&used)

	// Stage 5: Orders recovered
	h.db.WithContext(ctx).Model(&model.SyncedCheckout{}).
		Where("shop_id = ? AND created_at >= ? AND status = ?", shopID, since, "recovered").
		Count(&recovered)

	resp := FunnelResp{
		Abandoned: abandoned,
		Sent:      sent,
		Opened:    opened,
		Used:      used,
		Recovered: recovered,
	}

	httputil.WriteOK(w, resp)
}

// ── Campaigns Comparison ──────────────────────────────────────────────────────

// CampaignStats holds per-campaign analytics data matching frontend expectations.
type CampaignStats struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Revenue float64 `json:"revenue"`
	Orders  int64   `json:"orders"`
	Rate    float64 `json:"rate"`
	Sent    int64   `json:"sent"`
}

// CampaignsComparison handles GET /api/cart-recovery/analytics/campaigns.
func (h *CartRecoveryAnalyticsHandler) CampaignsComparison(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	shopID, err := resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	period := r.URL.Query().Get("period")
	days := parsePeriodDays(period)
	ctx := r.Context()
	since := time.Now().UTC().AddDate(0, 0, -days)

	// Fetch all campaigns for this shop
	var campaigns []model.CartRecoveryCampaign
	if err := h.db.WithContext(ctx).
		Where("shop_id = ?", shopID).
		Find(&campaigns).Error; err != nil {
		slog.Error("failed to fetch campaigns for comparison", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to fetch campaigns")
		return
	}

	// Build per-campaign stats
	statsMap := make(map[uuid.UUID]*CampaignStats, len(campaigns))

	for _, c := range campaigns {
		var codesSent, codesUsed int64
		var revenueRecovered float64

		// Codes generated for this campaign
		h.db.WithContext(ctx).Model(&model.CartRecoveryCode{}).
			Where("shop_id = ? AND campaign_id = ? AND created_at >= ?", shopID, c.ID, since).
			Count(&codesSent)

		// Codes used for this campaign
		h.db.WithContext(ctx).Model(&model.CartRecoveryCode{}).
			Where("shop_id = ? AND campaign_id = ? AND created_at >= ? AND is_used = ?", shopID, c.ID, since, true).
			Count(&codesUsed)

		// Revenue recovered via codes of this campaign
		h.db.WithContext(ctx).Model(&model.CartRecoveryCode{}).
			Where("shop_id = ? AND campaign_id = ? AND created_at >= ? AND is_used = ?", shopID, c.ID, since, true).
			Select("COALESCE(SUM(order_total), 0)").
			Scan(&revenueRecovered)

		recRate := 0.0
		if codesSent > 0 {
			recRate = float64(codesUsed) / float64(codesSent) * 100.0
		}

		statsMap[c.ID] = &CampaignStats{
			ID:      c.ID.String(),
			Name:    c.Name,
			Revenue: revenueRecovered,
			Orders:  codesUsed,
			Rate:    recRate,
			Sent:    codesSent,
		}
	}

	// Build response array
	result := make([]CampaignStats, 0, len(campaigns))
	for _, c := range campaigns {
		if s, ok := statsMap[c.ID]; ok {
			result = append(result, *s)
		}
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success":   true,
		"campaigns": result,
		"period":    period,
	})
}

// ── Trend ─────────────────────────────────────────────────────────────────────

// CartRecoveryTrendPoint is a single daily data point for cart recovery trends.
type CartRecoveryTrendPoint struct {
	Date    string  `json:"date"`
	Orders  int64   `json:"orders"`
	Revenue float64 `json:"revenue"`
}

// Trend handles GET /api/cart-recovery/analytics/trend.
func (h *CartRecoveryAnalyticsHandler) Trend(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	shopID, err := resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	period := r.URL.Query().Get("period")
	days := parsePeriodDays(period)
	ctx := r.Context()
	since := time.Now().UTC().AddDate(0, 0, -days)

	// Daily recovered orders
	type dailyRecovered struct {
		Date  string
		Count int64
	}
	var recoveredRows []dailyRecovered
	h.db.WithContext(ctx).Model(&model.SyncedCheckout{}).
		Select("DATE(created_at) as date, COUNT(*) as count").
		Where("shop_id = ? AND created_at >= ? AND status = ?", shopID, since, "recovered").
		Group("DATE(created_at)").
		Order("date ASC").
		Scan(&recoveredRows)

	// Daily revenue from used codes
	type dailyRevenue struct {
		Date    string
		Revenue float64
	}
	var revenueRows []dailyRevenue
	h.db.WithContext(ctx).Model(&model.CartRecoveryCode{}).
		Select("DATE(created_at) as date, COALESCE(SUM(order_total), 0) as revenue").
		Where("shop_id = ? AND created_at >= ? AND is_used = ?", shopID, since, true).
		Group("DATE(created_at)").
		Order("date ASC").
		Scan(&revenueRows)

	// Build date-indexed maps
	recoveredMap := make(map[string]int64, len(recoveredRows))
	for _, r := range recoveredRows {
		recoveredMap[r.Date] = r.Count
	}
	revenueMap := make(map[string]float64, len(revenueRows))
	for _, r := range revenueRows {
		revenueMap[r.Date] = r.Revenue
	}

	// Generate all dates in range
	daily := make([]CartRecoveryTrendPoint, 0, days)
	for i := days - 1; i >= 0; i-- {
		date := time.Now().UTC().AddDate(0, 0, -i).Format("2006-01-02")
		daily = append(daily, CartRecoveryTrendPoint{
			Date:    date,
			Orders:  recoveredMap[date],
			Revenue: revenueMap[date],
		})
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"daily":   daily,
		"period":  period,
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// parsePeriodDays converts a period string like "7d", "30d", "90d" to days.
// Defaults to 30 days.
func parsePeriodDays(period string) int {
	s := strings.TrimSuffix(period, "d")
	days, err := strconv.Atoi(s)
	if err != nil || days <= 0 {
		return 30
	}
	return days
}

// ratePct computes a percentage rate: (numerator / denominator) * 100.
// Returns 0 if denominator is 0.
func ratePct(num, denom int64) float64 {
	if denom == 0 {
		return 0
	}
	return float64(num) / float64(denom) * 100.0
}

// resolveShopID extracts and validates the ShopID from the request.
// Priority: context (set by gateway middleware) > X-Shop-ID header > shop_id query param.
func resolveShopID(r *http.Request) (uuid.UUID, error) {
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
