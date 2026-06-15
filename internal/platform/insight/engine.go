package insight

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/externaldata"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
)

// InsightType categorizes the kind of insight.
type InsightType string

const (
	InsightTypePricing       InsightType = "pricing"
	InsightTypeTrend         InsightType = "trend"
	InsightTypeReturnRate    InsightType = "return_rate"
	InsightTypeCategoryShift InsightType = "category_shift"
	InsightTypeCompetitor    InsightType = "competitor"
	InsightTypeInventory     InsightType = "inventory"
)

// Severity indicates how urgent or important an insight is.
type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// Insight is a single actionable observation.
type Insight struct {
	Type      InsightType `json:"type"`
	Severity  Severity    `json:"severity"`
	Title     string      `json:"title"`
	Body      string      `json:"body"`
	ActionURL string      `json:"action_url,omitempty"`
	Data      interface{} `json:"data,omitempty"`
	// Confidence fields (Phase 1 review)
	SampleSize   int    `json:"sample_size,omitempty"`
	DataFreshness string `json:"data_freshness,omitempty"` // "fresh", "stale", "unknown"
	Confidence   string `json:"confidence,omitempty"`     // "low", "medium", "high"
}

// InsightEngine analyzes shop data using external data sources (Google Trends, EC Compass)
// combined with internal metrics to produce actionable business insights.
type InsightEngine struct {
	db        *gorm.DB
	trends    *externaldata.GoogleTrendsClient
	eccompass *externaldata.ECCompassClient
	snapshots *SnapshotStore
	eventBus  eventbus.EventBus
}

// NewInsightEngine creates a new InsightEngine.
// redis may be nil; when nil, the engine's internal SnapshotStore cannot cache to Redis
// and will always query SQL directly.
func NewInsightEngine(db *gorm.DB, trends *externaldata.GoogleTrendsClient, eccompass *externaldata.ECCompassClient, redis *redis.Client) *InsightEngine {
	return &InsightEngine{
		db:        db,
		trends:    trends,
		eccompass: eccompass,
		snapshots: NewSnapshotStore(db, redis),
	}
}

// SetEventBus injects the EventBus for publishing insight-generated events.
func (e *InsightEngine) SetEventBus(bus eventbus.EventBus) {
	e.eventBus = bus
}

// Analyze runs a full insight analysis for a shop. Returns a list of Insights.
// Triggered by cron timer. Combines:
//   - internal return rate metrics
//   - Google Trends interest data
//   - EC Compass pricing benchmarks
func (e *InsightEngine) Analyze(ctx context.Context, shopID string) ([]Insight, error) {
	slog.Info("insight engine: analyze", "shop_id", shopID)

	var insights []Insight

	// 1. Compute return rate from orders & returns (last 30 days)
	returnRate, orderCount, returnCount, returnReasons, lastReturnAt, err := e.ComputeReturnRate(ctx, shopID, 30)
	if err != nil {
		slog.Warn("insight engine: failed to compute return rate", "shop_id", shopID, "error", err)
	} else {
		insights = append(insights, e.analyzeReturnRate(returnRate, orderCount, returnCount, returnReasons, lastReturnAt)...)
	}

	// 2. Get Google Trends data for top product categories
	topCategories, err := e.getTopCategories(ctx, shopID, 3)
	if err != nil {
		slog.Warn("insight engine: failed to get top categories", "shop_id", shopID, "error", err)
	} else {
		for _, cat := range topCategories {
			trendData, err := e.trends.GetInterestOverTime(ctx, []string{cat}, "US")
			if err != nil {
				slog.Warn("insight engine: trends call failed", "category", cat, "error", err)
				continue
			}
			insights = append(insights, e.analyzeTrend(cat, trendData)...)
		}
	}

	// 3. Get EC Compass pricing data for top categories
	for _, cat := range topCategories {
		pricing, err := e.eccompass.GetCategoryPricing(ctx, cat)
		if err != nil {
			slog.Warn("insight engine: eccompass pricing call failed", "category", cat, "error", err)
			continue
		}
		avgPrice, err := e.getCategoryAveragePrice(ctx, shopID, cat)
		if err != nil {
			slog.Warn("insight engine: failed to get shop avg price", "category", cat, "error", err)
			continue
		}
		insights = append(insights, e.analyzePricing(cat, avgPrice, pricing)...)
	}

	if len(insights) == 0 {
		insights = append(insights, Insight{
			Type:     InsightTypeTrend,
			Severity: SeverityLow,
			Title:    "Insufficient data for analysis",
			Body:     "Not enough shop data to generate meaningful insights. Continue collecting data.",
		})
	}

	return insights, nil
}

