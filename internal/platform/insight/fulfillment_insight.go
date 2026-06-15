// Package insight provides the Insight Engine with fulfillment stat tracking.
package insight

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
)

// FulfillmentInsight tracks fulfillment stats via EventBus and provides aggregation APIs.
type FulfillmentInsight struct {
	db       *gorm.DB
	eventBus eventbus.EventBus
	unsub    func()
}

// NewFulfillmentInsight creates a new FulfillmentInsight consumer.
func NewFulfillmentInsight(db *gorm.DB, eb eventbus.EventBus) *FulfillmentInsight {
	return &FulfillmentInsight{
		db:       db,
		eventBus: eb,
	}
}

// Start subscribes to fulfillment.status_changed events.
func (fi *FulfillmentInsight) Start(ctx context.Context) error {
	unsub, err := fi.eventBus.Subscribe(eventbus.EventFulfillmentStatusChanged, fi.onStatusChanged)
	if err != nil {
		return fmt.Errorf("fulfillment_insight: subscribe: %w", err)
	}
	fi.unsub = unsub
	slog.Info("fulfillment_insight: subscribed to fulfillment.status_changed")
	return nil
}

// Shutdown unsubscribes from the EventBus.
func (fi *FulfillmentInsight) Shutdown() {
	if fi.unsub != nil {
		fi.unsub()
	}
}

