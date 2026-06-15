package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/insight"
	"github.com/JingxuanC/vela-engine/internal/service/analytics"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"gorm.io/gorm"
)

type AnalyticsHandler struct {
	db        *gorm.DB
	engine    *analytics.AttributionEngine
	predictor *analytics.PredictiveModel
	snapshots *insight.SnapshotStore
}

func NewAnalyticsHandler(db *gorm.DB, ae *analytics.AttributionEngine, pm *analytics.PredictiveModel, ss *insight.SnapshotStore) *AnalyticsHandler {
	return &AnalyticsHandler{db, ae, pm, ss}
}
func (h *AnalyticsHandler) TrackEvent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShopID     string `json:"shop_id"`
		CustomerID string `json:"customer_id"`
		EventType  string `json:"event_type"`
		Channel    string `json:"channel"`
		Source     string `json:"source"`
		Medium     string `json:"medium"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.ShopID == "" {
		httputil.WriteError(w, 400, "shop_id required")
		return
	}
	if req.Channel == "" {
		req.Channel = "direct"
	}
	sid, err := uuid.Parse(req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid shop_id")
		return
	}
	if err := h.db.Create(&model.AnalyticsEvent{ShopID: sid, CustomerID: req.CustomerID, EventType: req.EventType, Channel: req.Channel, Source: req.Source, Medium: req.Medium}).Error; err != nil {
		slog.Error("analytics: failed to track event", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "failed to record event")
		return
	}
	httputil.WriteJSON(w, 201, map[string]interface{}{"success": true})
}
func (h *AnalyticsHandler) GetAttribution(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("shop_id")
	m := r.URL.Query().Get("model")
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	cached := r.URL.Query().Get("cached") != "false"
	if sid == "" {
		httputil.WriteError(w, 400, "shop_id required")
		return
	}
	if m == "" {
		m = "first_touch"
	}
	if days <= 0 {
		days = 30
	}

	// Try cached result first
	if cached {
		if rpt, err := h.engine.GetAttribution(r.Context(), sid); err == nil && rpt != nil {
			httputil.WriteOK(w, map[string]interface{}{
				"success":         true,
				"total_revenue":   rpt.TotalRevenue,
				"total_orders":    rpt.TotalOrders,
				"channel_summary": rpt.ChannelSummary,
				"cached":          true,
			})
			return
		}
	}

	rpt, err := h.engine.ComputeAttribution(r.Context(), sid, m, days)
	if err != nil || rpt == nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to compute attribution")
		return
	}
	httputil.WriteOK(w, map[string]interface{}{
		"success":         true,
		"total_revenue":   rpt.TotalRevenue,
		"total_orders":    rpt.TotalOrders,
		"channel_summary": rpt.ChannelSummary,
		"cached":          false,
	})
}
func (h *AnalyticsHandler) ComputeAttribution(w http.ResponseWriter, r *http.Request) {
	h.GetAttribution(w, r)
}
func (h *AnalyticsHandler) ChurnPredictions(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("shop_id")
	lim, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if lim < 1 || lim > 100 {
		lim = 50
	}
	if off < 0 {
		off = 0
	}
	scores, total, _ := h.predictor.GetPredictions(sid, "", lim, off)
	items := make([]map[string]interface{}, len(scores))
	for i, s := range scores {
		items[i] = map[string]interface{}{"id": s.ID, "customer_id": s.CustomerID, "churn_score": s.ChurnScore, "risk": s.ChurnRiskLevel}
	}
	httputil.WriteOK(w, map[string]interface{}{"success": true, "predictions": items, "total": total})
}

// ChurnRisks returns detailed churn risk records from customer_churn_risks,
// including top_factors (why the customer is at risk) and behavioral trends.
func (h *AnalyticsHandler) ChurnRisks(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("shop_id")
	riskLevel := r.URL.Query().Get("risk")
	lim, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if lim < 1 || lim > 100 {
		lim = 50
	}
	if off < 0 {
		off = 0
	}

	risks, total, _ := h.predictor.GetChurnRisks(r.Context(), sid, riskLevel, lim, off)
	items := make([]map[string]interface{}, len(risks))
	for i, r := range risks {
		var factors []string
		json.Unmarshal(r.TopFactors, &factors)
		items[i] = map[string]interface{}{
			"id":               r.ID,
			"customer_id":      r.CustomerID,
			"churn_score":      r.ChurnScore,
			"risk_level":       r.RiskLevel,
			"last_order_days":  r.LastOrderDays,
			"order_count_trend": r.OrderCountTrend,
			"sentiment_trend":  r.SentimentTrend,
			"top_factors":      factors,
			"computed_at":      r.ComputedAt,
		}
	}
	httputil.WriteOK(w, map[string]interface{}{"success": true, "risks": items, "total": total})
}
func (h *AnalyticsHandler) LTVPredictions(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("shop_id")
	lim, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if lim < 1 || lim > 100 {
		lim = 50
	}
	if off < 0 {
		off = 0
	}
	scores, total, _ := h.predictor.GetPredictions(sid, "", lim, off)
	items := make([]map[string]interface{}, len(scores))
	for i, s := range scores {
		items[i] = map[string]interface{}{"id": s.ID, "customer_id": s.CustomerID, "ltv": s.PredictedLTV}
	}
	httputil.WriteOK(w, map[string]interface{}{"success": true, "predictions": items, "total": total})
}
func (h *AnalyticsHandler) Events(w http.ResponseWriter, r *http.Request) {
	httputil.WriteOK(w, map[string]interface{}{"success": true, "events": []interface{}{}})
}

// ============================================================================
// Phase 1: New endpoints
// ============================================================================

// TrackEventRequest is the request body for POST /api/analytics/events/track.
type TrackEventRequest struct {
	ShopID     string `json:"shop_id"`
	EventType  string `json:"event_type"`
	CustomerID string `json:"customer_id,omitempty"`
	Channel    string `json:"channel,omitempty"`
	Source     string `json:"source,omitempty"`
	Medium     string `json:"medium,omitempty"`
	Metadata   string `json:"metadata,omitempty"` // JSON string for extra context
}

// TrackEventResponse holds the response for event tracking.
type TrackEventResponse struct {
	Success bool   `json:"success"`
	EventID string `json:"event_id,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Track handles POST /api/analytics/events/track — AI interaction event ingestion via EventBus.
