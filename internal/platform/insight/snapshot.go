package insight

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
)

// ShopSnapshot is a per-shop aggregated data cache that replaces N individual
// DataFetcher SQL queries with a single Redis read. Refreshed daily by cron.
type ShopSnapshot struct {
	ShopID            string    `json:"shop_id"`
	GeneratedAt       time.Time `json:"generated_at"`

	// Return metrics
	ReturnRate        float64  `json:"return_rate_30d"`
	ReturnCount       int64    `json:"return_count_30d"`
	OrderCount        int64    `json:"order_count_30d"`
	TopReturnReasons  []string `json:"top_return_reasons"`

	// Product metrics
	TotalProducts     int64    `json:"total_products"`
	TopCategories     []string `json:"top_categories"`

	// Customer metrics
	TotalCustomers    int64    `json:"total_customers"`

	// Try-on metrics
	TotalTryOns       int64    `json:"total_tryons_30d"`
}

// SnapshotStore manages per-shop aggregated snapshots in Redis.
type SnapshotStore struct {
	db    *gorm.DB
	redis *redis.Client
}

// NewSnapshotStore creates a new SnapshotStore.
func NewSnapshotStore(db *gorm.DB, redis *redis.Client) *SnapshotStore {
	return &SnapshotStore{db: db, redis: redis}
}

const snapshotKeyPrefix = "shop:snapshot:"
const snapshotTTL = 25 * time.Hour // slightly over 24h so cron has a grace window

// Refresh computes and caches a fresh snapshot for a shop.
func (s *SnapshotStore) Refresh(ctx context.Context, shopID string) (*ShopSnapshot, error) {
	if s.db == nil && s.redis == nil {
		return nil, fmt.Errorf("snapshot store: no db or redis available")
	}
	snap := &ShopSnapshot{
		ShopID:      shopID,
		GeneratedAt: time.Now().UTC(),
	}

	// 1. Return metrics (30 days)
	snap.ReturnRate, snap.OrderCount, snap.ReturnCount, snap.TopReturnReasons, _ = s.computeReturnMetrics(ctx, shopID, 30)

	// 2-4. Product/Customer/TryOn metrics (skip if no DB)
	if s.db != nil {
		s.db.WithContext(ctx).Table("synced_products").
			Where("shop_id = ? AND status = ?", shopID, "active").
			Count(&snap.TotalProducts)

		type catRow struct {
			Category string
			Count    int
		}
		var rows []catRow
		s.db.WithContext(ctx).Table("synced_products").
			Select("product_type as category, count(*) as count").
			Where("shop_id = ? AND product_type != ''", shopID).
			Group("product_type").Order("count desc").Limit(5).
			Scan(&rows)
		for _, r := range rows {
			snap.TopCategories = append(snap.TopCategories, r.Category)
		}

		// 3. Customer count
		s.db.WithContext(ctx).Table("synced_customers").
			Where("shop_id = ?", shopID).
			Count(&snap.TotalCustomers)

		// 4. Try-on count (30 days)
		since := time.Now().UTC().AddDate(0, 0, -30)
		s.db.WithContext(ctx).Table("try_on_records").
			Where("shop_id = ? AND created_at >= ?", shopID, since).
			Count(&snap.TotalTryOns)
	}

	// Cache in Redis
	data, err := json.Marshal(snap)
	if err != nil {
		return snap, fmt.Errorf("snapshot marshal: %w", err)
	}
	if s.redis != nil {
		s.redis.Set(ctx, snapshotKeyPrefix+shopID, data, snapshotTTL)
	}
	slog.Info("insight: snapshot refreshed", "shop_id", shopID, "products", snap.TotalProducts, "customers", snap.TotalCustomers)
	return snap, nil
}

// Get retrieves the cached snapshot. Returns nil if not cached or expired.
func (s *SnapshotStore) Get(ctx context.Context, shopID string) *ShopSnapshot {
	if s.redis == nil {
		return nil
	}
	data, err := s.redis.Get(ctx, snapshotKeyPrefix+shopID).Bytes()
	if err != nil {
		return nil
	}
	var snap ShopSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil
	}
	return &snap
}

// GetOrRefresh returns cached snapshot or computes a fresh one.
func (s *SnapshotStore) GetOrRefresh(ctx context.Context, shopID string) *ShopSnapshot {
	if snap := s.Get(ctx, shopID); snap != nil {
		return snap
	}
	snap, err := s.Refresh(ctx, shopID)
	if err != nil {
		slog.Warn("insight: snapshot refresh failed", "shop_id", shopID, "error", err)
		return nil
	}
	return snap
}

