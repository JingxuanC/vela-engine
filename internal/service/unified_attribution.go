package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// UnifiedAttributionService computes cross-channel attribution with equal-split deduplication.
type UnifiedAttributionService struct {
	db *gorm.DB
}

// NewUnifiedAttributionService creates a new UnifiedAttributionService.
func NewUnifiedAttributionService(db *gorm.DB) *UnifiedAttributionService {
	return &UnifiedAttributionService{db: db}
}

// UnifiedAttributionResult is the top-level API response.
type UnifiedAttributionResult struct {
	TotalActualRevenue     float64              `json:"total_actual_revenue"`
	TotalAttributedRevenue float64              `json:"total_attributed_revenue"`
	OverlapRate            float64              `json:"overlap_rate"`
	Channels               []ChannelAttribution `json:"channels"`
	OverlapMatrix          []OverlapPair        `json:"overlap_matrix"`
}

// ChannelAttribution represents per-channel aggregated metrics.
type ChannelAttribution struct {
	Channel           string  `json:"channel"`
	AttributedOrders  int64   `json:"attributed_orders"`
	AttributedRevenue float64 `json:"attributed_revenue"`
	SharedRevenue     float64 `json:"shared_revenue"`
}

// OverlapPair represents pairwise overlap between two or three channels.
type OverlapPair struct {
	Pair    string  `json:"pair"`
	Orders  int64   `json:"orders"`
	Revenue float64 `json:"revenue"`
}

