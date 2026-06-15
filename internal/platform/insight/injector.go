// Package insight provides the context injection layer that enriches AI
// handler prompts with platform data (orders, returns, products, trends, etc.)
// so every AI feature is backed by real per-merchant data.
package insight

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/externaldata"
)

// DataFetcher fetches a piece of platform data and formats it as a prompt snippet.
type DataFetcher interface {
	// Name returns a unique name for this fetcher (used for registration / logging).
	Name() string
	// Fetch looks up data for the given shop and returns a formatted prompt snippet.
	// Returns empty string if no relevant data is found (graceful degradation).
	Fetch(ctx context.Context, db *gorm.DB, shopID string) string
}

// QuerySetter is an optional interface for fetchers that need a user query
// to perform semantic search (e.g., RAGFetcher).
type QuerySetter interface {
	SetQuery(query string)
}

// SourceSetter is an optional interface for fetchers that support source filtering
// (e.g., RAGFetcher can filter by "product", "review", "return", etc.).
// Consumers call SetSources before Fetch to limit results to relevant source types.
type SourceSetter interface {
	SetSources(sources []string)
}

// ContextInjector is a registry of DataFetchers that collectively build
// an enriched context string for AI prompts.
type ContextInjector struct {
	fetchers []DataFetcher
}

// NewContextInjector creates an empty injector.  Call Register() to add fetchers.
func NewContextInjector() *ContextInjector {
	return &ContextInjector{}
}

// Register adds a DataFetcher to the injector.
func (inj *ContextInjector) Register(fetcher DataFetcher) {
	inj.fetchers = append(inj.fetchers, fetcher)
}

// Inject runs all registered fetchers and concatenates their output into a
// single context string for prompt injection.
func (inj *ContextInjector) Inject(ctx context.Context, db *gorm.DB, shopID string) string {
	return inj.InjectWithQuery(ctx, db, shopID, "")
}

