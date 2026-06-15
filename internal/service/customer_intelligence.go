package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/JingxuanC/vela-engine/internal/model"
)

// CustomerIntelligenceEngine computes LTV predictions and churn risk scores.
type CustomerIntelligenceEngine struct {
	db        *gorm.DB
	llmRouter *LLMRouter
}

// NewCustomerIntelligenceEngine creates a new CustomerIntelligenceEngine.
func NewCustomerIntelligenceEngine(db *gorm.DB, llmRouter *LLMRouter) *CustomerIntelligenceEngine {
	return &CustomerIntelligenceEngine{db: db, llmRouter: llmRouter}
}

// GetLLMRouter returns the LLM router for AI-powered explanations.
func (e *CustomerIntelligenceEngine) GetLLMRouter() *LLMRouter {
	return e.llmRouter
}

// orderAggRow holds aggregated order data per customer.
type orderAggRow struct {
	CustomerEmail string
	CustomerName  string
	OrderCount    int
	TotalSpent    float64
	FirstOrderAt  time.Time
	LastOrderAt   time.Time
}

// singleOrderRow holds a single order's data for trend calculation.
type singleOrderRow struct {
	CustomerEmail string
	TotalPrice    float64
	OrderCreatedAt time.Time
}

// ComputeAll computes LTV and churn risk for all customers of a shop.
func (e *CustomerIntelligenceEngine) ComputeAll(ctx context.Context, shopID uuid.UUID) error {
	aggRows, err := e.aggregateOrders(ctx, shopID)
	if err != nil {
		return fmt.Errorf("ComputeAll: aggregate orders: %w", err)
	}

	orderRows, err := e.fetchAllOrderRows(ctx, shopID)
	if err != nil {
		return fmt.Errorf("ComputeAll: fetch order rows: %w", err)
	}

	// Group single order rows by email
	ordersByEmail := make(map[string][]singleOrderRow)
	for _, row := range orderRows {
		ordersByEmail[row.CustomerEmail] = append(ordersByEmail[row.CustomerEmail], row)
	}

	for _, agg := range aggRows {
		orders := ordersByEmail[agg.CustomerEmail]
		insight := e.computeInsight(agg, orders)
		insight.ShopID = shopID
		insight.ComputedAt = time.Now().UTC()

		if err := e.upsertInsight(ctx, &insight); err != nil {
			slog.Error("customer_intelligence: upsert failed", "shop_id", shopID, "email", agg.CustomerEmail, "error", err)
		}
	}

	slog.Info("customer_intelligence: ComputeAll done", "shop_id", shopID, "customers", len(aggRows))
	return nil
}

// ComputeOne computes LTV and churn risk for a single customer.
func (e *CustomerIntelligenceEngine) ComputeOne(ctx context.Context, shopID uuid.UUID, email string) error {
	aggRow, err := e.aggregateOneOrder(ctx, shopID, email)
	if err != nil {
		return fmt.Errorf("ComputeOne: aggregate: %w", err)
	}

	orderRows, err := e.fetchOrderRowsForEmail(ctx, shopID, email)
	if err != nil {
		return fmt.Errorf("ComputeOne: fetch orders: %w", err)
	}

	insight := e.computeInsight(*aggRow, orderRows)
	insight.ShopID = shopID
	insight.ComputedAt = time.Now().UTC()

	return e.upsertInsight(ctx, &insight)
}

// GetInsights returns paginated customer insights for a shop.
func (e *CustomerIntelligenceEngine) GetInsights(ctx context.Context, shopID uuid.UUID, sort string, limit, offset int) ([]model.CustomerInsight, int64, error) {
	query := e.db.WithContext(ctx).Model(&model.CustomerInsight{}).Where("shop_id = ?", shopID)

	var total int64
	query.Count(&total)

	orderClause := "predicted_ltv DESC"
	switch sort {
	case "churn_risk":
		orderClause = "churn_risk_score DESC"
	default:
		orderClause = "predicted_ltv DESC"
	}

	var insights []model.CustomerInsight
	if err := query.Order(orderClause).Limit(limit).Offset(offset).Find(&insights).Error; err != nil {
		return nil, 0, fmt.Errorf("GetInsights: %w", err)
	}

	return insights, total, nil
}

