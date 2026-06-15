package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
)

// ── Attribution ────────────────────────────────────────────────────────────────

type AttributionEngine struct{ db *gorm.DB }

func NewAttributionEngine(db *gorm.DB) *AttributionEngine {
	return &AttributionEngine{db}
}

type ChannelAttribution struct {
	Channel          string  `json:"channel"`
	AttributedRevenue float64 `json:"attributed_revenue"`
	Percentage        float64 `json:"percentage"`
	TouchpointCount   int64   `json:"touchpoint_count"`
}

type AttributionReport struct {
	TotalRevenue   float64               `json:"total_revenue"`
	TotalOrders    int64                 `json:"total_orders"`
	ChannelSummary []ChannelAttribution  `json:"channel_summary"`
}

// ComputeAttribution calculates channel attribution and persists results to
// attribution_models for caching and future retrieval.
func (e *AttributionEngine) ComputeAttribution(ctx context.Context, shopIDStr, modelType string, days int) (*AttributionReport, error) {
	shopID, err := uuid.Parse(shopIDStr)
	if err != nil {
		return nil, fmt.Errorf("analytics: invalid shop_id: %w", err)
	}
	if modelType == "" {
		modelType = "linear"
	}

	cutoff := time.Now().AddDate(0, 0, -days)

	// Count touchpoints per channel
	touchMap := make(map[string]int64)
	type row struct {
		Channel    string
		OrderValue float64
	}
	var rows []row
	e.db.WithContext(ctx).Table("analytics_events").
		Select("COALESCE(channel, 'direct') as channel, COALESCE(order_value, 0) as order_value").
		Where("shop_id = ? AND created_at >= ?", shopID, cutoff).
		Order("created_at ASC").
		Find(&rows)

	for _, r := range rows {
		ch := r.Channel
		if ch == "" {
			ch = "direct"
		}
		touchMap[ch]++
	}

	// Aggregate revenue per channel
	chMap := make(map[string]float64)
	var totalRev float64
	var totalOrd int64
	for _, r := range rows {
		ch := r.Channel
		if ch == "" {
			ch = "direct"
		}
		if r.OrderValue > 0 {
			chMap[ch] += r.OrderValue
			totalRev += r.OrderValue
			totalOrd++
		}
	}

	summaries := []ChannelAttribution{}
	for ch, rev := range chMap {
		pct := 0.0
		if totalRev > 0 {
			pct = math.Round(rev/totalRev*10000) / 100
		}
		summaries = append(summaries, ChannelAttribution{
			Channel:          ch,
			AttributedRevenue: math.Round(rev*100) / 100,
			Percentage:        pct,
			TouchpointCount:   touchMap[ch],
		})
	}

	report := &AttributionReport{
		TotalRevenue:   totalRev,
		TotalOrders:    totalOrd,
		ChannelSummary: summaries,
	}

	// Persist to attribution_models (upsert by shop_id)
	resultsJSON, _ := json.Marshal(report)
	now := time.Now()
	attr := model.AttributionModel{
		ShopID:         shopID,
		ModelType:      modelType,
		WindowDays:     days,
		ResultsJSON:    datatypes.JSON(resultsJSON),
		LastComputedAt: &now,
	}
	if err := e.db.WithContext(ctx).
		Where("shop_id = ?", shopID).
		Assign(attr).
		FirstOrCreate(&attr).Error; err != nil {
		slog.Warn("analytics: failed to persist attribution", "shop_id", shopID, "error", err)
	}

	return report, nil
}

// GetAttribution returns the cached attribution from attribution_models.
func (e *AttributionEngine) GetAttribution(ctx context.Context, shopIDStr string) (*AttributionReport, error) {
	shopID, err := uuid.Parse(shopIDStr)
	if err != nil {
		return nil, fmt.Errorf("analytics: invalid shop_id: %w", err)
	}
	var attr model.AttributionModel
	if err := e.db.WithContext(ctx).
		Where("shop_id = ?", shopID).
		Order("last_computed_at DESC").
		First(&attr).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}

	var report AttributionReport
	if err := json.Unmarshal(attr.ResultsJSON, &report); err != nil {
		return nil, err
	}
	return &report, nil
}

// ── Predictive / Churn ─────────────────────────────────────────────────────────

type PredictiveModel struct{ db *gorm.DB }

func NewPredictiveModel(db *gorm.DB) *PredictiveModel {
	return &PredictiveModel{db}
}