// GetUnifiedAttribution computes unified cross-channel attribution for a shop.
func (s *UnifiedAttributionService) GetUnifiedAttribution(ctx context.Context, shopID uuid.UUID, days int) (*UnifiedAttributionResult, error) {
	if s.db == nil {
		return nil, fmt.Errorf("unified_attribution: database not available")
	}

	shopIDStr := shopID.String()

	// ── 1. Total actual revenue from synced_orders ──
	var totalActualRevenue float64
	if err := s.db.WithContext(ctx).Raw(
		`SELECT COALESCE(SUM(total_price), 0) FROM synced_orders
		 WHERE shop_id = ? AND created_at > now() - make_interval(days => ?)`,
		shopIDStr, days,
	).Scan(&totalActualRevenue).Error; err != nil {
		slog.Error("unified_attribution: total actual revenue query failed", "shop_id", shopIDStr, "error", err)
		return nil, fmt.Errorf("unified_attribution: query total actual revenue: %w", err)
	}

	// ── 2. Channel-level equal-split aggregation ──
	type channelRow struct {
		Channel           string
		AttributedOrders  int64
		AttributedRevenue float64
		SharedRevenue     float64
	}

	var channelRows []channelRow
	channelSQL := fmt.Sprintf(`
		WITH raw AS (
			SELECT 'cart_recovery' AS channel, order_id::varchar AS order_id,
			       shop_id::uuid, order_total AS revenue
			FROM cart_recovery_codes
			WHERE is_used = true AND shop_id = ? AND used_at > now() - make_interval(days => %d)

			UNION ALL

			SELECT 'content' AS channel, order_id,
			       shop_id::uuid, order_value AS revenue
			FROM analytics_events
			WHERE event_type = 'purchase' AND channel = 'content'
			  AND shop_id = ? AND created_at > now() - make_interval(days => %d)

			UNION ALL

			SELECT 'review' AS channel, order_id,
			       shop_id::uuid, revenue
			FROM review_revenue_attributions
			WHERE shop_id = ? AND attributed_at > now() - make_interval(days => %d)
		),
		with_counts AS (
			SELECT *, COUNT(*) OVER (PARTITION BY shop_id, order_id) AS channel_count
			FROM raw
		),
		split AS (
			SELECT channel, order_id, shop_id,
			       ROUND(revenue::numeric / channel_count, 2) AS split_revenue,
			       channel_count
			FROM with_counts
		)
		SELECT channel,
		       COUNT(DISTINCT order_id) AS attributed_orders,
		       COALESCE(SUM(split_revenue), 0) AS attributed_revenue,
		       COALESCE(SUM(CASE WHEN channel_count > 1 THEN split_revenue ELSE 0 END), 0) AS shared_revenue
		FROM split
		GROUP BY channel
		ORDER BY channel
	`, days, days, days)

	if err := s.db.WithContext(ctx).Raw(channelSQL, shopIDStr, shopIDStr, shopIDStr).Scan(&channelRows).Error; err != nil {
		slog.Error("unified_attribution: channel query failed", "shop_id", shopIDStr, "error", err)
		return nil, fmt.Errorf("unified_attribution: channel aggregation: %w", err)
	}

	// ── 3. Overlap matrix ──
	type overlapRow struct {
		Pair    string
		Orders  int64
		Revenue float64
	}

	var overlapRows []overlapRow

	type overlapPair struct {
		label string
		a     string
		b     string
	}
	pairs := []overlapPair{
		{"cart_recovery + content", "cart_recovery", "content"},
		{"cart_recovery + review", "cart_recovery", "review"},
		{"content + review", "content", "review"},
	}

	rawCTE := fmt.Sprintf(`
		WITH raw AS (
			SELECT 'cart_recovery' AS channel, order_id::varchar AS order_id,
			       shop_id::uuid, order_total AS revenue
			FROM cart_recovery_codes
			WHERE is_used = true AND shop_id = ? AND used_at > now() - make_interval(days => %d)

			UNION ALL

			SELECT 'content' AS channel, order_id,
			       shop_id::uuid, order_value AS revenue
			FROM analytics_events
			WHERE event_type = 'purchase' AND channel = 'content'
			  AND shop_id = ? AND created_at > now() - make_interval(days => %d)

			UNION ALL

			SELECT 'review' AS channel, order_id,
			       shop_id::uuid, revenue
			FROM review_revenue_attributions
			WHERE shop_id = ? AND attributed_at > now() - make_interval(days => %d)
		)
	`, days, days, days)

	for _, p := range pairs {
		sql := rawCTE + fmt.Sprintf(`
			SELECT '%s' AS pair,
			       COUNT(DISTINCT a.order_id) AS orders,
			       COALESCE(SUM(LEAST(a.revenue, b.revenue)), 0) AS revenue
			FROM (
				SELECT DISTINCT order_id, shop_id, revenue FROM raw WHERE channel = '%s'
			) a
			INNER JOIN (
				SELECT DISTINCT order_id, shop_id, revenue FROM raw WHERE channel = '%s'
			) b ON a.order_id = b.order_id AND a.shop_id = b.shop_id
		`, p.label, p.a, p.b)

		var row overlapRow
		if err := s.db.WithContext(ctx).Raw(sql, shopIDStr, shopIDStr, shopIDStr).Scan(&row).Error; err != nil {
			slog.Error("unified_attribution: overlap query failed", "pair", p.label, "error", err)
			return nil, fmt.Errorf("unified_attribution: overlap %s: %w", p.label, err)
		}
		overlapRows = append(overlapRows, row)
	}

	// Three-way overlap
	threeWaySQL := rawCTE + `
		SELECT 'all three' AS pair,
		       COUNT(DISTINCT a.order_id) AS orders,
		       COALESCE(SUM(LEAST(LEAST(a.revenue, b.revenue), c.revenue)), 0) AS revenue
		FROM (
			SELECT DISTINCT order_id, shop_id, revenue FROM raw WHERE channel = 'cart_recovery'
		) a
		INNER JOIN (
			SELECT DISTINCT order_id, shop_id, revenue FROM raw WHERE channel = 'content'
		) b ON a.order_id = b.order_id AND a.shop_id = b.shop_id
		INNER JOIN (
			SELECT DISTINCT order_id, shop_id, revenue FROM raw WHERE channel = 'review'
		) c ON a.order_id = c.order_id AND a.shop_id = c.shop_id
	`

	var threeWay overlapRow
	if err := s.db.WithContext(ctx).Raw(threeWaySQL, shopIDStr, shopIDStr, shopIDStr).Scan(&threeWay).Error; err != nil {
		slog.Error("unified_attribution: three-way overlap query failed", "shop_id", shopIDStr, "error", err)
		return nil, fmt.Errorf("unified_attribution: three-way overlap: %w", err)
	}
	overlapRows = append(overlapRows, threeWay)

	// ── 4. Assemble result ──
	channels := make([]ChannelAttribution, len(channelRows))
	var totalAttributedRevenue float64
	for i, r := range channelRows {
		channels[i] = ChannelAttribution{
			Channel:           r.Channel,
			AttributedOrders:  r.AttributedOrders,
			AttributedRevenue: r.AttributedRevenue,
			SharedRevenue:     r.SharedRevenue,
		}
		totalAttributedRevenue += r.AttributedRevenue
	}

	matrix := make([]OverlapPair, len(overlapRows))
	for i, r := range overlapRows {
		matrix[i] = OverlapPair{
			Pair:    r.Pair,
			Orders:  r.Orders,
			Revenue: r.Revenue,
		}
	}

	// Compute overlap rate
	var overlapRate float64
	if totalActualRevenue > 0 && totalAttributedRevenue > 0 {
		overlapRate = (1 - totalActualRevenue/totalAttributedRevenue) * 100
	}

	return &UnifiedAttributionResult{
		TotalActualRevenue:     totalActualRevenue,
		TotalAttributedRevenue: totalAttributedRevenue,
		OverlapRate:            overlapRate,
		Channels:               channels,
		OverlapMatrix:          matrix,
	}, nil
}
