package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// CustomerInsightsHandler handles customer LTV and churn risk API endpoints.
type CustomerInsightsHandler struct {
	engine *service.CustomerIntelligenceEngine
	db     *gorm.DB
}

// NewCustomerInsightsHandler creates a new CustomerInsightsHandler.
func NewCustomerInsightsHandler(engine *service.CustomerIntelligenceEngine, db *gorm.DB) *CustomerInsightsHandler {
	return &CustomerInsightsHandler{engine: engine, db: db}
}

// InsightResponse is the DTO for a single customer insight.
type InsightResponse struct {
	ID                   string  `json:"id"`
	ShopID               string  `json:"shop_id"`
	CustomerEmail        string  `json:"customer_email"`
	CustomerName         string  `json:"customer_name"`
	TotalOrders          int     `json:"total_orders"`
	TotalSpent           float64 `json:"total_spent"`
	AvgOrderValue        float64 `json:"avg_order_value"`
	LastOrderAt          string  `json:"last_order_at,omitempty"`
	FirstOrderAt         string  `json:"first_order_at,omitempty"`
	AvgOrderIntervalDays float64 `json:"avg_order_interval_days"`
	PredictedLTV         float64 `json:"predicted_ltv"`
	ChurnRiskScore       float64 `json:"churn_risk_score"`
	ChurnRiskLevel       string  `json:"churn_risk_level"`
	ActiveProbability    float64 `json:"active_probability"`
	AvgRecentValue       float64 `json:"avg_recent_value"`
	AvgEarlyValue        float64 `json:"avg_early_value"`
	Confidence           string  `json:"confidence"`
	DataPoints           int     `json:"data_points"`
	ComputedAt           string  `json:"computed_at"`
}

// ListResponse is the paginated list response.
type InsightListResponse struct {
	Success  bool              `json:"success"`
	Insights []InsightResponse `json:"insights"`
	Total    int64             `json:"total"`
	Limit    int               `json:"limit"`
	Offset   int               `json:"offset"`
}

