package service

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
)

// RFMEngine computes RFM (Recency, Frequency, Monetary) customer segmentation.
type RFMEngine struct {
	db *gorm.DB
}

// NewRFMEngine creates a new RFM engine.
func NewRFMEngine(db *gorm.DB) *RFMEngine {
	return &RFMEngine{db: db}
}

// SegmentResult holds the statistics for one customer segment.
type SegmentResult struct {
	Count int     `json:"count"`
	Pct   float64 `json:"pct"`
}

// SegmentsOutput is the result of GET /api/customers/segments.
type SegmentsOutput struct {
	VIP     SegmentResult `json:"VIP"`
	Active  SegmentResult `json:"Active"`
	Dormant SegmentResult `json:"Dormant"`
	Lost    SegmentResult `json:"Lost"`
	New     SegmentResult `json:"New"`
}

// CustomerSegmentInfo is a single customer's detail in a segment.
type CustomerSegmentInfo struct {
	Email      string  `json:"email"`
	Name       string  `json:"name"`
	Orders     int     `json:"orders"`
	TotalSpent float64 `json:"total_spent"`
	LastOrder  string  `json:"last_order"`
	Segment    string  `json:"segment"`
}

// rfmRow is raw aggregated data from synced_orders.
type rfmRow struct {
	CustomerEmail string
	CustomerName  string
	OrderCount    int
	TotalSpent    float64
	LastOrderAt   time.Time
}

// ComputeSegments calculates RFM segmentation for a shop.
func (e *RFMEngine) ComputeSegments(ctx context.Context, shopID uuid.UUID) (*SegmentsOutput, error) {
	rows, err := e.aggregateOrders(ctx, shopID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	// Determine monetary threshold (top 10%)
	monetaryThreshold := e.calcMonetaryTop10(rows)

	var out SegmentsOutput
	total := len(rows)

	for _, r := range rows {
		daysSinceLastOrder := int(now.Sub(r.LastOrderAt).Hours() / 24)
		segment := e.classify(daysSinceLastOrder, r.OrderCount, r.TotalSpent, monetaryThreshold)

		switch segment {
		case "VIP":
			out.VIP.Count++
		case "Active":
			out.Active.Count++
		case "Dormant":
			out.Dormant.Count++
		case "Lost":
			out.Lost.Count++
		case "New":
			out.New.Count++
		}
	}

	if total > 0 {
		out.VIP.Pct = math.Round(float64(out.VIP.Count)/float64(total)*10000) / 100
		out.Active.Pct = math.Round(float64(out.Active.Count)/float64(total)*10000) / 100
		out.Dormant.Pct = math.Round(float64(out.Dormant.Count)/float64(total)*10000) / 100
		out.Lost.Pct = math.Round(float64(out.Lost.Count)/float64(total)*10000) / 100
		out.New.Pct = math.Round(float64(out.New.Count)/float64(total)*10000) / 100
	}

	return &out, nil
}

// GetSegmentCustomers returns the list of customers in a given segment.
func (e *RFMEngine) GetSegmentCustomers(ctx context.Context, shopID uuid.UUID, segment string) ([]CustomerSegmentInfo, error) {
	rows, err := e.aggregateOrders(ctx, shopID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	monetaryThreshold := e.calcMonetaryTop10(rows)

	var result []CustomerSegmentInfo
	for _, r := range rows {
		daysSinceLastOrder := int(now.Sub(r.LastOrderAt).Hours() / 24)
		s := e.classify(daysSinceLastOrder, r.OrderCount, r.TotalSpent, monetaryThreshold)
		if s == segment {
			result = append(result, CustomerSegmentInfo{
				Email:      r.CustomerEmail,
				Name:       r.CustomerName,
				Orders:     r.OrderCount,
				TotalSpent: math.Round(r.TotalSpent*100) / 100,
				LastOrder:  r.LastOrderAt.Format(time.RFC3339),
				Segment:    s,
			})
		}
	}

	return result, nil
}

// aggregateOrders aggregates order data per customer from synced_orders.
func (e *RFMEngine) aggregateOrders(ctx context.Context, shopID uuid.UUID) ([]rfmRow, error) {
	type aggRow struct {
		CustomerEmail string
		MaxCreatedAt  time.Time
		OrderCount    int
		TotalSpent    float64
	}

	var rows []aggRow
	err := e.db.WithContext(ctx).
		Model(&model.SyncedOrder{}).
		Select(`customer_email,
			MAX(COALESCE(order_created_at, created_at)) AS max_created_at,
			COUNT(*) AS order_count,
			SUM(total_price) AS total_spent`).
		Where("shop_id = ? AND customer_email != ''", shopID).
		Group("customer_email").
		Order("total_spent DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	// Also get customer names from synced_customers
	type nameRow struct {
		Email     string
		FirstName string
		LastName  string
	}
	var names []nameRow
	e.db.WithContext(ctx).
		Model(&model.SyncedCustomer{}).
		Select("email, first_name, last_name").
		Where("shop_id = ? AND email != ''", shopID).
		Find(&names)

	nameMap := make(map[string]string, len(names))
	for _, n := range names {
		nameMap[n.Email] = trimName(n.FirstName + " " + n.LastName)
	}

	result := make([]rfmRow, len(rows))
	for i, r := range rows {
		result[i] = rfmRow{
			CustomerEmail: r.CustomerEmail,
			CustomerName:  nameMap[r.CustomerEmail],
			OrderCount:    r.OrderCount,
			TotalSpent:    r.TotalSpent,
			LastOrderAt:   r.MaxCreatedAt,
		}
	}

	return result, nil
}

// calcMonetaryTop10 returns the top-10% monetary threshold.
func (e *RFMEngine) calcMonetaryTop10(rows []rfmRow) float64 {
	if len(rows) == 0 {
		return 0
	}
	spents := make([]float64, len(rows))
	for i, r := range rows {
		spents[i] = r.TotalSpent
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(spents)))

	top10Idx := int(math.Ceil(float64(len(rows)) * 0.10))
	if top10Idx == 0 {
		top10Idx = 1
	}
	if top10Idx > len(rows) {
		top10Idx = len(rows)
	}
	return spents[top10Idx-1]
}

// classify assigns a segment label based on RFM rules.
// Rules:
//   - New: first order within 7 days
//   - VIP: last order <30d, ≥3 orders, in top 10% monetary
//   - Active: last order <30d (not VIP)
//   - Dormant: last order 30-90d
//   - Lost: last order 90-180d
func (e *RFMEngine) classify(daysSinceLastOrder, orderCount int, totalSpent, monetaryTop10 float64) string {
	// New: first order within 7 days (only 1 order)
	if daysSinceLastOrder <= 7 && orderCount == 1 {
		return "New"
	}

	// VIP: <30d, ≥3 orders, top 10% monetary
	if daysSinceLastOrder < 30 && orderCount >= 3 && totalSpent >= monetaryTop10 && monetaryTop10 > 0 {
		return "VIP"
	}

	// Active: <30d (not VIP, not New)
	if daysSinceLastOrder < 30 {
		return "Active"
	}

	// Dormant: 30-90d
	if daysSinceLastOrder >= 30 && daysSinceLastOrder < 90 {
		return "Dormant"
	}

	// Lost: 90-180d
	if daysSinceLastOrder >= 90 && daysSinceLastOrder < 180 {
		return "Lost"
	}

	// Beyond 180d: still Lost (borderline)
	return "Lost"
}

func trimName(s string) string {
	if len(s) > 100 {
		s = s[:100]
	}
	return s
}