// computeReturnMetrics returns return metrics for the snapshot.
// P0-1/P0-2 fix: counts return items (not return orders) from BOTH legacy returns+return_items
// AND Shopify synced_returns tables, unified on the item dimension for consistency with
// ReturnFetcher. Return rate = total_return_items / total_orders * 100.
func (s *SnapshotStore) computeReturnMetrics(ctx context.Context, shopID string, days int) (float64, int64, int64, []string, error) {
	if s.db == nil {
		return 0, 0, 0, nil, fmt.Errorf("db not available")
	}

	since := time.Now().UTC().AddDate(0, 0, -days)

	// Count total orders in the window
	var orderCount int64
	s.db.WithContext(ctx).Table("synced_orders").
		Where("shop_id = ? AND created_at >= ?", shopID, since).Count(&orderCount)

	// Count return items from BOTH sources (unified on item dimension)
	var totalReturnItems int64
	reasonAgg := make(map[string]int)

	// Source 1: Legacy returns + return_items (manual entries)
	var legacyItemCount int64
	s.db.WithContext(ctx).Table("return_items").
		Joins("JOIN returns ON returns.id = return_items.return_id").
		Where("returns.shop_id = ? AND returns.created_at >= ?", shopID, since).
		Count(&legacyItemCount)
	totalReturnItems += legacyItemCount

	// Legacy reasons
	type reasonRow struct {
		Reason string
		Count  int
	}
	var legacyReasons []reasonRow
	s.db.WithContext(ctx).Table("return_items").
		Select("return_items.reason, COUNT(*) as count").
		Joins("JOIN returns ON returns.id = return_items.return_id").
		Where("returns.shop_id = ? AND returns.created_at >= ? AND return_items.reason != ''", shopID, since).
		Group("return_items.reason").Order("count desc").Limit(10).
		Scan(&legacyReasons)
	for _, r := range legacyReasons {
		reasonAgg[r.Reason] += r.Count
	}

	// Source 2: Shopify synced_returns (webhook data)
	var syncedReturns []model.SyncedReturn
	s.db.WithContext(ctx).Where("shop_id = ? AND synced_at >= ? AND line_items IS NOT NULL", shopID, since).
		Find(&syncedReturns)
	for _, sr := range syncedReturns {
		var items []model.ReturnLineItemPayload
		if err := json.Unmarshal(sr.LineItems, &items); err != nil {
			continue
		}
		for _, li := range items {
			totalReturnItems += int64(li.Quantity)
			if li.ReturnReason != "" {
				reasonAgg[li.ReturnReason] += li.Quantity
			}
		}
	}

	// Rate: return items per order (consistent item-level dimension)
	rate := 0.0
	if orderCount > 0 {
		rate = float64(totalReturnItems) / float64(orderCount) * 100
	}

	// Sort merged reasons by count desc, take top 3
	type reasonPair struct {
		Reason string
		Count  int
	}
	sorted := make([]reasonPair, 0, len(reasonAgg))
	for reason, count := range reasonAgg {
		sorted = append(sorted, reasonPair{reason, count})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Count > sorted[j].Count })
	if len(sorted) > 3 {
		sorted = sorted[:3]
	}
	reasonStrs := make([]string, len(sorted))
	for i, r := range sorted {
		reasonStrs[i] = r.Reason
	}

	return rate, orderCount, totalReturnItems, reasonStrs, nil
}

// ── Cron integration ─────────────────────────────────────────────────────────

// RefreshAllSnapshots refreshes snapshots for all active shops.
func (s *SnapshotStore) RefreshAllSnapshots(ctx context.Context) (int, error) {
	if s.db == nil {
		return 0, fmt.Errorf("db not available")
	}

	var shopIDs []string
	s.db.WithContext(ctx).Table("shops").
		Where("uninstalled_at IS NULL").
		Pluck("id", &shopIDs)

	for _, shopID := range shopIDs {
		if _, err := s.Refresh(ctx, shopID); err != nil {
			slog.Warn("insight: snapshot refresh failed for shop", "shop_id", shopID, "error", err)
		}
	}

	slog.Info("insight: all snapshots refreshed", "shops", len(shopIDs))
	return len(shopIDs), nil
}