// GetOneInsight returns the insight for a single customer by email.
func (e *CustomerIntelligenceEngine) GetOneInsight(ctx context.Context, shopID uuid.UUID, email string) (*model.CustomerInsight, error) {
	var insight model.CustomerInsight
	if err := e.db.WithContext(ctx).
		Where("shop_id = ? AND customer_email = ?", shopID, email).
		First(&insight).Error; err != nil {
		return nil, err
	}
	return &insight, nil
}

// GetStats returns aggregate stats for a shop's customer insights.
func (e *CustomerIntelligenceEngine) GetStats(ctx context.Context, shopID uuid.UUID) (map[string]interface{}, error) {
	type row struct {
		TotalCustomers int64   `gorm:"column:total_customers"`
		TotalLTV       float64 `gorm:"column:total_ltv"`
		AvgLTV         float64 `gorm:"column:avg_ltv"`
		LowCount       int64   `gorm:"column:low_count"`
		MediumCount    int64   `gorm:"column:medium_count"`
		HighCount      int64   `gorm:"column:high_count"`
	}

	var r row
	err := e.db.WithContext(ctx).
		Model(&model.CustomerInsight{}).
		Select(`
			COUNT(*) AS total_customers,
			COALESCE(SUM(predicted_ltv), 0) AS total_ltv,
			COALESCE(AVG(predicted_ltv), 0) AS avg_ltv,
			COALESCE(SUM(CASE WHEN churn_risk_level = 'low' THEN 1 ELSE 0 END), 0) AS low_count,
			COALESCE(SUM(CASE WHEN churn_risk_level = 'medium' THEN 1 ELSE 0 END), 0) AS medium_count,
			COALESCE(SUM(CASE WHEN churn_risk_level = 'high' THEN 1 ELSE 0 END), 0) AS high_count
		`).
		Where("shop_id = ?", shopID).
		Scan(&r).Error
	if err != nil {
		return nil, fmt.Errorf("GetStats: %w", err)
	}

	return map[string]interface{}{
		"total_customers":       r.TotalCustomers,
		"total_predicted_ltv":   math.Round(r.TotalLTV*100) / 100,
		"avg_ltv":               math.Round(r.AvgLTV*100) / 100,
		"churn_risk_distribution": map[string]int64{
			"low":    r.LowCount,
			"medium": r.MediumCount,
			"high":   r.HighCount,
		},
	}, nil
}

// aggregateOrders returns per-customer aggregated order data.
func (e *CustomerIntelligenceEngine) aggregateOrders(ctx context.Context, shopID uuid.UUID) ([]orderAggRow, error) {
	type aggRow struct {
		CustomerEmail string
		CustomerName  string
		OrderCount    int
		TotalSpent    float64
		FirstOrderAt  time.Time
		LastOrderAt   time.Time
	}

	var rows []aggRow
	err := e.db.WithContext(ctx).
		Model(&model.SyncedOrder{}).
		Select(`customer_email,
			MAX(customer_name) AS customer_name,
			COUNT(*) AS order_count,
			SUM(total_price) AS total_spent,
			MIN(COALESCE(order_created_at, created_at)) AS first_order_at,
			MAX(COALESCE(order_created_at, created_at)) AS last_order_at`).
		Where("shop_id = ? AND customer_email != ''", shopID).
		Group("customer_email").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	result := make([]orderAggRow, len(rows))
	for i, r := range rows {
		result[i] = orderAggRow{
			CustomerEmail: r.CustomerEmail,
			CustomerName:  r.CustomerName,
			OrderCount:    r.OrderCount,
			TotalSpent:    r.TotalSpent,
			FirstOrderAt:  r.FirstOrderAt,
			LastOrderAt:   r.LastOrderAt,
		}
	}
	return result, nil
}