// ComputeChurnScores calculates churn risk for all customers and writes both
// predictive_scores (simple score) and customer_churn_risks (detailed factors).
func (m *PredictiveModel) ComputeChurnScores(ctx context.Context, sid string) error {
	shopID, err := uuid.Parse(sid)
	if err != nil {
		return fmt.Errorf("analytics: invalid shop_id: %w", err)
	}
	now := time.Now()

	// Clear previous results
	m.db.WithContext(ctx).Where("shop_id = ?", shopID).Delete(&model.PredictiveScore{})
	m.db.WithContext(ctx).Where("shop_id = ?", shopID).Delete(&model.CustomerChurnRisk{})

	// Get customers with their last order date from synced_orders (more reliable than analytics_events)
	var orders []struct {
		CustomerID  string
		LastOrderAt time.Time
		OrderCount  int
	}
	m.db.WithContext(ctx).Table("synced_orders").
		Select("customer_id, MAX(created_at) as last_order_at, COUNT(*) as order_count").
		Where("shop_id = ? AND customer_id != ''", shopID).
		Group("customer_id").
		Scan(&orders)

	if len(orders) == 0 {
		return nil
	}

	// Get negative review signals
	negMap := make(map[string]int)
	var negRows []struct {
		CustomerID string
		Count      int
	}
	m.db.WithContext(ctx).Table("synced_reviews").
		Select("customer_id, COUNT(*) as count").
		Where("shop_id = ? AND customer_id != '' AND rating <= 3", shopID).
		Group("customer_id").
		Scan(&negRows)
	for _, r := range negRows {
		negMap[r.CustomerID] = r.Count
	}

	// Get chat objection signals
	objMap := make(map[string]int)
	var objRows []struct {
		CustomerID string
		Count      int
	}
	m.db.WithContext(ctx).Table("chat_intent_events").
		Select("customer_id, COUNT(*) as count").
		Where("shop_id = ? AND customer_id != '' AND event_type LIKE '%objection%'", shopID).
		Group("customer_id").
		Scan(&objRows)
	for _, r := range objRows {
		objMap[r.CustomerID] = r.Count
	}

	for _, o := range orders {
		daysSince := int(now.Sub(o.LastOrderAt).Hours() / 24)
		risk := 0.0
		level := "low"

		// Base risk from recency
		if daysSince > 90 {
			risk = 0.9
			level = "high"
		} else if daysSince > 30 {
			risk = 0.5
			level = "medium"
		} else if daysSince > 14 {
			risk = 0.2
		}

		// Order count trend factor (fewer orders = higher risk)
		orderTrend := 0.0
		if o.OrderCount <= 2 {
			orderTrend = -0.3
		} else if o.OrderCount <= 5 {
			orderTrend = -0.1
		} else {
			orderTrend = 0.1
		}

		// Sentiment trend from reviews and chat
		sentimentTrend := 0.0
		if n, ok := negMap[o.CustomerID]; ok && n > 0 {
			sentimentTrend -= float64(n) * 0.15
		}
		if n, ok := objMap[o.CustomerID]; ok && n > 0 {
			sentimentTrend -= float64(n) * 0.1
		}

		// Composite risk adjusted by behavioral signals
		compositeRisk := risk + (orderTrend*-0.3) + (sentimentTrend*-0.5)
		if compositeRisk > 1.0 {
			compositeRisk = 1.0
		}
		if compositeRisk < 0.0 {
			compositeRisk = 0.0
		}

		// Build top factors
		var factors []string
		if daysSince > 90 {
			factors = append(factors, "No purchase in 90+ days")
		} else if daysSince > 30 {
			factors = append(factors, "No purchase in 30+ days")
		}
		if o.OrderCount <= 2 {
			factors = append(factors, "Low order count — first-time buyer risk")
		}
		if n := negMap[o.CustomerID]; n > 0 {
			factors = append(factors, "Left negative reviews")
		}
		if n := objMap[o.CustomerID]; n > 0 {
			factors = append(factors, "Price/discount objections in chat")
		}
		factorsJSON, _ := json.Marshal(factors)

		// Write predictive_scores
		m.db.WithContext(ctx).Create(&model.PredictiveScore{
			ShopID:         shopID,
			CustomerID:     o.CustomerID,
			ChurnScore:     compositeRisk,
			ChurnRiskLevel: level,
			ComputedAt:     now,
		})

		// Write customer_churn_risks
		churnRisk := model.CustomerChurnRisk{
			ShopID:          shopID,
			CustomerID:      o.CustomerID,
			ChurnScore:      compositeRisk,
			RiskLevel:       level,
			LastOrderDays:   daysSince,
			OrderCountTrend: orderTrend,
			SentimentTrend:  sentimentTrend,
			TopFactors:      datatypes.JSON(factorsJSON),
			ComputedAt:      now,
		}
		m.db.WithContext(ctx).Create(&churnRisk)

		// Link to Customer360View
		m.db.WithContext(ctx).Model(&model.Customer360View{}).
			Where("shop_id = ? AND customer_id = ?", shopID, o.CustomerID).
			Update("churn_risk_id", churnRisk.ID)
	}

	slog.Info("churn scores computed",
		"shop_id", sid,
		"customers", len(orders),
	)
	return nil
}

func (m *PredictiveModel) ComputeLTVPredictions(ctx context.Context, sid string) error {
	return m.ComputeChurnScores(ctx, sid)
}

func (m *PredictiveModel) GetPredictions(sid, risk string, lim, off int) ([]model.PredictiveScore, int64, error) {
	shopID, err := uuid.Parse(sid)
	if err != nil {
		return nil, 0, fmt.Errorf("analytics: invalid shop_id: %w", err)
	}
	q := m.db.Where("shop_id = ?", shopID)
	if risk != "" {
		q = q.Where("churn_risk_level = ?", risk)
	}
	var t int64
	q.Model(&model.PredictiveScore{}).Count(&t)
	var s []model.PredictiveScore
	q.Order("churn_score DESC").Limit(lim).Offset(off).Find(&s)
	return s, t, nil
}

// GetChurnRisks returns detailed churn risk records from customer_churn_risks.
func (m *PredictiveModel) GetChurnRisks(ctx context.Context, sid string, riskLevel string, lim, off int) ([]model.CustomerChurnRisk, int64, error) {
	shopID, err := uuid.Parse(sid)
	if err != nil {
		return nil, 0, fmt.Errorf("analytics: invalid shop_id: %w", err)
	}
	q := m.db.WithContext(ctx).Where("shop_id = ?", shopID)
	if riskLevel != "" {
		q = q.Where("risk_level = ?", riskLevel)
	}
	var t int64
	q.Model(&model.CustomerChurnRisk{}).Count(&t)
	var risks []model.CustomerChurnRisk
	q.Order("churn_score DESC").Limit(lim).Offset(off).Find(&risks)
	return risks, t, nil
}