func (h *AnalyticsHandler) Track(w http.ResponseWriter, r *http.Request) {
	var req TrackEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.ShopID == "" || req.EventType == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id and event_type are required")
		return
	}
	if req.Channel == "" {
		req.Channel = "direct"
	}

	sid, err := uuid.Parse(req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id format")
		return
	}

	// Persist to DB
	h.db.Create(&model.AnalyticsEvent{
		ShopID:     sid,
		CustomerID: req.CustomerID,
		EventType:  req.EventType,
		Channel:    req.Channel,
		Source:     req.Source,
		Medium:     req.Medium,
	})

	httputil.WriteJSON(w, http.StatusCreated, TrackEventResponse{
		Success: true,
	})
}

// AnalyticsSalesTrendPoint represents a single data point on the sales trend chart.
type AnalyticsSalesTrendPoint struct {
	Date  string  `json:"date"`
	Sales float64 `json:"sales"`
	Orders int    `json:"orders"`
}

// AnalyticsReturnReasonStat represents a return reason with its count and percentage.
type AnalyticsReturnReasonStat struct {
	Reason     string  `json:"reason"`
	Count      int     `json:"count"`
	Percentage float64 `json:"percentage"`
}

// DashboardMetric represents a single KPI on the analytics dashboard.
type DashboardMetric struct {
	Label    string  `json:"label"`
	Value    float64 `json:"value"`
	Change   float64 `json:"change"`   // percentage change vs previous period
	Trend    string  `json:"trend"`    // up, down, flat
	Unit     string  `json:"unit"`     // %, $, count
}