// GetInsights handles GET /api/customers/insights?sort=ltv&limit=50&offset=0
func (h *CustomerInsightsHandler) GetInsights(w http.ResponseWriter, r *http.Request) {
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

	sort := r.URL.Query().Get("sort")
	if sort == "" {
		sort = "ltv"
	}
	if sort != "ltv" && sort != "churn_risk" {
		httputil.WriteError(w, http.StatusBadRequest, "sort must be 'ltv' or 'churn_risk'")
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	insights, total, err := h.engine.GetInsights(r.Context(), shopID, sort, limit, offset)
	if err != nil {
		slog.Error("customer_insights: list failed", "shop_id", shopID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to get customer insights")
		return
	}

	items := make([]InsightResponse, 0, len(insights))
	for _, ins := range insights {
		items = append(items, insightToResponse(&ins))
	}

	httputil.WriteOK(w, InsightListResponse{
		Success:  true,
		Insights: items,
		Total:    total,
		Limit:    limit,
		Offset:   offset,
	})
}

// GetStats handles GET /api/customers/insights/stats
func (h *CustomerInsightsHandler) GetStats(w http.ResponseWriter, r *http.Request) {
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

	stats, err := h.engine.GetStats(r.Context(), shopID)
	if err != nil {
		slog.Error("customer_insights: stats failed", "shop_id", shopID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to get customer stats")
		return
	}

	stats["success"] = true
	httputil.WriteOK(w, stats)
}

// GetOneInsight handles GET /api/customers/insights/{email}
func (h *CustomerInsightsHandler) GetOneInsight(w http.ResponseWriter, r *http.Request) {
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

	email := chi.URLParam(r, "email")
	if email == "" {
		httputil.WriteError(w, http.StatusBadRequest, "email is required")
		return
	}

	insight, err := h.engine.GetOneInsight(r.Context(), shopID, email)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			httputil.WriteError(w, http.StatusNotFound, "Customer insight not found")
			return
		}
		slog.Error("customer_insights: get one failed", "shop_id", shopID, "email", email, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to get customer insight")
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"insight": insightToResponse(insight),
	})
}

// ExplainInsight handles GET /api/customers/insights/{email}/explain
// Generates an AI-powered explanation of the customer's LTV and churn risk.
func (h *CustomerInsightsHandler) ExplainInsight(w http.ResponseWriter, r *http.Request) {
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

	email := chi.URLParam(r, "email")
	if email == "" {
		httputil.WriteError(w, http.StatusBadRequest, "email is required")
		return
	}

	insight, err := h.engine.GetOneInsight(r.Context(), shopID, email)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			httputil.WriteError(w, http.StatusNotFound, "Customer insight not found")
			return
		}
		slog.Error("customer_insights: explain - get insight failed", "shop_id", shopID, "email", email, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to get customer insight")
		return
	}

	// Build prompt
	daysSinceLast := "N/A"
	if insight.LastOrderAt != nil {
		daysSinceLast = fmt.Sprintf("%.0f", time.Since(*insight.LastOrderAt).Hours()/24)
	}

	prompt := fmt.Sprintf(`你是电商数据分析师。基于以下数据，用一句话总结这个顾客的价值和风险，并给出1个具体操作建议。

顾客: %s
LTV预测: $%.0f
流失风险: %.0f分 (%s)
历史: %d单, 总消费$%.0f
上次: %s天前
间隔: %.0f天/单
趋势: 最近3单均价$%.0f vs 早期$%.0f

格式要求:
1. 25字以内中文总结
2. 1条具体操作建议（含金额/折扣）
3. 不用"建议考虑""可能"等模糊表述`,
		insight.CustomerName,
		insight.PredictedLTV,
		insight.ChurnRiskScore, insight.ChurnRiskLevel,
		insight.TotalOrders, insight.TotalSpent,
		daysSinceLast,
		insight.AvgOrderIntervalDays,
		insight.AvgRecentValue, insight.AvgEarlyValue,
	)

	provider, cfg, err := h.engine.GetLLMRouter().GetProvider(r.Context(), shopID)
	if err != nil {
		slog.Error("customer_insights: explain - LLM provider unavailable", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "AI service unavailable: "+err.Error())
		return
	}

	result, err := provider.ChatCompletion(r.Context(), &service.ChatCompletionRequest{
		Model:       cfg.Model,
		Messages:    []service.ChatMessage{{Role: "user", Content: prompt}},
		Temperature: 0.5,
		MaxTokens:   256,
	})
	if err != nil {
		slog.Error("customer_insights: explain - LLM call failed", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "AI service unavailable: "+err.Error())
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success":     true,
		"explanation": result,
	})
}

func insightToResponse(ins *model.CustomerInsight) InsightResponse {
	resp := InsightResponse{
		ID:                   ins.ID.String(),
		ShopID:               ins.ShopID.String(),
		CustomerEmail:        ins.CustomerEmail,
		CustomerName:         ins.CustomerName,
		TotalOrders:          ins.TotalOrders,
		TotalSpent:           ins.TotalSpent,
		AvgOrderValue:        ins.AvgOrderValue,
		AvgOrderIntervalDays: ins.AvgOrderIntervalDays,
		PredictedLTV:         ins.PredictedLTV,
		ChurnRiskScore:       ins.ChurnRiskScore,
		ChurnRiskLevel:       ins.ChurnRiskLevel,
		ActiveProbability:    ins.ActiveProbability,
		AvgRecentValue:       ins.AvgRecentValue,
		AvgEarlyValue:        ins.AvgEarlyValue,
		Confidence:           ins.Confidence,
		DataPoints:           ins.DataPoints,
		ComputedAt:           ins.ComputedAt.Format("2006-01-02T15:04:05Z07:00"),
	}

	if ins.LastOrderAt != nil {
		resp.LastOrderAt = ins.LastOrderAt.Format("2006-01-02T15:04:05Z07:00")
	}
	if ins.FirstOrderAt != nil {
		resp.FirstOrderAt = ins.FirstOrderAt.Format("2006-01-02T15:04:05Z07:00")
	}

	return resp
}
