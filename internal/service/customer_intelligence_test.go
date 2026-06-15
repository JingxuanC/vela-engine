package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestComputeInsight_ConservativeEstimate_LessThan3Orders(t *testing.T) {
	agg := orderAggRow{
		CustomerEmail: "test@example.com",
		CustomerName:  "Test User",
		OrderCount:    2,
		TotalSpent:    80.0,
		FirstOrderAt:  time.Now().Add(-30 * 24 * time.Hour),
		LastOrderAt:   time.Now().Add(-5 * 24 * time.Hour),
	}
	orders := []singleOrderRow{
		{TotalPrice: 40, OrderCreatedAt: time.Now().Add(-30 * 24 * time.Hour)},
		{TotalPrice: 40, OrderCreatedAt: time.Now().Add(-5 * 24 * time.Hour)},
	}

	engine := &CustomerIntelligenceEngine{}
	insight := engine.computeInsight(agg, orders)

	assert.Equal(t, "low", insight.Confidence, "should be low confidence for < 3 orders")
	assert.Equal(t, float64(80), insight.PredictedLTV, "LTV should be total spent for conservative estimate")
	assert.Equal(t, float64(0.5), insight.ActiveProbability, "active prob should be 0.5 for conservative")
	assert.Equal(t, float64(30), insight.ChurnRiskScore, "churn should be 30 for conservative")
	assert.Equal(t, "low", insight.ChurnRiskLevel)
	assert.Equal(t, 2, insight.DataPoints)
}

func TestComputeInsight_ConservativeEstimate_1Order(t *testing.T) {
	agg := orderAggRow{
		CustomerEmail: "single@example.com",
		CustomerName:  "Single Buyer",
		OrderCount:    1,
		TotalSpent:    50.0,
		FirstOrderAt:  time.Now().Add(-1 * 24 * time.Hour),
		LastOrderAt:   time.Now().Add(-1 * 24 * time.Hour),
	}
	orders := []singleOrderRow{
		{TotalPrice: 50, OrderCreatedAt: time.Now().Add(-1 * 24 * time.Hour)},
	}

	engine := &CustomerIntelligenceEngine{}
	insight := engine.computeInsight(agg, orders)

	assert.Equal(t, float64(50), insight.PredictedLTV)
	assert.Equal(t, "low", insight.Confidence)
	assert.Equal(t, 1, insight.DataPoints)
}

func TestComputeInsight_FullCalculation_MultipleOrders(t *testing.T) {
	now := time.Now().UTC()
	firstOrder := now.Add(-90 * 24 * time.Hour)  // 90 days ago
	midOrder := now.Add(-50 * 24 * time.Hour)    // 50 days ago
	lastOrder := now.Add(-20 * 24 * time.Hour)   // 20 days ago

	agg := orderAggRow{
		CustomerEmail: "vip@example.com",
		CustomerName:  "VIP Customer",
		OrderCount:    3,
		TotalSpent:    240.0,
		FirstOrderAt:  firstOrder,
		LastOrderAt:   lastOrder,
	}
	orders := []singleOrderRow{
		{TotalPrice: 80, OrderCreatedAt: firstOrder},
		{TotalPrice: 80, OrderCreatedAt: midOrder},
		{TotalPrice: 80, OrderCreatedAt: lastOrder},
	}

	engine := &CustomerIntelligenceEngine{}
	insight := engine.computeInsight(agg, orders)

	// Span: 90-20 = 70 days / 2 intervals = 35 day avg
	assert.InDelta(t, 35.0, insight.AvgOrderIntervalDays, 2.0)

	// Active probability: 1 - 20/35 = 0.43
	assert.InDelta(t, 0.43, insight.ActiveProbability, 0.05)

	// LTV: 240 + (80 * 0.43 * 1080/35) = 240 + (80 * 13.2) ≈ 1296
	assert.Greater(t, insight.PredictedLTV, float64(1000))
	assert.Less(t, insight.PredictedLTV, float64(1500))

	// Churn: 20/35*50 = 28.6, steady value → low
	assert.LessOrEqual(t, insight.ChurnRiskScore, float64(35))
	assert.Equal(t, "low", insight.ChurnRiskLevel)
	assert.Equal(t, "high", insight.Confidence)
}

func TestComputeInsight_HighChurnRisk(t *testing.T) {
	now := time.Now().UTC()
	firstOrder := now.Add(-200 * 24 * time.Hour)
	oldOrder := now.Add(-160 * 24 * time.Hour)
	lastOrder := now.Add(-90 * 24 * time.Hour) // 90 days ago, but avg interval is only 55 days

	agg := orderAggRow{
		CustomerEmail: "leaving@example.com",
		CustomerName:  "Leaving Customer",
		OrderCount:    3,
		TotalSpent:    300.0,
		FirstOrderAt:  firstOrder,
		LastOrderAt:   lastOrder,
	}
	orders := []singleOrderRow{
		{TotalPrice: 120, OrderCreatedAt: firstOrder},
		{TotalPrice: 100, OrderCreatedAt: oldOrder},
		{TotalPrice: 80, OrderCreatedAt: lastOrder},
	}

	engine := &CustomerIntelligenceEngine{}
	insight := engine.computeInsight(agg, orders)

	// Span: 200-90 = 110 days / 2 = 55 day avg interval
	// Days since last: 90 → base = 90/55*50 = 81.8
	// Declining value: early avg (120+100)/2=110, recent=80 → decay=(110-80)/110=0.27
	// modifier = min(1.2, 1.27) = 1.2 → churn = 81.8*1.2 = 98 → clamped or ~98
	assert.Greater(t, insight.ChurnRiskScore, float64(60), "should be high churn risk with long absence")
	assert.Equal(t, "high", insight.ChurnRiskLevel)
}

func TestComputeInsight_AvgRecentVsEarly(t *testing.T) {
	now := time.Now().UTC()
	orders := []singleOrderRow{
		{TotalPrice: 100, OrderCreatedAt: now.Add(-90 * 24 * time.Hour)},
		{TotalPrice: 100, OrderCreatedAt: now.Add(-60 * 24 * time.Hour)},
		{TotalPrice: 120, OrderCreatedAt: now.Add(-30 * 24 * time.Hour)},
		{TotalPrice: 120, OrderCreatedAt: now.Add(-15 * 24 * time.Hour)},
		{TotalPrice: 150, OrderCreatedAt: now.Add(-5 * 24 * time.Hour)},
	}

	engine := &CustomerIntelligenceEngine{}
	insight := engine.computeInsight(orderAggRow{
		CustomerEmail: "growing@example.com",
		CustomerName:  "Growing Customer",
		OrderCount:    5,
		TotalSpent:    590.0,
		FirstOrderAt:  now.Add(-90 * 24 * time.Hour),
		LastOrderAt:   now.Add(-5 * 24 * time.Hour),
	}, orders)

	// Recent 3: (120+120+150)/3 = 130, Early 3: (100+100+120)/3 = 106.67
	assert.InDelta(t, 130.0, insight.AvgRecentValue, 5.0)
	assert.InDelta(t, 106.0, insight.AvgEarlyValue, 5.0)

	// Growing customer → churn should be low
	assert.Equal(t, "low", insight.ChurnRiskLevel)
	assert.Greater(t, insight.PredictedLTV, insight.TotalSpent, "LTV > total spent for growing customer")
}