// InventoryStatusItem represents stock status for a product or category.
type InventoryStatusItem struct {
	ProductID   string  `json:"product_id"`
	ProductName string  `json:"product_name"`
	StockLevel  int     `json:"stock_level"`
	ReorderAt   int     `json:"reorder_at"`
	Status      string  `json:"status"` // healthy, low, out_of_stock
}

// DashboardResponse holds the full analytics dashboard payload.
type DashboardResponse struct {
	Success             bool                   `json:"success"`
	ReturnRate          float64                `json:"return_rate"`
	ReturnRateChange    float64                `json:"return_rate_change"`
	TotalInventory      int                    `json:"total_inventory"`
	LowStockItems       int                    `json:"low_stock_items"`
	OutOfStockItems     int                    `json:"out_of_stock_items"`
	SalesTrend          []AnalyticsSalesTrendPoint      `json:"sales_trend"`
	TopReturnReasons    []AnalyticsReturnReasonStat     `json:"top_return_reasons"`
	InventoryBreakdown  []InventoryStatusItem  `json:"inventory_breakdown"`
	Metrics             []DashboardMetric      `json:"metrics"`
	Period              string                 `json:"period"`
}

// Dashboard handles GET /api/analytics/dashboard.
// Returns real data from SnapshotStore (cache) and DB queries for the given period.
func (h *AnalyticsHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "30d"
	}
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	ctx := r.Context()

	// Parse period: "30d" → 30 days, "7d" → 7 days, etc.
	days := 30
	if d, err := strconv.Atoi(strings.TrimSuffix(period, "d")); err == nil && d > 0 {
		days = d
	}

	resp := DashboardResponse{
		Success:             true,
		ReturnRate:          0,
		ReturnRateChange:    0,
		TotalInventory:      0,
		LowStockItems:       0,
		OutOfStockItems:     0,
		SalesTrend:          []AnalyticsSalesTrendPoint{},
		TopReturnReasons:    []AnalyticsReturnReasonStat{},
		InventoryBreakdown:  []InventoryStatusItem{},
		Metrics:             []DashboardMetric{},
		Period:              period,
	}

	// ── 1. Return metrics from SnapshotStore ──
	if h.db != nil {
		if h.snapshots != nil {
			snap := h.snapshots.GetOrRefresh(ctx, shopID)
			if snap != nil {
				resp.ReturnRate = snap.ReturnRate
				resp.ReturnRateChange = 0 // computed below

				// Build top_return_reasons from snapshot (simple string list)
				totalReturnCount := snap.ReturnCount
				for _, r := range snap.TopReturnReasons {
					stat := AnalyticsReturnReasonStat{
						Reason: r,
						Count:  0, // snapshot doesn't store per-reason counts
					}
					if totalReturnCount > 0 {
						stat.Percentage = 100.0 / float64(len(snap.TopReturnReasons))
					}
					resp.TopReturnReasons = append(resp.TopReturnReasons, stat)
				}
			}
		}

		// ── 2. Sales trend from synced_orders (daily aggregation) ──
		since := time.Now().UTC().AddDate(0, 0, -days)
		type dailySales struct {
			Date   string
			Sales  float64
			Orders int
		}
		var rows []dailySales
		h.db.WithContext(ctx).Table("synced_orders").
			Select("DATE(created_at) as date, SUM(total_price) as sales, COUNT(*) as orders").
			Where("shop_id = ? AND created_at >= ?", shopID, since).
			Group("DATE(created_at)").
			Order("date ASC").
			Scan(&rows)
		for _, r := range rows {
			resp.SalesTrend = append(resp.SalesTrend, AnalyticsSalesTrendPoint{
				Date:   r.Date,
				Sales:  r.Sales,
				Orders: r.Orders,
			})
		}

		// ── 3. Inventory breakdown from synced_products ──
		type invRow struct {
			ProductID     string
			TotalStock    int
			ProductName   string
		}
		var invRows []invRow
		h.db.WithContext(ctx).Table("synced_products").
			Select("platform_id as product_id, title as product_name, (variants->0->>'inventory_quantity')::int as total_stock").
			Where("shop_id = ? AND status = ?", shopID, "active").
			Limit(100).
			Scan(&invRows)

		lowStockCount := 0
		outOfStockCount := 0
		totalInventory := 0
		for _, r := range invRows {
			status := "healthy"
			if r.TotalStock <= 0 {
				status = "out_of_stock"
				outOfStockCount++
			} else if r.TotalStock <= 5 {
				status = "low"
				lowStockCount++
			}
			totalInventory += r.TotalStock
			resp.InventoryBreakdown = append(resp.InventoryBreakdown, InventoryStatusItem{
				ProductID:   r.ProductID,
				ProductName: r.ProductName,
				StockLevel:  r.TotalStock,
				ReorderAt:   10,
				Status:      status,
			})
		}
		resp.TotalInventory = totalInventory
		resp.LowStockItems = lowStockCount
		resp.OutOfStockItems = outOfStockCount
	}

	// ── 4. Return rate change (compare current period to previous period) ──
	if h.db != nil {
		prevSince := time.Now().UTC().AddDate(0, 0, -2*days)
		prevEnd := time.Now().UTC().AddDate(0, 0, -days)
		var prevOrderCount, prevReturnCount int64
		h.db.WithContext(ctx).Table("synced_orders").
			Where("shop_id = ? AND created_at >= ? AND created_at < ?", shopID, prevSince, prevEnd).
			Count(&prevOrderCount)
		h.db.WithContext(ctx).Table("returns").
			Where("shop_id = ? AND created_at >= ? AND created_at < ?", shopID, prevSince, prevEnd).
			Count(&prevReturnCount)

		if prevOrderCount > 0 {
			prevRate := float64(prevReturnCount) / float64(prevOrderCount) * 100
			if prevRate > 0 {
				resp.ReturnRateChange = ((resp.ReturnRate - prevRate) / prevRate) * 100
			}
		}
	}

	// ── 5. KPI metrics summary ──
	resp.Metrics = []DashboardMetric{
		{
			Label: "Return Rate",
			Value: resp.ReturnRate,
			Change: resp.ReturnRateChange,
			Trend: trendLabel(resp.ReturnRateChange),
			Unit:  "%",
		},
		{
			Label: "Total Revenue (30d)",
			Value: totalSalesFromTrend(resp.SalesTrend),
			Change: 0,
			Trend: "flat",
			Unit:  "$",
		},
		{
			Label:  "Total Inventory",
			Value:  float64(resp.TotalInventory),
			Change: 0,
			Trend:  "flat",
			Unit:   "count",
		},
		{
			Label: "Total Orders (30d)",
			Value: float64(totalOrdersFromTrend(resp.SalesTrend)),
			Change: 0,
			Trend: "flat",
			Unit:  "count",
		},
	}

	httputil.WriteOK(w, resp)
}

// trendLabel returns "up", "down", or "flat" based on the percentage change.
func trendLabel(changePct float64) string {
	if changePct > 5 {
		return "up"
	} else if changePct < -5 {
		return "down"
	}
	return "flat"
}

// totalSalesFromTrend sums up sales from trend data.
func totalSalesFromTrend(points []AnalyticsSalesTrendPoint) float64 {
	var total float64
	for _, p := range points {
		total += p.Sales
	}
	return total
}

// totalOrdersFromTrend sums up orders from trend data.
func totalOrdersFromTrend(points []AnalyticsSalesTrendPoint) int {
	var total int
	for _, p := range points {
		total += p.Orders
	}
	return total
}
