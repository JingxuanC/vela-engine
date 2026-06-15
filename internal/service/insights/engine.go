// Package insights provides AI-powered shop analytics, trend detection, and daily reports.
package insights

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/JingxuanC/vela-engine/internal/service"
)

// Metrics holds computed business metrics.
type Metrics struct {
	TotalSales        int     `json:"total_sales"`
	TotalRevenue      float64 `json:"total_revenue"`
	ConversionRate    float64 `json:"conversion_rate"`
	AverageOrderValue float64 `json:"average_order_value"`
	Period            string  `json:"period"`
}

// AnomalyItem describes a detected anomaly.
type AnomalyItem struct {
	Metric      string `json:"metric"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
	Suggestion  string `json:"suggestion"`
}

// TrendItem describes an identified trend.
type TrendItem struct {
	Dimension  string  `json:"dimension"`
	Direction  string  `json:"direction"`
	Percentage float64 `json:"percentage"`
	Insight    string  `json:"insight"`
}

// RecommendationItem is an actionable recommendation.
type RecommendationItem struct {
	Priority       string `json:"priority"`
	Category       string `json:"category"`
	Action         string `json:"action"`
	ExpectedImpact string `json:"expected_impact"`
}

// InsightsResult is the full output of shop insight analysis.
type InsightsResult struct {
	Metrics         Metrics              `json:"metrics"`
	Anomalies       []AnomalyItem        `json:"anomalies"`
	Recommendations []RecommendationItem `json:"recommendations"`
	Trends          []TrendItem          `json:"trends"`
}

// DailyReportResult holds a generated daily business report.
type DailyReportResult struct {
	Date        string   `json:"date"`
	ShopID      string   `json:"shop_id"`
	Summary     string   `json:"summary"`
	Highlights  []string `json:"highlights"`
	ActionItems []string `json:"action_items"`
}

// GenerateInsights analyzes sales data and generates AI-powered business insights.
func GenerateInsights(ctx context.Context, client service.LLMProvider, shopID string, period string, salesData []map[string]interface{}, productData []map[string]interface{}) InsightsResult {
	slog.Info("generating insights", "shop_id", shopID, "period", period)

	// Compute basic metrics
	metrics := Metrics{Period: period}
	if len(salesData) > 0 {
		for _, s := range salesData {
			if qty, ok := floatVal(s["quantity"]); ok {
				metrics.TotalSales += int(qty)
			}
			if rev, ok := floatVal(s["revenue"]); ok {
				metrics.TotalRevenue += rev
			}
		}
		if metrics.TotalSales > 0 {
			metrics.AverageOrderValue = roundTo(metrics.TotalRevenue/float64(metrics.TotalSales), 2)
		}
		metrics.ConversionRate = roundTo(float64(metrics.TotalSales)/float64(maxInt(len(salesData), 1))*0.01, 2)
	}

	salesSummary, _ := json.Marshal(map[string]interface{}{
		"period":           period,
		"total_sales":      metrics.TotalSales,
		"total_revenue":    metrics.TotalRevenue,
		"avg_order_value":  metrics.AverageOrderValue,
		"conversion_rate":  metrics.ConversionRate,
		"num_products":     len(productData),
		"num_transactions": len(salesData),
	})

	messages := []service.ChatMessage{
		{Role: "system", Content: `You are a business-intelligence analyst. Analyze sales data and return JSON with keys:
- "anomalies": [{metric, severity, description, suggestion}]
- "recommendations": [{priority, category, action, expected_impact}]
- "trends": [{dimension, direction, percentage, insight}]`},
		{Role: "user", Content: fmt.Sprintf("Shop ID: %s\nSales data:\n%s\n\nAnalyze and return JSON.", shopID, string(salesSummary))},
	}

	req := &service.ChatCompletionRequest{
		Messages:    messages,
		Temperature: 0.4,
		MaxTokens:   2048,
	}

	raw, err := client.ChatCompletion(ctx, req)
	if err != nil {
		slog.Warn("insights: LLM call failed", "error", err)
		return InsightsResult{
			Metrics: metrics,
			Anomalies: []AnomalyItem{{
				Metric: "general", Severity: "low",
				Description: "Insufficient data to detect anomalies.",
				Suggestion:  "Collect more sales data over a longer period.",
			}},
			Recommendations: []RecommendationItem{{
				Priority: "medium", Category: "marketing",
				Action:         "Analyze top-selling products and run targeted promotions.",
				ExpectedImpact: "Increased revenue from high-performing categories.",
			}},
		}
	}

	var parsed map[string]interface{}
	json.Unmarshal(service.ExtractJSONHelper(raw), &parsed)

	result := InsightsResult{Metrics: metrics}
	if anomalies, ok := parsed["anomalies"].([]interface{}); ok {
		for _, a := range anomalies {
			if am, ok := a.(map[string]interface{}); ok {
				result.Anomalies = append(result.Anomalies, AnomalyItem{
					Metric:      strVal(am, "metric"),
					Severity:    strVal(am, "severity"),
					Description: strVal(am, "description"),
					Suggestion:  strVal(am, "suggestion"),
				})
			}
		}
	}
	if recs, ok := parsed["recommendations"].([]interface{}); ok {
		for _, r := range recs {
			if rm, ok := r.(map[string]interface{}); ok {
				result.Recommendations = append(result.Recommendations, RecommendationItem{
					Priority:       strVal(rm, "priority"),
					Category:       strVal(rm, "category"),
					Action:         strVal(rm, "action"),
					ExpectedImpact: strVal(rm, "expected_impact"),
				})
			}
		}
	}
	if trends, ok := parsed["trends"].([]interface{}); ok {
		for _, t := range trends {
			if tm, ok := t.(map[string]interface{}); ok {
				result.Trends = append(result.Trends, TrendItem{
					Dimension:  strVal(tm, "dimension"),
					Direction:  strVal(tm, "direction"),
					Percentage: floatValDef(tm, "percentage", 0),
					Insight:    strVal(tm, "insight"),
				})
			}
		}
	}

	return result
}

// DailyReport generates a daily business performance report.
func DailyReport(ctx context.Context, client service.LLMProvider, shopID string, date string, salesData []map[string]interface{}, productData []map[string]interface{}) DailyReportResult {
	reportDate := date
	if reportDate == "" {
		reportDate = time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	}

	slog.Info("daily report", "shop_id", shopID, "date", reportDate)

	dailySales := 0
	dailyRevenue := 0.0
	for _, s := range salesData {
		if qty, ok := floatVal(s["quantity"]); ok {
			dailySales += int(qty)
		}
		if rev, ok := floatVal(s["revenue"]); ok {
			dailyRevenue += rev
		}
	}

	messages := []service.ChatMessage{
		{Role: "system", Content: `You are an e-commerce operations manager. Return a JSON daily report with:
- "summary": 2-3 sentence overview
- "highlights": 3-5 positive outcomes
- "action_items": 2-4 concrete next steps`},
		{Role: "user", Content: fmt.Sprintf(
			"Shop: %s\nDate: %s\nDaily sales: %d units\nDaily revenue: $%.2f\nTransactions: %d\nActive products: %d\n\nGenerate the daily report JSON.",
			shopID, reportDate, dailySales, dailyRevenue, len(salesData), len(productData),
		)},
	}

	req := &service.ChatCompletionRequest{
		Messages:    messages,
		Temperature: 0.3,
		MaxTokens:   1024,
	}

	raw, err := client.ChatCompletion(ctx, req)
	if err != nil {
		slog.Warn("insights: daily report LLM call failed", "error", err)
		return DailyReportResult{
			Date:   reportDate,
			ShopID: shopID,
			Summary: fmt.Sprintf(
				"Shop %s recorded %d unit(s) sold generating $%.2f in revenue on %s.",
				shopID, dailySales, dailyRevenue, reportDate,
			),
			Highlights: []string{
				fmt.Sprintf("Processed %d transaction(s) on %s.", len(salesData), reportDate),
			},
			ActionItems: []string{
				"Review inventory levels for top-selling products.",
				"Analyze traffic sources to optimize marketing spend.",
			},
		}
	}

	var parsed map[string]interface{}
	json.Unmarshal(service.ExtractJSONHelper(raw), &parsed)

	return DailyReportResult{
		Date:        reportDate,
		ShopID:      shopID,
		Summary:     strVal(parsed, "summary"),
		Highlights:  strSlice(parsed, "highlights"),
		ActionItems: strSlice(parsed, "action_items"),
	}
}

func floatVal(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	}
	return 0, false
}

func floatValDef(m map[string]interface{}, key string, def float64) float64 {
	if v, ok := m[key]; ok {
		if f, ok := floatVal(v); ok {
			return f
		}
	}
	return def
}

func strVal(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func strSlice(m map[string]interface{}, key string) []string {
	if v, ok := m[key]; ok {
		switch arr := v.(type) {
		case []interface{}:
			var result []string
			for _, item := range arr {
				if s, ok := item.(string); ok {
					result = append(result, s)
				}
			}
			return result
		}
	}
	return nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func roundTo(v float64, decimals int) float64 {
	pow := 1.0
	for i := 0; i < decimals; i++ {
		pow *= 10
	}
	return float64(int(v*pow+0.5)) / pow
}