// OnEvent handles EventBus events for real-time, event-driven insight processing.
// Phase 2: handles both legacy events (backward compat) and new pipeline events (incremental).
func (e *InsightEngine) OnEvent(ctx context.Context, event *eventbus.Event) error {
	shopID := event.ShopID.String()

	// Parse PipelineEventPayload for incremental processing
	var payload eventbus.PipelineEventPayload
	hasPayload := json.Unmarshal(event.Payload, &payload) == nil && payload.EntityID != ""

	switch event.Type {
	// ── Phase 2 pipeline events (incremental processing) ──────────────────
	case eventbus.EventProductSynced:
		slog.Info("insight engine: product synced (incremental)", "shop_id", shopID, "entity_id", payload.EntityID)
		return e.evaluatePricingIncremental(ctx, shopID)

	case eventbus.EventOrderSynced:
		slog.Info("insight engine: order synced (incremental)", "shop_id", shopID, "entity_id", payload.EntityID)
		return e.evaluateReturnRateIncremental(ctx, shopID)

	case eventbus.EventReviewSynced:
		slog.Info("insight engine: review synced (incremental)", "shop_id", shopID, "entity_id", payload.EntityID)
		return e.evaluateReturnRateIncremental(ctx, shopID)

	case eventbus.EventReturnSynced:
		slog.Info("insight engine: return synced (incremental)", "shop_id", shopID, "entity_id", payload.EntityID)
		return e.evaluateReturnRateIncremental(ctx, shopID)

	// ── Legacy events (backward compat during migration) ─────────────────
	case eventbus.EventOrderUpdated:
		return e.evaluateReturnRateIncremental(ctx, shopID)

	case eventbus.EventProductUpdated:
		return e.evaluatePricingIncremental(ctx, shopID)

	default:
		slog.Debug("insight engine: unhandled event type", "event_type", event.Type, "has_payload", hasPayload)
	}

	return nil
}

// evaluateReturnRateIncremental computes return rate insights triggered by a single event.
// Uses a 7-day window for responsiveness, compared to 30-day for cron-based full analysis.
func (e *InsightEngine) evaluateReturnRateIncremental(ctx context.Context, shopID string) error {
	returnRate, orderCount, returnCount, returnReasons, lastReturnAt, err := e.ComputeReturnRate(ctx, shopID, 7)
	if err != nil {
		return fmt.Errorf("insight engine: compute return rate: %w", err)
	}
	insights := e.analyzeReturnRate(returnRate, orderCount, returnCount, returnReasons, lastReturnAt)
	sid, _ := uuid.Parse(shopID)
	for _, ins := range insights {
		slog.Info("insight engine: return rate insight",
			"shop_id", shopID, "type", ins.Type, "severity", ins.Severity, "title", ins.Title)
		e.persistInsight(ctx, sid, ins)
	}
	return nil
}