// onStatusChanged records fulfillment event stats for later aggregation.
func (fi *FulfillmentInsight) onStatusChanged(ctx context.Context, event *eventbus.Event) error {
	if event == nil || event.Payload == nil {
		return nil
	}

	// We don't need to store a separate table — the fulfillment_events table already
	// has all the data. This consumer exists so the insight engine can do real-time
	// anomaly detection (e.g., spike in FAILURE events).
	var payload struct {
		Status  string `json:"status"`
		Carrier string `json:"carrier"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return nil
	}

	slog.Debug("fulfillment_insight: event received",
		"shop_id", event.ShopID,
		"status", payload.Status,
		"carrier", payload.Carrier,
	)

	return nil
}

// ── Stats aggregation API ────────────────────────────────────────────────────

// CarrierStat holds per-carrier delivery time statistics.
type CarrierStat struct {
	Carrier        string  `json:"carrier"`
	TotalOrders    int64   `json:"total_orders"`
	AvgDeliveryHours float64 `json:"avg_delivery_hours"`
	OnTimeRate     float64 `json:"on_time_rate"` // % of orders delivered on/before estimated
}

// AnomalyStat holds anomaly rate stats.
type AnomalyStat struct {
	TotalEvents     int64   `json:"total_events"`
	AnomalyEvents   int64   `json:"anomaly_events"`
	AnomalyRate     float64 `json:"anomaly_rate"`
}

// StatusDistribution holds count per status.
type StatusDistribution struct {
	Status string `json:"status"`
	Count  int64  `json:"count"`
}

// FulfillmentStats holds aggregated fulfillment statistics.
type FulfillmentStats struct {
	CarrierStats       []CarrierStat        `json:"carrier_stats"`
	Anomaly            AnomalyStat           `json:"anomaly"`
	StatusDistribution []StatusDistribution  `json:"status_distribution"`
}

// ComputeFulfillmentStats computes aggregate fulfillment stats for a shop over the last N days.
func (fi *FulfillmentInsight) ComputeFulfillmentStats(ctx context.Context, shopID uuid.UUID, days int) (*FulfillmentStats, error) {
	if fi.db == nil {
		return nil, fmt.Errorf("fulfillment_insight: no database configured")
	}

	since := time.Now().AddDate(0, 0, -days)

	stats := &FulfillmentStats{}

	// 1. Carrier stats: average delivery time per carrier
	//    Measured from first LABEL_PRINTED to DELIVERED for each order
	type carrierRow struct {
		Carrier     string
		TotalOrders int64
		AvgHours    float64
	}
	var carrierRows []carrierRow

	// Join synced_orders for carrier info + fulfillment_events for timing
	fi.db.WithContext(ctx).Raw(`
		WITH delivered_orders AS (
			SELECT fe.order_id,
			       MIN(fe.happened_at) FILTER (WHERE fe.status = 'LABEL_PRINTED') AS label_time,
			       MIN(fe.happened_at) FILTER (WHERE fe.status = 'DELIVERED')  AS delivered_time
			FROM fulfillment_events fe
			WHERE fe.shop_id = ?
			  AND fe.happened_at > ?
			GROUP BY fe.order_id
			HAVING MIN(fe.happened_at) FILTER (WHERE fe.status = 'LABEL_PRINTED') IS NOT NULL
			   AND MIN(fe.happened_at) FILTER (WHERE fe.status = 'DELIVERED') IS NOT NULL
		)
		SELECT COALESCE(so.carrier, 'Unknown') AS carrier,
		       COUNT(*)                          AS total_orders,
		       AVG(EXTRACT(EPOCH FROM (do.delivered_time - do.label_time)) / 3600.0) AS avg_hours
		FROM delivered_orders do
		JOIN synced_orders so ON so.id = do.order_id AND so.shop_id = ?
		WHERE so.carrier != ''
		GROUP BY so.carrier
		ORDER BY total_orders DESC
	`, shopID, since, shopID).Scan(&carrierRows)

	for _, cr := range carrierRows {
		stats.CarrierStats = append(stats.CarrierStats, CarrierStat{
			Carrier:          cr.Carrier,
			TotalOrders:      cr.TotalOrders,
			AvgDeliveryHours: cr.AvgHours,
		})
	}

	// 2. Anomaly rate: (ATTEMPTED_DELIVERY + FAILURE) / total events
	var totalEvents, anomalyEvents int64
	fi.db.WithContext(ctx).Raw(`
		SELECT COUNT(*)
		FROM fulfillment_events
		WHERE shop_id = ? AND happened_at > ?
	`, shopID, since).Scan(&totalEvents)

	fi.db.WithContext(ctx).Raw(`
		SELECT COUNT(*)
		FROM fulfillment_events
		WHERE shop_id = ? AND happened_at > ? AND status IN ('ATTEMPTED_DELIVERY', 'FAILURE')
	`, shopID, since).Scan(&anomalyEvents)

	stats.Anomaly = AnomalyStat{
		TotalEvents:   totalEvents,
		AnomalyEvents: anomalyEvents,
	}
	if totalEvents > 0 {
		stats.Anomaly.AnomalyRate = float64(anomalyEvents) / float64(totalEvents) * 100
	}

	// 3. Status distribution over last 7 days
	type statusRow struct {
		Status string
		Count  int64
	}
	var statusRows []statusRow
	sevenDaysAgo := time.Now().AddDate(0, 0, -7)
	fi.db.WithContext(ctx).Raw(`
		SELECT status, COUNT(*) AS count
		FROM fulfillment_events
		WHERE shop_id = ? AND happened_at > ?
		GROUP BY status
		ORDER BY count DESC
	`, shopID, sevenDaysAgo).Scan(&statusRows)

	for _, sr := range statusRows {
		stats.StatusDistribution = append(stats.StatusDistribution, StatusDistribution{
			Status: sr.Status,
			Count:  sr.Count,
		})
	}

	return stats, nil
}

// ComputeRecentUpdates returns a human-readable summary of recent fulfillment updates for chat context injection.
// Returns a slice of strings like "#1001 IN_TRANSIT (Shanghai), #1002 DELIVERED"
func (fi *FulfillmentInsight) ComputeRecentUpdates(ctx context.Context, shopID uuid.UUID) ([]string, error) {
	if fi.db == nil {
		return nil, nil
	}

	type updateRow struct {
		OrderNumber int
		Status      string
		City        string
	}
	var rows []updateRow

	// Get distinct orders with their latest status in the last 24h
	fi.db.WithContext(ctx).Raw(`
		SELECT DISTINCT ON (so.order_number)
		       so.order_number,
		       fe.status,
		       fe.city
		FROM fulfillment_events fe
		JOIN synced_orders so ON so.id = fe.order_id
		WHERE fe.shop_id = ?
		  AND fe.happened_at > NOW() - INTERVAL '24 hours'
		ORDER BY so.order_number, fe.happened_at DESC
		LIMIT 10
	`, shopID).Scan(&rows)

	var updates []string
	for _, r := range rows {
		loc := ""
		if r.City != "" {
			loc = fmt.Sprintf(" (%s)", r.City)
		}
		updates = append(updates, fmt.Sprintf("#%d %s%s", r.OrderNumber, r.Status, loc))
	}

	return updates, nil
}

// SetEventBus injects the EventBus (needed for future event publishing).
func (fi *FulfillmentInsight) SetEventBus(bus eventbus.EventBus) {
	fi.eventBus = bus
}
