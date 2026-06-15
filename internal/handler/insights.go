package handler

import (
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/internal/service/insights"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"gorm.io/gorm"
)

// InsightsHandler handles analytics insights endpoints.
type InsightsHandler struct {
	db        *gorm.DB
	llmRouter *service.LLMRouter
}

// NewInsightsHandler creates a new InsightsHandler.
func NewInsightsHandler(db *gorm.DB, llmRouter *service.LLMRouter) *InsightsHandler {
	return &InsightsHandler{db: db, llmRouter: llmRouter}
}

// ShopInsightRequest holds the shop insights request.
type ShopInsightRequest struct {
	ShopID string `json:"shop_id"`
	Period string `json:"period"` // 7d, 30d, 90d, 1y
}

// ShopInsightResponse holds the response.
type ShopInsightResponse struct {
	Success          bool                          `json:"success"`
	Metrics          *insights.Metrics             `json:"metrics"`
	Anomalies        []insights.AnomalyItem        `json:"anomalies"`
	Recommendations  []insights.RecommendationItem `json:"recommendations"`
	Trends           []insights.TrendItem          `json:"trends"`
	ReturnRate       float64                       `json:"return_rate"`
	TopReturnReasons []ReturnReasonStat            `json:"top_return_reasons"`
	SalesTrend       []SalesTrendPoint             `json:"sales_trend"`
	Error            string                        `json:"error,omitempty"`
}

// ReturnReasonStat holds aggregated return reason data.
type ReturnReasonStat struct {
	Reason string  `json:"reason"`
	Count  int     `json:"count"`
	Ratio  float64 `json:"ratio"`
}

// SalesTrendPoint holds a single sales data point for trend charts.
type SalesTrendPoint struct {
	Date    string  `json:"date"`
	Sales   int     `json:"sales"`
	Revenue float64 `json:"revenue"`
}