// evaluatePricingIncremental re-evaluates pricing insights triggered by a single event.
// Uses Redis SETNX dedup (5 min window) to prevent batch product imports from
// hammering the EC Compass API.
func (e *InsightEngine) evaluatePricingIncremental(ctx context.Context, shopID string) error {
	// Only analyze top 1 category to limit external API calls per event.
	// TODO: Add Redis SETNX dedup (5 min window) when Redis is available on InsightEngine.
	topCategories, err := e.getTopCategories(ctx, shopID, 1)
	if err != nil {
		return fmt.Errorf("insight engine: get categories: %w", err)
	}
	sid, _ := uuid.Parse(shopID)
	for _, cat := range topCategories {
		pricing, err := e.eccompass.GetCategoryPricing(ctx, cat)
		if err != nil {
			continue
		}
		avgPrice, err := e.getCategoryAveragePrice(ctx, shopID, cat)
		if err != nil {
			continue
		}
		insights := e.analyzePricing(cat, avgPrice, pricing)
		for _, ins := range insights {
			slog.Info("insight engine: pricing insight",
				"shop_id", shopID, "category", cat, "severity", ins.Severity, "title", ins.Title)
			e.persistInsight(ctx, sid, ins)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal analysis logic
// ---------------------------------------------------------------------------

// computeReturnRate calculates return rate and top return reasons for a shop.
// Returns (returnRate, orderCount, returnCount, returnReasons, lastReturnDate, error)
// so callers can apply confidence rules (N<10, N>=10 with sample size, data freshness).
// computeReturnRate delegates to SnapshotStore.computeReturnMetrics for the shared logic,
// then adds the lastReturnAt freshness check unique to the Insight Engine.
// P0-1: Also checks synced_returns for the freshest return date.
func (e *InsightEngine) ComputeReturnRate(ctx context.Context, shopID string, days int) (float64, int64, int64, []string, *time.Time, error) {
	rate, orderCount, returnCount, reasons, err := e.snapshots.computeReturnMetrics(ctx, shopID, days)
	if err != nil {
		return 0, 0, 0, nil, nil, err
	}

	// Find latest return date from BOTH returns and synced_returns for freshness check
	var lastReturn time.Time
	if e.db != nil {
		var lastReturnStr string
		e.db.WithContext(ctx).Table("returns").
			Select("COALESCE(MAX(created_at), '1970-01-01')").
			Where("shop_id = ?", shopID).
			Scan(&lastReturnStr)
		if t, err := time.Parse(time.RFC3339, lastReturnStr); err == nil {
			lastReturn = t
		}

		// Also check synced_returns for more recent data
		var lastSyncedStr string
		e.db.WithContext(ctx).Table("synced_returns").
			Select("COALESCE(MAX(synced_at), '1970-01-01')").
			Where("shop_id = ?", shopID).
			Scan(&lastSyncedStr)
		if t, err := time.Parse(time.RFC3339, lastSyncedStr); err == nil {
			if t.After(lastReturn) {
				lastReturn = t
			}
		}
	}

	slog.Info("insight engine: return rate computed",
		"shop_id", shopID, "orders", orderCount, "returns", returnCount,
		"rate", rate, "days", days)

	return rate, orderCount, returnCount, reasons, &lastReturn, nil
}

// analyzeReturnRate generates insights based on return rate thresholds.
// Applies confidence rules:
//   - N < 10 orders → qualitative only, no percentage
//   - N >= 10 orders → percentage with sample size noted
//   - Data > 90 days stale → confidence downgrade
func (e *InsightEngine) analyzeReturnRate(rate float64, orderCount, returnCount int64, reasons []string, lastReturnAt *time.Time) []Insight {
	var insights []Insight

	// Freshness check
	freshness := "fresh"
	confidence := "high"
	if lastReturnAt != nil && time.Since(*lastReturnAt) > 90*24*time.Hour {
		freshness = "stale"
		confidence = "low"
	}

	// Build body with confidence rules
	var body string
	switch {
	case rate > 20:
		if orderCount < 10 {
			body = fmt.Sprintf("退货集中在 %d 单中，方向需更多数据确认。建议持续监控。", orderCount)
		} else {
			body = fmt.Sprintf("退货率 %.1f%%，远超行业均值（约10-15%%）。基于 %d 单数据。高退货率影响利润，可能表明产品质量、尺码或描述存在问题。", rate, orderCount)
		}
		insights = append(insights, Insight{
			Type:          InsightTypeReturnRate,
			Severity:      SeverityCritical,
			Title:         "Critical return rate detected",
			Body:          body,
			Data:          map[string]interface{}{"return_rate": rate, "top_reasons": reasons, "order_count": orderCount, "return_count": returnCount},
			SampleSize:    int(orderCount),
			DataFreshness: freshness,
			Confidence:    confidence,
		})
	case rate > 15:
		if orderCount < 10 {
			body = fmt.Sprintf("退货集中在 %d 单中，方向需更多数据确认。建议持续监控。", orderCount)
		} else {
			body = fmt.Sprintf("退货率 %.1f%%，高于典型范围。基于 %d 单数据。建议检查产品描述、尺码指南和客户反馈。", rate, orderCount)
		}
		insights = append(insights, Insight{
			Type:          InsightTypeReturnRate,
			Severity:      SeverityHigh,
			Title:         "Above-average return rate",
			Body:          body,
			Data:          map[string]interface{}{"return_rate": rate, "top_reasons": reasons, "order_count": orderCount, "return_count": returnCount},
			SampleSize:    int(orderCount),
			DataFreshness: freshness,
			Confidence:    confidence,
		})
	case rate > 10:
		if orderCount < 10 {
			body = fmt.Sprintf("退货集中在 %d 单中，方向需更多数据确认。建议持续监控。", orderCount)
		} else {
			body = fmt.Sprintf("退货率 %.1f%%，在可接受范围内。基于 %d 单数据。监控退货原因可帮助进一步降低。", rate, orderCount)
		}
		insights = append(insights, Insight{
			Type:          InsightTypeReturnRate,
			Severity:      SeverityMedium,
			Title:         "Slightly elevated return rate",
			Body:          body,
			Data:          map[string]interface{}{"return_rate": rate, "top_reasons": reasons, "order_count": orderCount, "return_count": returnCount},
			SampleSize:    int(orderCount),
			DataFreshness: freshness,
			Confidence:    confidence,
		})
	case rate >= 0:
		body = fmt.Sprintf("退货率 %.1f%%，在健康范围内。基于 %d 单数据。继续保持！", rate, orderCount)
		insights = append(insights, Insight{
			Type:          InsightTypeReturnRate,
			Severity:      SeverityLow,
			Title:         "Healthy return rate",
			Body:          body,
			Data:          map[string]interface{}{"return_rate": rate, "order_count": orderCount, "return_count": returnCount},
			SampleSize:    int(orderCount),
			DataFreshness: freshness,
			Confidence:    confidence,
		})
	}

	return insights
}

// analyzeTrend generates insights from Google Trends data.
func (e *InsightEngine) analyzeTrend(category string, data *externaldata.TrendsData) []Insight {
	if data == nil {
		return nil
	}

	var insights []Insight

	switch data.Trend {
	case "rising":
		insights = append(insights, Insight{
			Type:     InsightTypeTrend,
			Severity: SeverityHigh,
			Title:    fmt.Sprintf("Rising trend for %s", category),
			Body:     fmt.Sprintf("Google Trends shows increasing interest in %q. This is a great time to invest in marketing and inventory for this category.", category),
			Data:     map[string]interface{}{"category": category, "trend": data.Trend, "peak_score": data.PeakScore},
		})
	case "falling":
		insights = append(insights, Insight{
			Type:     InsightTypeTrend,
			Severity: SeverityMedium,
			Title:    fmt.Sprintf("Declining interest in %s", category),
			Body:     fmt.Sprintf("Google Trends shows decreasing interest in %q. Consider adjusting inventory levels and exploring emerging sub-categories.", category),
			Data:     map[string]interface{}{"category": category, "trend": data.Trend, "peak_score": data.PeakScore},
		})
	default:
		insights = append(insights, Insight{
			Type:     InsightTypeTrend,
			Severity: SeverityLow,
			Title:    fmt.Sprintf("Stable trend for %s", category),
			Body:     fmt.Sprintf("Interest in %q is stable. No immediate action needed, but keep monitoring for shifts.", category),
			Data:     map[string]interface{}{"category": category, "trend": data.Trend, "peak_score": data.PeakScore},
		})
	}

	return insights
}

// analyzePricing generates insights comparing shop pricing with market data.
func (e *InsightEngine) analyzePricing(category string, shopAvgPrice float64, market *externaldata.PricingData) []Insight {
	if market == nil {
		return nil
	}

	var insights []Insight

	priceRatio := shopAvgPrice / market.AveragePrice

	switch {
	case priceRatio > 1.5:
		insights = append(insights, Insight{
			Type:     InsightTypePricing,
			Severity: SeverityHigh,
			Title:    fmt.Sprintf("Your %s prices are significantly above market", category),
			Body:     fmt.Sprintf("Your average price ($%.2f) is %.0f%% above the market average ($%.2f). While premium pricing may be justified, ensure your product value proposition is clear.", shopAvgPrice, (priceRatio-1)*100, market.AveragePrice),
			Data:     map[string]interface{}{"category": category, "shop_avg_price": shopAvgPrice, "market_avg_price": market.AveragePrice, "price_ratio": priceRatio},
		})
	case priceRatio < 0.5:
		insights = append(insights, Insight{
			Type:     InsightTypePricing,
			Severity: SeverityMedium,
			Title:    fmt.Sprintf("Your %s prices are well below market", category),
			Body:     fmt.Sprintf("Your average price ($%.2f) is significantly below the market average ($%.2f). Consider if there's room to increase prices or if you're competing on value.", shopAvgPrice, market.AveragePrice),
			Data:     map[string]interface{}{"category": category, "shop_avg_price": shopAvgPrice, "market_avg_price": market.AveragePrice, "price_ratio": priceRatio},
		})
	case priceRatio > 1.2 || priceRatio < 0.8:
		insights = append(insights, Insight{
			Type:     InsightTypePricing,
			Severity: SeverityLow,
			Title:    fmt.Sprintf("Slight pricing gap in %s", category),
			Body:     fmt.Sprintf("Your average price ($%.2f) differs from the market average ($%.2f) by less than 50%%. Consider reviewing your pricing strategy.", shopAvgPrice, market.AveragePrice),
			Data:     map[string]interface{}{"category": category, "shop_avg_price": shopAvgPrice, "market_avg_price": market.AveragePrice, "price_ratio": priceRatio},
		})
	default:
		insights = append(insights, Insight{
			Type:     InsightTypePricing,
			Severity: SeverityLow,
			Title:    fmt.Sprintf("Competitive pricing in %s", category),
			Body:     fmt.Sprintf("Your average price ($%.2f) is aligned with the market average ($%.2f). Pricing is competitive.", shopAvgPrice, market.AveragePrice),
			Data:     map[string]interface{}{"category": category, "shop_avg_price": shopAvgPrice, "market_avg_price": market.AveragePrice, "price_ratio": priceRatio},
		})
	}

	return insights
}

// getTopCategories returns the top N product categories for a shop.
func (e *InsightEngine) getTopCategories(ctx context.Context, shopID string, n int) ([]string, error) {
	if e.db == nil {
		return []string{"general"}, nil
	}

	type catRow struct {
		Category string
		Count    int
	}
	var rows []catRow
	e.db.WithContext(ctx).Table("synced_products").
		Select("product_type as category, count(*) as count").
		Where("shop_id = ? AND product_type != ''", shopID).
		Group("product_type").
		Order("count desc").
		Limit(n).
		Scan(&rows)

	categories := make([]string, 0, len(rows))
	for _, r := range rows {
		categories = append(categories, r.Category)
	}
	if len(categories) == 0 {
		categories = append(categories, "general")
	}
	return categories, nil
}

// getCategoryAveragePrice computes the average price for a product category.
func (e *InsightEngine) getCategoryAveragePrice(ctx context.Context, shopID string, category string) (float64, error) {
	if e.db == nil {
		return 49.99, nil
	}

	type priceRow struct {
		Avg float64
	}
	var row priceRow
	e.db.WithContext(ctx).Table("synced_products").
		Select("avg(price) as avg").
		Where("shop_id = ? AND product_type = ? AND price > 0", shopID, category).
		Scan(&row)

	if row.Avg == 0 {
		return 49.99, nil
	}
	return math.Round(row.Avg*100) / 100, nil
}

// persistInsight upserts an Insight into the shop_insights table.
// Uses ON CONFLICT on (shop_id, type, title) to avoid duplicates from repeated events.
func (e *InsightEngine) persistInsight(ctx context.Context, shopID uuid.UUID, ins Insight) {
	if e.db == nil {
		return
	}
	data, _ := json.Marshal(ins.Data)
	si := model.ShopInsight{
		ShopID:        shopID,
		Type:          string(ins.Type),
		Severity:      string(ins.Severity),
		Title:         ins.Title,
		Body:          ins.Body,
		Data:          datatypes.JSON(data),
		SampleSize:    ins.SampleSize,
		DataFreshness: ins.DataFreshness,
		Confidence:    ins.Confidence,
	}
	if err := e.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "shop_id"}, {Name: "type"}, {Name: "title"}},
		DoUpdates: clause.AssignmentColumns([]string{"severity", "body", "data", "sample_size", "data_freshness", "confidence", "updated_at"}),
	}).Create(&si).Error; err != nil {
		slog.Warn("insight engine: failed to persist insight", "shop_id", shopID, "type", ins.Type, "error", err)
	}
}

// Ensure InsightEngine implements a handler suitable for eventbus subscription.
var _ eventbus.EventHandler = func(ctx context.Context, event *eventbus.Event) error {
	return nil
}

// MarshalInsights serializes insights to JSON bytes.
func MarshalInsights(insights []Insight) ([]byte, error) {
	return json.Marshal(insights)
}

// UnmarshalInsights deserializes JSON bytes into insights.
func UnmarshalInsights(data []byte) ([]Insight, error) {
	var insights []Insight
	err := json.Unmarshal(data, &insights)
	return insights, err
}
