package handler

import (
	"net/http"
	"strconv"
	"time"

	chi "github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// ContentAnalyticsHandler provides dashboard API endpoints for the Content Factory.
type ContentAnalyticsHandler struct {
	db *gorm.DB
}

// NewContentAnalyticsHandler creates a new ContentAnalyticsHandler.
func NewContentAnalyticsHandler(db *gorm.DB) *ContentAnalyticsHandler {
	return &ContentAnalyticsHandler{db: db}
}

// ─────────────────────────────────────────────────────────────────────────────
// 1. Content Performance Overview
// GET /api/analytics/content-performance?days=30
// ─────────────────────────────────────────────────────────────────────────────

// TypeRevenueEntry holds per-content-type revenue.
type TypeRevenueEntry struct {
	ContentType string  `json:"content_type"`
	Revenue     float64 `json:"revenue"`
}

// TopContentOverviewEntry is a lightweight top-content entry for the overview.
type TopContentOverviewEntry struct {
	Title       string  `json:"title"`
	ContentType string  `json:"content_type"`
	Platform    string  `json:"platform"`
	Revenue     float64 `json:"revenue"`
	Orders      int     `json:"orders"`
}

// ContentPerformanceOverviewResp holds the aggregated content performance metrics.
type ContentPerformanceOverviewResp struct {
	Success        bool                     `json:"success"`
	TotalRevenue   float64                  `json:"total_revenue"`
	TotalOrders    int                      `json:"total_orders"`
	GeneratedCount int64                    `json:"generated_count"`
	PublishedCount int64                    `json:"published_count"`
	ByType         []TypeRevenueEntry       `json:"by_type"`
	TopContent     []TopContentOverviewEntry `json:"top_content"`
	Days           int                      `json:"days"`
}

// ContentPerformanceOverview returns an aggregated overview of content-driven metrics.
// Queries ContentPerformance WHERE shop_id=? AND last_attribution_at >= cutoff
// (NOT updated_at — the attribution window is what matters).
func (h *ContentAnalyticsHandler) ContentPerformanceOverview(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, 503, "DB unavailable")
		return
	}

	sid := r.URL.Query().Get("shop_id")
	if sid == "" {
		httputil.WriteError(w, 400, "shop_id is required")
		return
	}
	shopID, err := uuid.Parse(sid)
	if err != nil {
		httputil.WriteError(w, 400, "invalid shop_id: must be UUID")
		return
	}

	days := 30
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d > 0 && d <= 365 {
		days = d
	}

	resp := ContentPerformanceOverviewResp{
		Success: true,
		ByType:  make([]TypeRevenueEntry, 0),
		Days:    days,
	}

	ctx := r.Context()
	cutoff := time.Now().UTC().AddDate(0, 0, -days)

	// ── Aggregated attributions (last_attribution_at, NOT updated_at) ──
	type aggRow struct {
		Revenue float64
		Orders  int
		Type    string
	}
	var aggRows []aggRow
	h.db.WithContext(ctx).Model(&model.ContentPerformance{}).
		Select("content_type, SUM(attributed_revenue) as revenue, SUM(attributed_orders) as orders").
		Where("shop_id = ? AND last_attribution_at >= ?", shopID, cutoff).
		Group("content_type").
		Scan(&aggRows)

	var totalRev float64
	var totalOrders int
	for _, row := range aggRows {
		resp.ByType = append(resp.ByType, TypeRevenueEntry{
			ContentType: row.Type,
			Revenue:     row.Revenue,
		})
		totalRev += row.Revenue
		totalOrders += row.Orders
	}
	resp.TotalRevenue = totalRev
	resp.TotalOrders = totalOrders

	// ── Content piece counts ──
	var generatedCount int64
	h.db.WithContext(ctx).Model(&model.ContentPiece{}).
		Where("shop_id = ?", shopID).
		Count(&generatedCount)
	resp.GeneratedCount = generatedCount

	var publishedCount int64
	h.db.WithContext(ctx).Model(&model.ContentPiece{}).
		Where("shop_id = ? AND status = ?", shopID, "published").
		Count(&publishedCount)
	resp.PublishedCount = publishedCount

	// ── Top content (merged into overview) ──
	type topRow struct {
		Title       string  `gorm:"column:title"`
		ContentType string  `gorm:"column:content_type"`
		Platform    string  `gorm:"column:platform"`
		Revenue     float64 `gorm:"column:revenue"`
		Orders      int     `gorm:"column:orders"`
	}
	var topRows []topRow
	h.db.WithContext(ctx).Table("content_performances perf").
		Select("cp.title, cp.content_type, cp.platform, perf.attributed_revenue as revenue, perf.attributed_orders as orders").
		Joins("JOIN content_pieces cp ON cp.id = perf.content_piece_id AND cp.shop_id = perf.shop_id").
		Where("perf.shop_id = ? AND perf.last_attribution_at >= ?", shopID, cutoff).
		Order("perf.attributed_revenue DESC").
		Limit(10).
		Scan(&topRows)

	resp.TopContent = make([]TopContentOverviewEntry, 0, len(topRows))
	for _, row := range topRows {
		resp.TopContent = append(resp.TopContent, TopContentOverviewEntry{
			Title:       row.Title,
			ContentType: row.ContentType,
			Platform:    row.Platform,
			Revenue:     row.Revenue,
			Orders:      row.Orders,
		})
	}

	httputil.WriteOK(w, resp)
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. Content Pieces List
// GET /api/content/pieces?type=social&status=published&platform=instagram
// ─────────────────────────────────────────────────────────────────────────────