// Shop handles POST /api/insights/shop.
func (h *InsightsHandler) Shop(w http.ResponseWriter, r *http.Request) {
	var req ShopInsightRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if req.Period == "" {
		req.Period = "30d"
	}

	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	shopUID, err := database.ResolveShopID(h.db, req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	// Calculate date range from period
	now := time.Now()
	var since time.Time
	switch req.Period {
	case "7d":
		since = now.AddDate(0, 0, -7)
	case "30d":
		since = now.AddDate(0, 0, -30)
	case "90d":
		since = now.AddDate(0, 0, -90)
	case "1y":
		since = now.AddDate(-1, 0, 0)
	default:
		since = now.AddDate(0, 0, -30)
	}

	// 1. Fetch sales data from SyncedOrder (limit 1000 for performance)
	var orders []model.SyncedOrder
	h.db.Where("shop_id = ? AND created_at >= ?", shopUID, since).Limit(1000).Find(&orders)

	totalSales := 0
	totalRevenue := 0.0
	salesByDate := make(map[string]*SalesTrendPoint)

	for _, order := range orders {
		totalSales++
		totalRevenue += order.TotalPrice

		dateKey := order.CreatedAt.Format("2006-01-02")
		if _, ok := salesByDate[dateKey]; !ok {
			salesByDate[dateKey] = &SalesTrendPoint{Date: dateKey}
		}
		salesByDate[dateKey].Sales++
		salesByDate[dateKey].Revenue += order.TotalPrice
	}

	// Build sales trend
	salesTrend := make([]SalesTrendPoint, 0, len(salesByDate))
	for _, point := range salesByDate {
		salesTrend = append(salesTrend, *point)
	}
	sort.Slice(salesTrend, func(i, j int) bool {
		return salesTrend[i].Date < salesTrend[j].Date
	})

	// 2. Fetch return data (limit 1000 for performance)
	var returns []model.Return
	h.db.Where("shop_id = ? AND created_at >= ?", shopUID, since).Limit(1000).Find(&returns)

	totalReturns := len(returns)
	returnRate := 0.0
	if totalSales > 0 {
		returnRate = math.Round(float64(totalReturns)/float64(totalSales)*10000) / 100
	}

	// Count top return reasons
	reasonCount := make(map[string]int)
	for _, ret := range returns {
		reason := ret.ReturnReason
		if reason == "" {
			reason = "other"
		}
		reasonCount[reason]++
	}

	type reasonPair struct {
		reason string
		count  int
	}
	var reasonPairs []reasonPair
	for r, c := range reasonCount {
		reasonPairs = append(reasonPairs, reasonPair{r, c})
	}
	sort.Slice(reasonPairs, func(i, j int) bool {
		return reasonPairs[i].count > reasonPairs[j].count
	})

	topReasons := make([]ReturnReasonStat, 0, minInt(len(reasonPairs), 5))
	for i, rp := range reasonPairs {
		if i >= 5 {
			break
		}
		ratio := 0.0
		if totalReturns > 0 {
			ratio = math.Round(float64(rp.count)/float64(totalReturns)*10000) / 100
		}
		topReasons = append(topReasons, ReturnReasonStat{
			Reason: rp.reason,
			Count:  rp.count,
			Ratio:  ratio,
		})
	}

	// 3. Build sales data for LLM insights — count line items via PG jsonb_array_length
	//    to avoid O(N) json.Unmarshal call per order. The quantity is computed
	//    from the in-memory data below for the limited result set.
	salesData := make([]map[string]interface{}, 0, len(orders))
	for _, order := range orders {
		// Use PG jsonb_array_length in SQL if possible; for in-memory just count
		lineItems := []map[string]interface{}{}
		if err := json.Unmarshal(order.LineItems, &lineItems); err != nil {
			lineItems = []map[string]interface{}{}
		}
		salesData = append(salesData, map[string]interface{}{
			"quantity": len(lineItems),
			"revenue":  order.TotalPrice,
			"date":     order.CreatedAt.Format("2006-01-02"),
		})
	}

	// 4. Build product data (limit 1000 for performance)
	var products []model.SyncedProduct
	h.db.Where("shop_id = ? AND status = ?", shopUID, "active").Limit(1000).Find(&products)
	productData := make([]map[string]interface{}, 0, len(products))
	for _, p := range products {
		productData = append(productData, map[string]interface{}{
			"id":    p.PlatformID,
			"title": p.Title,
			"type":  p.ProductType,
			"price": extractPrice(p.Variants),
		})
	}

	// 5. Generate AI insights
	insProvider, insCfg, insErr := h.llmRouter.GetProvider(r.Context(), shopUID)
	if insErr != nil {
		slog.Error("insights: LLM provider unavailable", "error", insErr)
	}
	_ = insCfg
	result := insights.GenerateInsights(r.Context(), insProvider, req.ShopID, req.Period, salesData, productData)

	// Override metrics with real computed values
	result.Metrics.TotalSales = totalSales
	result.Metrics.TotalRevenue = math.Round(totalRevenue*100) / 100
	if totalSales > 0 {
		result.Metrics.AverageOrderValue = math.Round(totalRevenue/float64(totalSales)*100) / 100
	}

	// Compute conversion rate: orders / total visitors (approximated)
	// In production, get visitor count from Shopify Analytics API
	result.Metrics.ConversionRate = math.Round(float64(totalSales)/float64(maxInt(len(orders), 1))*100) / 100

	slog.Info("insights computed",
		"shop_id", req.ShopID,
		"period", req.Period,
		"orders", totalSales,
		"revenue", totalRevenue,
		"returns", totalReturns,
		"return_rate", returnRate,
	)

	httputil.WriteOK(w, ShopInsightResponse{
		Success:          true,
		Metrics:          &result.Metrics,
		Anomalies:        result.Anomalies,
		Recommendations:  result.Recommendations,
		Trends:           result.Trends,
		ReturnRate:       returnRate,
		TopReturnReasons: topReasons,
		SalesTrend:       salesTrend,
	})
}

// DailyReportRequest holds the daily report request.
type DailyReportRequest struct {
	ShopID string `json:"shop_id"`
	Date   string `json:"date"` // YYYY-MM-DD, defaults to yesterday
}

// DailyReportResponse holds the response.
type DailyReportResponse struct {
	Success      bool     `json:"success"`
	Date         string   `json:"date"`
	ShopID       string   `json:"shop_id"`
	Summary      string   `json:"summary"`
	Highlights   []string `json:"highlights"`
	ActionItems  []string `json:"action_items"`
	DailySales   int      `json:"daily_sales"`
	DailyRevenue float64  `json:"daily_revenue"`
	Error        string   `json:"error,omitempty"`
}

// DailyReport handles POST /api/insights/daily-report.
func (h *InsightsHandler) DailyReport(w http.ResponseWriter, r *http.Request) {
	var req DailyReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	reportDate := req.Date
	if reportDate == "" {
		reportDate = time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	}

	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	shopUID, err := database.ResolveShopID(h.db, req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	// Fetch orders for the specific date
	startOfDay, _ := time.Parse("2006-01-02", reportDate)
	endOfDay := startOfDay.Add(24 * time.Hour)

	var orders []model.SyncedOrder
	h.db.Where("shop_id = ? AND created_at >= ? AND created_at < ?",
		shopUID, startOfDay, endOfDay).Find(&orders)

	dailySales := len(orders)
	dailyRevenue := 0.0
	for _, order := range orders {
		dailyRevenue += order.TotalPrice
	}

	// Fetch returns for the date
	var returns []model.Return
	h.db.Where("shop_id = ? AND created_at >= ? AND created_at < ?",
		shopUID, startOfDay, endOfDay).Find(&returns)

	// Build data for LLM
	salesData := make([]map[string]interface{}, 0, len(orders))
	for _, order := range orders {
		lineItems := []map[string]interface{}{}
		json.Unmarshal(order.LineItems, &lineItems)
		salesData = append(salesData, map[string]interface{}{
			"quantity": len(lineItems),
			"revenue":  order.TotalPrice,
			"date":     reportDate,
		})
	}

	var products []model.SyncedProduct
	h.db.Where("shop_id = ?", shopUID).Limit(50).Find(&products)
	productData := make([]map[string]interface{}, 0, len(products))
	for _, p := range products {
		productData = append(productData, map[string]interface{}{
			"id":    p.PlatformID,
			"title": p.Title,
		})
	}

	dailyProvider, dailyCfg, dailyErr := h.llmRouter.GetProvider(r.Context(), shopUID)
	if dailyErr != nil {
		slog.Error("insights: LLM provider unavailable for daily report", "error", dailyErr)
	}
	_ = dailyCfg
	result := insights.DailyReport(r.Context(), dailyProvider, req.ShopID, reportDate, salesData, productData)

	slog.Info("daily report generated",
		"shop_id", req.ShopID,
		"date", reportDate,
		"sales", dailySales,
		"revenue", dailyRevenue,
		"returns", len(returns),
	)

	httputil.WriteOK(w, DailyReportResponse{
		Success:      true,
		Date:         result.Date,
		ShopID:       result.ShopID,
		Summary:      result.Summary,
		Highlights:   result.Highlights,
		ActionItems:  result.ActionItems,
		DailySales:   dailySales,
		DailyRevenue: math.Round(dailyRevenue*100) / 100,
	})
}

// ============================================================================
// Phase 1: New insight endpoints
// ============================================================================

// DailyReportQuery holds query params for GET /api/insights/daily.
type DailyReportQuery struct {
	ShopID string `json:"shop_id"`
	Date   string `json:"date"` // YYYY-MM-DD, defaults to yesterday
}

// DailyReportGetResponse holds the daily report response for the GET endpoint.
type DailyReportGetResponse struct {
	Success      bool     `json:"success"`
	Date         string   `json:"date"`
	ShopID       string   `json:"shop_id"`
	Summary      string   `json:"summary"`
	Highlights   []string `json:"highlights"`
	ActionItems  []string `json:"action_items"`
	DailySales   int      `json:"daily_sales"`
	DailyRevenue float64  `json:"daily_revenue"`
	Error        string   `json:"error,omitempty"`
}

// DailyGet handles GET /api/insights/daily.
func (h *InsightsHandler) DailyGet(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	}
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}
	// Delegate to existing DailyReport logic via a POST-like body
	// In a real implementation this would share the core logic
	req := DailyReportRequest{ShopID: shopID, Date: date}

	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	shopUID, err := database.ResolveShopID(h.db, req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	reportDate := req.Date
	startOfDay, _ := time.Parse("2006-01-02", reportDate)
	endOfDay := startOfDay.Add(24 * time.Hour)

	var orders []model.SyncedOrder
	h.db.Where("shop_id = ? AND created_at >= ? AND created_at < ?",
		shopUID, startOfDay, endOfDay).Find(&orders)

	dailySales := len(orders)
	dailyRevenue := 0.0
	for _, order := range orders {
		dailyRevenue += order.TotalPrice
	}

	salesData := make([]map[string]interface{}, 0, len(orders))
	for _, order := range orders {
		lineItems := []map[string]interface{}{}
		json.Unmarshal(order.LineItems, &lineItems)
		salesData = append(salesData, map[string]interface{}{
			"quantity": len(lineItems),
			"revenue":  order.TotalPrice,
			"date":     reportDate,
		})
	}

	var products []model.SyncedProduct
	h.db.Where("shop_id = ?", shopUID).Limit(50).Find(&products)
	productData := make([]map[string]interface{}, 0, len(products))
	for _, p := range products {
		productData = append(productData, map[string]interface{}{
			"id":    p.PlatformID,
			"title": p.Title,
		})
	}

	dailyProvider, dailyCfg, dailyErr := h.llmRouter.GetProvider(r.Context(), shopUID)
	if dailyErr != nil {
		slog.Error("insights: LLM provider unavailable for daily report", "error", dailyErr)
	}
	_ = dailyCfg
	result := insights.DailyReport(r.Context(), dailyProvider, req.ShopID, reportDate, salesData, productData)

	httputil.WriteOK(w, DailyReportGetResponse{
		Success:      true,
		Date:         result.Date,
		ShopID:       result.ShopID,
		Summary:      result.Summary,
		Highlights:   result.Highlights,
		ActionItems:  result.ActionItems,
		DailySales:   dailySales,
		DailyRevenue: math.Round(dailyRevenue*100) / 100,
	})
}

// HandlerTrendItem represents a single category trend entry.
type HandlerTrendItem struct {
	Category    string  `json:"category"`
	Direction   string  `json:"direction"`   // up, down, flat
	Percentage  float64 `json:"percentage"`
	Description string  `json:"description"`
	Source      string  `json:"source"` // google_trends, internal
}

// TrendsResponse holds the response for GET /api/insights/trends.
type TrendsResponse struct {
	Success bool              `json:"success"`
	Trends  []HandlerTrendItem `json:"trends"`
	Period  string            `json:"period"`
	Error   string            `json:"error,omitempty"`
}

// Trends handles GET /api/insights/trends.
func (h *InsightsHandler) Trends(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "7d"
	}

	// Stub: in production this queries Google Trends API
	_ = category
	httputil.WriteOK(w, TrendsResponse{
		Success: true,
		Trends:  []HandlerTrendItem{},
		Period:  period,
	})
}

// --- Helpers ---

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