// aggregateOneOrder returns aggregated data for a single customer.
func (e *CustomerIntelligenceEngine) aggregateOneOrder(ctx context.Context, shopID uuid.UUID, email string) (*orderAggRow, error) {
	type aggRow struct {
		CustomerEmail string
		CustomerName  string
		OrderCount    int
		TotalSpent    float64
		FirstOrderAt  time.Time
		LastOrderAt   time.Time
	}

	var r aggRow
	err := e.db.WithContext(ctx).
		Model(&model.SyncedOrder{}).
		Select(`customer_email,
			MAX(customer_name) AS customer_name,
			COUNT(*) AS order_count,
			SUM(total_price) AS total_spent,
			MIN(COALESCE(order_created_at, created_at)) AS first_order_at,
			MAX(COALESCE(order_created_at, created_at)) AS last_order_at`).
		Where("shop_id = ? AND customer_email = ?", shopID, email).
		Group("customer_email").
		First(&r).Error
	if err != nil {
		return nil, err
	}

	return &orderAggRow{
		CustomerEmail: r.CustomerEmail,
		CustomerName:  r.CustomerName,
		OrderCount:    r.OrderCount,
		TotalSpent:    r.TotalSpent,
		FirstOrderAt:  r.FirstOrderAt,
		LastOrderAt:   r.LastOrderAt,
	}, nil
}