// ContentPieceWithPerf represents a ContentPiece joined with its performance stats.
type ContentPieceWithPerf struct {
	ID                string  `json:"id"`
	ShortID           string  `json:"short_id"`
	JobID             string  `json:"job_id"`
	ContentType       string  `json:"content_type"`
	Platform          string  `json:"platform,omitempty"`
	ProductID         string  `json:"product_id,omitempty"`
	Title             string  `json:"title,omitempty"`
	Body              string  `json:"body"`
	Tone              string  `json:"tone"`
	Language          string  `json:"language"`
	UTMURL            string  `json:"utm_url,omitempty"`
	Status            string  `json:"status"`
	AttributedRevenue float64 `json:"attributed_revenue,omitempty"`
	AttributedOrders  int     `json:"attributed_orders,omitempty"`
	TotalClicks       int     `json:"total_clicks,omitempty"`
	TotalImpressions  int     `json:"total_impressions,omitempty"`
	CreatedAt         string  `json:"created_at"`
}

// ContentPiecesListResp holds the paginated content pieces response.
type ContentPiecesListResp struct {
	Success bool                   `json:"success"`
	Pieces  []ContentPieceWithPerf `json:"pieces"`
	Total   int64                  `json:"total"`
}

// ContentPiecesList returns a filtered, paginated list of content pieces with
// their attribution stats joined from ContentPerformance.
func (h *ContentAnalyticsHandler) ContentPiecesList(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, 503, "DB unavailable")
		return
	}

	sid := r.URL.Query().Get("shop_id")
	if sid == "" {
		httputil.WriteError(w, 400, "shop_id is required")
		return
	}
	shopID, err := uuid.Parse(sid)
	if err != nil {
		httputil.WriteError(w, 400, "invalid shop_id: must be UUID")
		return
	}

	contentType := r.URL.Query().Get("type")
	status := r.URL.Query().Get("status")
	platform := r.URL.Query().Get("platform")

	limit := 50
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 500 {
		limit = l
	}
	offset := 0
	if o, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && o >= 0 {
		offset = o
	}

	ctx := r.Context()

	// ── Count total ──
	type countResult struct {
		Total int64
	}
	var cnt countResult
	countQuery := h.db.WithContext(ctx).Model(&model.ContentPiece{}).
		Where("shop_id = ?", shopID)
	if contentType != "" {
		countQuery = countQuery.Where("content_type = ?", contentType)
	}
	if status != "" {
		countQuery = countQuery.Where("status = ?", status)
	}
	if platform != "" {
		countQuery = countQuery.Where("platform = ?", platform)
	}
	countQuery.Select("COUNT(*) as total").Scan(&cnt)

	// ── Query pieces with performance joined ──
	type joinRow struct {
		model.ContentPiece
		AttributedRevenue float64 `gorm:"column:attributed_revenue"`
		AttributedOrders  int     `gorm:"column:attributed_orders"`
		TotalClicks       int     `gorm:"column:total_clicks"`
		TotalImpressions  int     `gorm:"column:total_impressions"`
	}

	var rows []joinRow
	q := h.db.WithContext(ctx).Table("content_pieces cp").
		Select(`cp.id, cp.short_id, cp.job_id, cp.content_type, cp.platform, cp.product_id,
			cp.title, cp.body, cp.tone, cp.language, cp.utm_url, cp.status, cp.created_at,
			COALESCE(perf.attributed_revenue, 0) as attributed_revenue,
			COALESCE(perf.attributed_orders, 0) as attributed_orders,
			COALESCE(perf.total_clicks, 0) as total_clicks,
			COALESCE(perf.total_impressions, 0) as total_impressions`).
		Joins("LEFT JOIN content_performances perf ON cp.id = perf.content_piece_id AND perf.shop_id = cp.shop_id").
		Where("cp.shop_id = ?", shopID)

	if contentType != "" {
		q = q.Where("cp.content_type = ?", contentType)
	}
	if status != "" {
		q = q.Where("cp.status = ?", status)
	}
	if platform != "" {
		q = q.Where("cp.platform = ?", platform)
	}

	q = q.Order("cp.created_at DESC").Limit(limit).Offset(offset)
	q.Scan(&rows)

	pieces := make([]ContentPieceWithPerf, 0, len(rows))
	for _, row := range rows {
		pieces = append(pieces, ContentPieceWithPerf{
			ID:                row.ID.String(),
			ShortID:           row.ShortID,
			JobID:             row.JobID.String(),
			ContentType:       row.ContentType,
			Platform:          row.Platform,
			ProductID:         row.ProductID,
			Title:             row.Title,
			Body:              row.Body,
			Tone:              row.Tone,
			Language:          row.Language,
			UTMURL:            row.UTMURL,
			Status:            row.Status,
			AttributedRevenue: row.AttributedRevenue,
			AttributedOrders:  row.AttributedOrders,
			TotalClicks:       row.TotalClicks,
			TotalImpressions:  row.TotalImpressions,
			CreatedAt:         row.CreatedAt.Format(time.RFC3339),
		})
	}

	httputil.WriteOK(w, ContentPiecesListResp{
		Success: true,
		Pieces:  pieces,
		Total:   cnt.Total,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. Single ContentPiece Performance
// GET /api/analytics/content/{pieceID}/performance
// ─────────────────────────────────────────────────────────────────────────────

// ContentPieceDetailResp holds detailed metrics for a single content piece.
type ContentPieceDetailResp struct {
	Success     bool   `json:"success"`
	PieceID     string `json:"piece_id"`
	ShopID      string `json:"shop_id"`

	// Content fields
	ContentType string `json:"content_type"`
	Platform    string `json:"platform,omitempty"`
	ProductID   string `json:"product_id,omitempty"`
	Title       string `json:"title,omitempty"`
	Body        string `json:"body"`
	Tone        string `json:"tone"`
	Language    string `json:"language"`
	UTMURL      string `json:"utm_url,omitempty"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`

	// Performance overview
	AttributedRevenue float64 `json:"attributed_revenue"`
	AttributedOrders  int     `json:"attributed_orders"`
	AttributedUnits   int     `json:"attributed_units"`
	TotalImpressions  int     `json:"total_impressions"`
	TotalClicks       int     `json:"total_clicks"`
	TotalEngagement   int     `json:"total_engagement"`
	LastAttributionAt string  `json:"last_attribution_at,omitempty"`

	// Per-platform metrics
	PlatformMetrics []PlatformMetricEntry `json:"platform_metrics,omitempty"`
}

// PlatformMetricEntry represents a single platform-day metric row.
type PlatformMetricEntry struct {
	Platform       string `json:"platform"`
	MetricsDate    string `json:"metrics_date"`
	Impressions    int    `json:"impressions"`
	Clicks         int    `json:"clicks"`
	Saves          int    `json:"saves"`
	Engagement     int    `json:"engagement"`
	Reach          int    `json:"reach"`
	VideoViews     int    `json:"video_views"`
	PlatformPostID string `json:"platform_post_id,omitempty"`
}

// ContentPiecePerformance returns detailed performance metrics for a single
// content piece, including its body, UTM URL, attribution, and per-platform
// metrics.
func (h *ContentAnalyticsHandler) ContentPiecePerformance(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, 503, "DB unavailable")
		return
	}

	sid := r.URL.Query().Get("shop_id")
	if sid == "" {
		httputil.WriteError(w, 400, "shop_id is required")
		return
	}
	shopID, err := uuid.Parse(sid)
	if err != nil {
		httputil.WriteError(w, 400, "invalid shop_id: must be UUID")
		return
	}

	pieceIDStr := chi.URLParam(r, "pieceID")
	pieceID, err := uuid.Parse(pieceIDStr)
	if err != nil {
		httputil.WriteError(w, 400, "invalid piece_id: must be UUID")
		return
	}

	ctx := r.Context()

	// ── Load the content piece ──
	var piece model.ContentPiece
	if err := h.db.WithContext(ctx).
		Where("id = ? AND shop_id = ?", pieceID, shopID).
		First(&piece).Error; err != nil {
		httputil.WriteError(w, 404, "content piece not found")
		return
	}

	resp := ContentPieceDetailResp{
		Success:       true,
		PieceID:       piece.ID.String(),
		ShopID:        piece.ShopID.String(),
		ContentType:   piece.ContentType,
		Platform:      piece.Platform,
		ProductID:     piece.ProductID,
		Title:         piece.Title,
		Body:          piece.Body,
		Tone:          piece.Tone,
		Language:      piece.Language,
		UTMURL:        piece.UTMURL,
		Status:        piece.Status,
		CreatedAt:     piece.CreatedAt.Format(time.RFC3339),
	}

	// ── Load performance stats ──
	var perf model.ContentPerformance
	if err := h.db.WithContext(ctx).
		Where("content_piece_id = ? AND shop_id = ?", pieceID, shopID).
		First(&perf).Error; err == nil {
		resp.AttributedRevenue = perf.AttributedRevenue
		resp.AttributedOrders = perf.AttributedOrders
		resp.AttributedUnits = perf.AttributedUnits
		resp.TotalImpressions = perf.TotalImpressions
		resp.TotalClicks = perf.TotalClicks
		resp.TotalEngagement = perf.TotalEngagement
		if perf.LastAttributionAt != nil {
			resp.LastAttributionAt = perf.LastAttributionAt.Format(time.RFC3339)
		}
	}

	// ── Load per-platform metrics ──
	var platformMetrics []model.ContentPlatformMetrics
	h.db.WithContext(ctx).
		Where("content_piece_id = ? AND shop_id = ?", pieceID, shopID).
		Order("metrics_date DESC, platform ASC").
		Find(&platformMetrics)

	resp.PlatformMetrics = make([]PlatformMetricEntry, 0, len(platformMetrics))
	for _, pm := range platformMetrics {
		resp.PlatformMetrics = append(resp.PlatformMetrics, PlatformMetricEntry{
			Platform:       pm.Platform,
			MetricsDate:    pm.MetricsDate.Format("2006-01-02"),
			Impressions:    pm.Impressions,
			Clicks:         pm.Clicks,
			Saves:          pm.Saves,
			Engagement:     pm.Engagement,
			Reach:          pm.Reach,
			VideoViews:     pm.VideoViews,
			PlatformPostID: pm.PlatformPostID,
		})
	}

	httputil.WriteOK(w, resp)
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. Top Content Leaderboard
// GET /api/analytics/content/top?limit=10&days=30
// ─────────────────────────────────────────────────────────────────────────────

// TopContentEntry represents a single entry on the content leaderboard.
type TopContentEntry struct {
	PieceID           string  `json:"piece_id"`
	ShortID           string  `json:"short_id"`
	ContentType       string  `json:"content_type"`
	ProductID         string  `json:"product_id,omitempty"`
	Title             string  `json:"title,omitempty"`
	Body              string  `json:"body,omitempty"`
	Status            string  `json:"status"`
	AttributedRevenue float64 `json:"attributed_revenue"`
	AttributedOrders  int     `json:"attributed_orders"`
	TotalClicks       int     `json:"total_clicks"`
	CreatedAt         string  `json:"created_at"`
}

// TopContentResp holds the leaderboard response.
type TopContentResp struct {
	Success bool              `json:"success"`
	Top     []TopContentEntry `json:"top"`
	Days    int               `json:"days"`
}

// TopContent returns the top N content pieces ranked by attributed_revenue.
func (h *ContentAnalyticsHandler) TopContent(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, 503, "DB unavailable")
		return
	}

	sid := r.URL.Query().Get("shop_id")
	if sid == "" {
		httputil.WriteError(w, 400, "shop_id is required")
		return
	}
	shopID, err := uuid.Parse(sid)
	if err != nil {
		httputil.WriteError(w, 400, "invalid shop_id: must be UUID")
		return
	}

	limit := 10
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 100 {
		limit = l
	}

	days := 30
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d > 0 && d <= 365 {
		days = d
	}

	ctx := r.Context()
	cutoff := time.Now().UTC().AddDate(0, 0, -days)

	type topRow struct {
		PieceID           string  `gorm:"column:piece_id"`
		ShortID           string  `gorm:"column:short_id"`
		ContentType       string  `gorm:"column:content_type"`
		ProductID         string  `gorm:"column:product_id"`
		Title             string  `gorm:"column:title"`
		Body              string  `gorm:"column:body"`
		Status            string  `gorm:"column:status"`
		CreatedAt         string  `gorm:"column:created_at"`
		AttributedRevenue float64 `gorm:"column:attributed_revenue"`
		AttributedOrders  int     `gorm:"column:attributed_orders"`
		TotalClicks       int     `gorm:"column:total_clicks"`
	}

	var rows []topRow
	h.db.WithContext(ctx).Table("content_performances perf").
		Select(`cp.id as piece_id, cp.short_id, cp.content_type, cp.product_id,
			cp.title, cp.body, cp.status,
			cp.created_at::text as created_at,
			perf.attributed_revenue, perf.attributed_orders, perf.total_clicks`).
		Joins("JOIN content_pieces cp ON cp.id = perf.content_piece_id AND cp.shop_id = perf.shop_id").
		Where("perf.shop_id = ? AND perf.last_attribution_at >= ?", shopID, cutoff).
		Order("perf.attributed_revenue DESC").
		Limit(limit).
		Scan(&rows)

	top := make([]TopContentEntry, 0, len(rows))
	for _, row := range rows {
		top = append(top, TopContentEntry{
			PieceID:           row.PieceID,
			ShortID:           row.ShortID,
			ContentType:       row.ContentType,
			ProductID:         row.ProductID,
			Title:             row.Title,
			Body:              row.Body,
			Status:            row.Status,
			AttributedRevenue: row.AttributedRevenue,
			AttributedOrders:  row.AttributedOrders,
			TotalClicks:       row.TotalClicks,
			CreatedAt:         row.CreatedAt,
		})
	}

	httputil.WriteOK(w, TopContentResp{
		Success: true,
		Top:     top,
		Days:    days,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. Platform Breakdown
// GET /api/analytics/content/platforms
// ─────────────────────────────────────────────────────────────────────────────

// PlatformBreakdownEntry represents aggregated metrics for one platform.
type PlatformBreakdownEntry struct {
	Platform string  `json:"platform"`
	Revenue  float64 `json:"revenue"`
	Orders   int     `json:"orders"`
	Clicks   int     `json:"clicks"`
}

// PlatformBreakdownResp holds the platform breakdown response.
type PlatformBreakdownResp struct {
	Success   bool                    `json:"success"`
	Platforms []PlatformBreakdownEntry `json:"platforms"`
}

// PlatformBreakdown returns revenue, orders, and clicks grouped by platform
// from ContentPlatformMetrics.
func (h *ContentAnalyticsHandler) PlatformBreakdown(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, 503, "DB unavailable")
		return
	}

	sid := r.URL.Query().Get("shop_id")
	if sid == "" {
		httputil.WriteError(w, 400, "shop_id is required")
		return
	}
	shopID, err := uuid.Parse(sid)
	if err != nil {
		httputil.WriteError(w, 400, "invalid shop_id: must be UUID")
		return
	}

	ctx := r.Context()

	type platformRow struct {
		Platform string `gorm:"column:platform"`
		Clicks   int    `gorm:"column:clicks"`
	}

	var rows []platformRow
	h.db.WithContext(ctx).Model(&model.ContentPlatformMetrics{}).
		Select("platform, SUM(clicks) as clicks").
		Where("shop_id = ?", shopID).
		Group("platform").
		Order("clicks DESC").
		Scan(&rows)

	// We also need revenue/orders by platform.  ContentPlatformMetrics has
	// no revenue column, so we derive it from ContentPerformance joined by
	// content_piece_id.  We do this in a second query: for each platform
	// the piece was published on, sum the attributed revenue.
	type platformRevRow struct {
		Platform string  `gorm:"column:platform"`
		Revenue  float64 `gorm:"column:revenue"`
		Orders   int     `gorm:"column:orders"`
	}

	var revRows []platformRevRow
	h.db.WithContext(ctx).Table("content_pieces cp").
		Select("cp.platform, COALESCE(SUM(perf.attributed_revenue), 0) as revenue, COALESCE(SUM(perf.attributed_orders), 0) as orders").
		Joins("JOIN content_performances perf ON perf.content_piece_id = cp.id AND perf.shop_id = cp.shop_id").
		Where("cp.shop_id = ? AND cp.platform != ''", shopID).
		Group("cp.platform").
		Scan(&revRows)

	// Build a map for revenue/orders by platform
	revMap := make(map[string]struct{ revenue float64; orders int })
	for _, r := range revRows {
		revMap[r.Platform] = struct{ revenue float64; orders int }{r.Revenue, r.Orders}
	}

	// Also include platforms that only have clicks
	seenPlatforms := make(map[string]bool)
	platforms := make([]PlatformBreakdownEntry, 0, len(rows)+len(revRows))

	for _, row := range rows {
		entry := PlatformBreakdownEntry{
			Platform: row.Platform,
			Clicks:   row.Clicks,
		}
		if rv, ok := revMap[row.Platform]; ok {
			entry.Revenue = rv.revenue
			entry.Orders = rv.orders
		}
		platforms = append(platforms, entry)
		seenPlatforms[row.Platform] = true
	}

	// Add any platforms that have revenue/orders but no clicks
	for _, r := range revRows {
		if !seenPlatforms[r.Platform] {
			platforms = append(platforms, PlatformBreakdownEntry{
				Platform: r.Platform,
				Revenue:  r.Revenue,
				Orders:   r.Orders,
			})
		}
	}

	httputil.WriteOK(w, PlatformBreakdownResp{
		Success:   true,
		Platforms: platforms,
	})
}