// InjectWithQuery runs all fetchers, setting a user query on any fetcher
// that implements QuerySetter (e.g. RAGFetcher for semantic search).
func (inj *ContextInjector) InjectWithQuery(ctx context.Context, db *gorm.DB, shopID, query string) string {
	if shopID == "" {
		return ""
	}
	// Set query on RAG-aware fetchers
	if query != "" {
		for _, f := range inj.fetchers {
			if qs, ok := f.(QuerySetter); ok {
				qs.SetQuery(query)
			}
		}
	}
	var b strings.Builder
	for _, f := range inj.fetchers {
		s := f.Fetch(ctx, db, shopID)
		if s != "" {
			b.WriteString(s)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// GetFetcher returns a registered DataFetcher by name, or nil if not found.
// Useful for callers that need to set fetcher-specific options (e.g. RAGFetcher.SetSources()).
func (inj *ContextInjector) GetFetcher(name string) DataFetcher {
	for _, f := range inj.fetchers {
		if f.Name() == name {
			return f
		}
	}
	return nil
}

// InjectOne runs a single named fetcher (if registered) and returns its output.
func (inj *ContextInjector) InjectOne(ctx context.Context, db *gorm.DB, shopID string, name string) string {
	if shopID == "" {
		return ""
	}
	for _, f := range inj.fetchers {
		if f.Name() == name {
			return f.Fetch(ctx, db, shopID)
		}
	}
	return ""
}

// --- Fetchers ---

// ReturnFetcher returns both size-related and reason-based return analysis.
// Queries BOTH the legacy returns+return_items table AND the synced_returns from Shopify webhooks.
type ReturnFetcher struct{}

func (f *ReturnFetcher) Name() string { return "return_insight" }

func (f *ReturnFetcher) Fetch(ctx context.Context, db *gorm.DB, shopID string) string {
	if db == nil {
		return ""
	}

	since := time.Now().UTC().AddDate(0, 0, -30)

	// Aggregate: reason → count (merged from both sources)
	reasonAgg := make(map[string]int)

	// Source 1: Legacy returns+return_items (manual entries)
	type row struct {
		Size   string
		Reason string
		Cnt    int
	}
	var legacyRows []row
	_ = db.Raw(`
		SELECT ri.size, ri.reason, COUNT(*) AS cnt
		FROM return_items ri
		JOIN returns r ON r.id = ri.return_id
		WHERE r.shop_id = ? AND r.created_at > ?
		GROUP BY ri.size, ri.reason
		ORDER BY cnt DESC LIMIT 20
	`, shopID, since).Scan(&legacyRows)

	for _, r := range legacyRows {
		if r.Reason != "" {
			reasonAgg[r.Reason] += r.Cnt
		}
	}

	// Source 2: synced_returns (Shopify webhook data)
	var syncedRows []model.SyncedReturn
	_ = db.Where("shop_id = ? AND synced_at > ? AND line_items IS NOT NULL", shopID, since).
		Order("synced_at DESC").Limit(100).Find(&syncedRows)

	for _, sr := range syncedRows {
		var items []model.ReturnLineItemPayload
		json.Unmarshal(sr.LineItems, &items)
		for _, li := range items {
			if li.ReturnReason != "" {
				reasonAgg[li.ReturnReason] += li.Quantity
			}
		}
	}

	if len(reasonAgg) == 0 {
		return ""
	}

	// Sort by count desc
	type reasonPair struct {
		Reason string
		Cnt    int
	}
	sorted := make([]reasonPair, 0, len(reasonAgg))
	for reason, cnt := range reasonAgg {
		sorted = append(sorted, reasonPair{reason, cnt})
	}
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Cnt > sorted[j-1].Cnt; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}

	var reasonParts []string
	for i, r := range sorted {
		if i >= 5 {break}
		reasonParts = append(reasonParts, fmt.Sprintf("%s:%d次", r.Reason, r.Cnt))
	}

	// Include size+reason detail from legacy returns (if any)
	var sizeParts []string
	for i, r := range legacyRows {
		if i >= 5 {break}
		if r.Size != "" {
			sizeParts = append(sizeParts, fmt.Sprintf("%s (%s): %d次", r.Size, r.Reason, r.Cnt))
		}
	}

	var b strings.Builder
	if len(sizeParts) > 0 {
		b.WriteString(fmt.Sprintf("[尺码退货 近30天] %s", strings.Join(sizeParts, "; ")))
	}
	if len(reasonParts) > 0 {
		if b.Len() > 0 { b.WriteString("\n") }
		b.WriteString(fmt.Sprintf("[退货原因 近30天] %s", strings.Join(reasonParts, " ")))
	}
	b.WriteString(fmt.Sprintf("\n[数据说明] 来源:人工录入%d条 + Shopify自动同步%d条", len(legacyRows), len(syncedRows)))
	return b.String()
}

// OrderStatusFetcher returns the 5 most recent orders for customer-facing AI chat.
type OrderStatusFetcher struct{}

func (f *OrderStatusFetcher) Name() string { return "order_status" }

func (f *OrderStatusFetcher) Fetch(ctx context.Context, db *gorm.DB, shopID string) string {
	if db == nil {
		return ""
	}

	var orders []model.SyncedOrder
	if err := db.Where("shop_id = ?", shopID).
		Order("created_at DESC").
		Limit(5).
		Find(&orders).Error; err != nil || len(orders) == 0 {
		return ""
	}

	var parts []string
	for _, o := range orders {
		parts = append(parts, fmt.Sprintf("#%d ($%.2f 财务:%s 物流:%s)",
			o.OrderNumber, o.TotalPrice, o.FinancialStatus, o.FulfillmentStatus))
	}
	return fmt.Sprintf("[最近订单] %s", strings.Join(parts, " | "))
}

// ProductDataFetcher returns the 10 most recent products for context.
type ProductDataFetcher struct{}

func (f *ProductDataFetcher) Name() string { return "product_data" }

func (f *ProductDataFetcher) Fetch(ctx context.Context, db *gorm.DB, shopID string) string {
	if db == nil {
		return ""
	}

	var prods []model.SyncedProduct
	if err := db.Where("shop_id = ?", shopID).
		Order("updated_at DESC").
		Limit(10).
		Find(&prods).Error; err != nil || len(prods) == 0 {
		return ""
	}

	var parts []string
	for _, p := range prods {
		parts = append(parts, fmt.Sprintf("%s (type:%s tags:%s)", p.Title, p.ProductType, p.Tags))
	}
	return fmt.Sprintf("[产品列表] %s", strings.Join(parts, " | "))
}

// TrendDataFetcher returns Google Trends data for the merchant's top product type.
type TrendDataFetcher struct {
	TrendsClient *externaldata.GoogleTrendsClient
}

func (f *TrendDataFetcher) Name() string { return "trend_data" }

func (f *TrendDataFetcher) Fetch(ctx context.Context, db *gorm.DB, shopID string) string {
	if db == nil || f.TrendsClient == nil {
		return ""
	}

	var p model.SyncedProduct
	if err := db.Where("shop_id = ?", shopID).
		Order("updated_at DESC").
		First(&p).Error; err != nil {
		return ""
	}

	td, err := f.TrendsClient.GetInterestOverTime(ctx, []string{p.ProductType}, "US")
	if err != nil || td == nil {
		return ""
	}

	score := 0
	if len(td.Timeline) > 0 {
		score = td.Timeline[len(td.Timeline)-1].Value
	}
	return fmt.Sprintf("[趋势数据] 品类:%s 趋势:%s 最新评分:%d", p.ProductType, td.Trend, score)
}

// InventoryVelocityFetcher returns aggregated sales velocity from line_items.
// Uses Go-level JSON parsing instead of PostgreSQL-specific jsonb functions
// so it works with both PostgreSQL (production) and SQLite (test).
type InventoryVelocityFetcher struct{}

func (f *InventoryVelocityFetcher) Name() string { return "inventory_velocity" }

func (f *InventoryVelocityFetcher) Fetch(ctx context.Context, db *gorm.DB, shopID string) string {
	if db == nil {
		return ""
	}

	since := time.Now().UTC().AddDate(0, 0, -30)

	// Fetch orders with line_items in the last 30 days
	var orders []model.SyncedOrder
	if err := db.Where("shop_id = ? AND created_at > ?", shopID, since).
		Order("created_at DESC").Limit(200).
		Find(&orders).Error; err != nil || len(orders) == 0 {
		return ""
	}

	// Aggregate quantities by product title in Go (portable across DBs)
	type li struct {
		Title    string `json:"title"`
		Quantity int    `json:"quantity"`
	}
	agg := make(map[string]int)
	for _, o := range orders {
		var items []li
		if err := json.Unmarshal(o.LineItems, &items); err != nil {
			continue
		}
		for _, it := range items {
			agg[it.Title] += it.Quantity
		}
	}
	if len(agg) == 0 {
		return ""
	}

	// Sort by quantity descending, take top 5
	type pair struct {
		Title    string
		Quantity int
	}
	sorted := make([]pair, 0, len(agg))
	for title, qty := range agg {
		sorted = append(sorted, pair{title, qty})
	}
	// Simple insertion sort for top 5 (small n)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Quantity > sorted[j-1].Quantity; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	if len(sorted) > 5 {
		sorted = sorted[:5]
	}

	var parts []string
	for _, p := range sorted {
		parts = append(parts, fmt.Sprintf("%s ×%d", p.Title, p.Quantity))
	}
	return fmt.Sprintf("[销量 近30天] %s", strings.Join(parts, " | "))
}

// TryOnHistoryFetcher returns the 5 most recent try-on records for context.
type TryOnHistoryFetcher struct{}

func (f *TryOnHistoryFetcher) Name() string { return "try_on_history" }

func (f *TryOnHistoryFetcher) Fetch(ctx context.Context, db *gorm.DB, shopID string) string {
	if db == nil {
		return ""
	}

	var records []model.TryOnRecord
	if err := db.Where("shop_id = ?", shopID).
		Order("created_at DESC").
		Limit(5).
		Find(&records).Error; err != nil || len(records) == 0 {
		return ""
	}

	var parts []string
	for _, r := range records {
		parts = append(parts, fmt.Sprintf("product:%s status:%s", r.ProductID, r.Status))
	}
	return fmt.Sprintf("[试穿记录] %s", strings.Join(parts, " | "))
}

// SnapshopFetcher provides a comprehensive shop overview from the Redis snapshot cache.
// This replaces 6 individual PG queries with a single Redis read.
type SnapshopFetcher struct {
	Store *SnapshotStore
}

func (f *SnapshopFetcher) Name() string { return "shop_snapshot" }

func (f *SnapshopFetcher) Fetch(ctx context.Context, db *gorm.DB, shopID string) string {
	if f.Store == nil {
		return ""
	}
	snap := f.Store.Get(ctx, shopID)
	if snap == nil {
		// Cache miss — skip. Refresh is handled by cron (/api/cron/refresh-snapshots)
		// to avoid blocking the AI inference path with sync DB queries.
		return ""
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("退货率(30天):%.1f%% 订单数:%d 退货数:%d",
		snap.ReturnRate, snap.OrderCount, snap.ReturnCount))
	if len(snap.TopReturnReasons) > 0 {
		parts = append(parts, fmt.Sprintf("Top退货原因:%s", strings.Join(snap.TopReturnReasons, ",")))
	}
	parts = append(parts, fmt.Sprintf("产品数:%d 客户数:%d 试穿数(30天):%d",
		snap.TotalProducts, snap.TotalCustomers, snap.TotalTryOns))
	if len(snap.TopCategories) > 0 {
		parts = append(parts, fmt.Sprintf("Top品类:%s", strings.Join(snap.TopCategories, ",")))
	}

	return "[店铺快照] 近30天: " + strings.Join(parts, "; ")
}