// fetchAllOrderRows fetches all individual orders for trend calculation.
func (e *CustomerIntelligenceEngine) fetchAllOrderRows(ctx context.Context, shopID uuid.UUID) ([]singleOrderRow, error) {
	var rows []singleOrderRow
	err := e.db.WithContext(ctx).
		Model(&model.SyncedOrder{}).
		Select("customer_email, total_price, COALESCE(order_created_at, created_at) AS order_created_at").
		Where("shop_id = ? AND customer_email != ''", shopID).
		Order("COALESCE(order_created_at, created_at) ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// fetchOrderRowsForEmail fetches individual orders for a single customer.
func (e *CustomerIntelligenceEngine) fetchOrderRowsForEmail(ctx context.Context, shopID uuid.UUID, email string) ([]singleOrderRow, error) {
	var rows []singleOrderRow
	err := e.db.WithContext(ctx).
		Model(&model.SyncedOrder{}).
		Select("customer_email, total_price, COALESCE(order_created_at, created_at) AS order_created_at").
		Where("shop_id = ? AND customer_email = ?", shopID, email).
		Order("COALESCE(order_created_at, created_at) ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// computeInsight computes the full insight from aggregated data and individual orders.
func (e *CustomerIntelligenceEngine) computeInsight(agg orderAggRow, orders []singleOrderRow) model.CustomerInsight {
	totalOrders := agg.OrderCount
	totalSpent := agg.TotalSpent
	now := time.Now().UTC()

	insight := model.CustomerInsight{
		CustomerEmail: agg.CustomerEmail,
		CustomerName:  agg.CustomerName,
		TotalOrders:   totalOrders,
		TotalSpent:    totalSpent,
		DataPoints:    totalOrders,
	}

	if totalOrders > 0 {
		avgOrderValue := totalSpent / float64(totalOrders)
		insight.AvgOrderValue = math.Round(avgOrderValue*100) / 100
	}

	firstOrder := agg.FirstOrderAt
	lastOrder := agg.LastOrderAt
	insight.FirstOrderAt = &firstOrder
	insight.LastOrderAt = &lastOrder

	// Conservative estimate for data_points < 3
	if totalOrders < 3 {
		insight.PredictedLTV = totalSpent
		insight.ActiveProbability = 0.5
		insight.ChurnRiskScore = 30
		insight.ChurnRiskLevel = "low"
		insight.Confidence = "low"

		// Still compute recent/early values if possible
		if len(orders) > 0 {
			insight.AvgRecentValue = avgTopN(orders, totalOrders, false)
			insight.AvgEarlyValue = avgTopN(orders, totalOrders, true)
		}
		return insight
	}

	// Sort orders by time (ascending) for trend calculation
	sort.Slice(orders, func(i, j int) bool {
		return orders[i].OrderCreatedAt.Before(orders[j].OrderCreatedAt)
	})

	// Compute interval
	firstOrderAt := orders[0].OrderCreatedAt
	lastOrderAt := orders[len(orders)-1].OrderCreatedAt
	totalDays := lastOrderAt.Sub(firstOrderAt).Hours() / 24
	var avgIntervalDays float64
	if totalOrders > 1 {
		avgIntervalDays = totalDays / float64(totalOrders-1)
	}
	if avgIntervalDays <= 0 {
		avgIntervalDays = 30 // fallback: assume monthly
	}
	insight.AvgOrderIntervalDays = math.Round(avgIntervalDays*100) / 100

	// Days since last order
	daysSinceLast := now.Sub(lastOrderAt).Hours() / 24
	if daysSinceLast < 0 {
		daysSinceLast = 0
	}

	// Active probability
	activeProb := 1.0 - math.Min(daysSinceLast/avgIntervalDays, 1.0)
	insight.ActiveProbability = math.Round(activeProb*100) / 100

	// Predicted LTV
	avgOrderValue := totalSpent / float64(totalOrders)
	futurePurchases := activeProb * (1080.0 / avgIntervalDays) // 36 months = 1080 days
	predictedLTV := totalSpent + (avgOrderValue * futurePurchases)
	insight.PredictedLTV = math.Round(predictedLTV*100) / 100

	// Trend: average of recent 3 vs earliest 3 orders
	avgRecent := avgTopN(orders, 3, false)
	avgEarly := avgTopN(orders, 3, true)
	insight.AvgRecentValue = math.Round(avgRecent*100) / 100
	insight.AvgEarlyValue = math.Round(avgEarly*100) / 100

	// Churn risk
	churnBase := math.Min(daysSinceLast/avgIntervalDays*50, 100)

	var decayRatio float64
	if avgEarly > 0 {
		decayRatio = math.Max(0, (avgEarly-avgRecent)/avgEarly)
	}
	churnModifier := math.Min(1.2, 1+decayRatio)
	churnScore := clamp(churnBase*churnModifier, 0, 100)
	insight.ChurnRiskScore = math.Round(churnScore*100) / 100

	// Churn risk level
	switch {
	case churnScore <= 30:
		insight.ChurnRiskLevel = "low"
	case churnScore <= 60:
		insight.ChurnRiskLevel = "medium"
	default:
		insight.ChurnRiskLevel = "high"
	}

	insight.Confidence = "high"
	return insight
}

// avgTopN returns the average of the top N orders by price.
// If earliest is true, takes the first N (oldest); otherwise the last N (newest).
func avgTopN(orders []singleOrderRow, n int, earliest bool) float64 {
	if len(orders) == 0 {
		return 0
	}

	count := n
	if count > len(orders) {
		count = len(orders)
	}
	if count <= 0 {
		return 0
	}

	var sum float64
	if earliest {
		for i := 0; i < count; i++ {
			sum += orders[i].TotalPrice
		}
	} else {
		for i := len(orders) - count; i < len(orders); i++ {
			sum += orders[i].TotalPrice
		}
	}
	return sum / float64(count)
}

// upsertInsight upserts a customer insight record.
func (e *CustomerIntelligenceEngine) upsertInsight(ctx context.Context, insight *model.CustomerInsight) error {
	return e.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "shop_id"}, {Name: "customer_email"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"customer_name", "total_orders", "total_spent", "avg_order_value",
			"last_order_at", "first_order_at", "avg_order_interval_days",
			"predicted_ltv", "churn_risk_score", "churn_risk_level",
			"active_probability", "avg_recent_value", "avg_early_value",
			"confidence", "data_points", "computed_at", "updated_at",
		}),
	}).Create(insight).Error
}

// clamp constrains a float64 to [minVal, maxVal].
func clamp(val, minVal, maxVal float64) float64 {
	if val < minVal {
		return minVal
	}
	if val > maxVal {
		return maxVal
	}
	return val
}
