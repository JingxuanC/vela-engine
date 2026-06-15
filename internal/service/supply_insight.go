// Package service provides business logic for the Vela AI API.
// supply_insight.go implements supply chain analytics: stock prediction,
// return anomaly detection, and trend-product matching.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/externaldata"
)

// ──────────────────────────────────────────────────────────────────────────────
// Stock Prediction
// ──────────────────────────────────────────────────────────────────────────────

// StockPrediction holds a single product's inventory forecast.
type StockPrediction struct {
	ProductID       string  `json:"product_id"`
	ProductTitle    string  `json:"product_title"`
	ProductType     string  `json:"product_type,omitempty"`
	CurrentStock    int     `json:"current_stock"`
	DailySales      float64 `json:"daily_sales"`
	DaysUntilStockout float64 `json:"days_until_stockout"`
	Status          string  `json:"status"` // healthy / warning / critical / out_of_stock
	RecommendedRestock int  `json:"recommended_restock"`
}

// shopifyVariant mirrors the Shopify REST API variant shape stored in synced_products.variants JSON.
type shopifyVariant struct {
	ID               int64   `json:"id"`
	Title            string  `json:"title"`
	Price            string  `json:"price"`
	SKU              string  `json:"sku"`
	InventoryQuantity int    `json:"inventory_quantity"`
}

// shopifyLineItem mirrors the order line item shape stored in synced_orders.line_items JSON.
type shopifyLineItem struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Quantity  int    `json:"quantity"`
	Price     string `json:"price"`
	SKU       string `json:"sku"`
	ProductID int64  `json:"product_id"`
	VariantID int64  `json:"variant_id"`
}

// ComputeStockPredictions analyses synced_products inventory and synced_orders
// sales velocity (past 30 days) to produce per-product stock forecasts.
func ComputeStockPredictions(ctx context.Context, db *gorm.DB, shopID uuid.UUID) ([]StockPrediction, error) {
	// 1. Fetch all active products
	var products []model.SyncedProduct
	if err := db.WithContext(ctx).
		Where("shop_id = ? AND status = ?", shopID, "active").
		Find(&products).Error; err != nil {
		return nil, fmt.Errorf("supply_insight: query products: %w", err)
	}

	// 2. Fetch orders from past 30 days
	since := time.Now().AddDate(0, 0, -30)
	var orders []model.SyncedOrder
	if err := db.WithContext(ctx).
		Where("shop_id = ? AND created_at >= ?", shopID, since).
		Find(&orders).Error; err != nil {
		return nil, fmt.Errorf("supply_insight: query orders: %w", err)
	}

	// 3. Build product_id → quantities-sold map from order line items
	//    We normalise by title (fallback) when product_id is missing (legacy data).
	type soldKey struct {
		productID string
		title     string
	}
	soldQty := make(map[soldKey]int)
	for _, o := range orders {
		var items []shopifyLineItem
		if err := json.Unmarshal(o.LineItems, &items); err != nil {
			continue
		}
		for _, li := range items {
			pid := fmt.Sprintf("%d", li.ProductID)
			// Fallback for legacy data without product_id: use title
			if pid == "0" {
				pid = ""
			}
			key := soldKey{productID: pid, title: strings.ToLower(strings.TrimSpace(li.Title))}
			soldQty[key] += li.Quantity
		}
	}

	// 4. Compute per-product predictions
	results := make([]StockPrediction, 0, len(products))
	soldByProductID := make(map[string]int)
	soldByTitle := make(map[string]int)
	for k, v := range soldQty {
		if k.productID != "" {
			soldByProductID[k.productID] += v
		}
		if k.title != "" {
			soldByTitle[k.title] += v
		}
	}

	for _, p := range products {
		// Sum variant inventory
		inventory := 0
		var variants []shopifyVariant
		if err := json.Unmarshal(p.Variants, &variants); err == nil {
			for _, v := range variants {
				inventory += v.InventoryQuantity
			}
		}

		// Sales in last 30 days — match by platform_id first, then by title
		totalSold := soldByProductID[p.PlatformID]
		if totalSold == 0 {
			totalSold = soldByTitle[strings.ToLower(strings.TrimSpace(p.Title))]
		}

		dailySales := float64(totalSold) / 30.0

		var daysUntil float64
		var status string
		var recommendedRestock int

		switch {
		case inventory == 0:
			daysUntil = 0
			status = "out_of_stock"
			if dailySales > 0 {
				recommendedRestock = int(math.Ceil(dailySales * 30))
			}
		case dailySales == 0:
			daysUntil = -1 // unlimited (no sales data)
			status = "healthy"
			recommendedRestock = 0
		default:
			daysUntil = float64(inventory) / dailySales
			switch {
			case daysUntil > 30:
				status = "healthy"
			case daysUntil >= 7:
				status = "warning"
			default:
				status = "critical"
			}
			predictedDemand30 := dailySales * 30
			if predictedDemand30 > float64(inventory) {
				recommendedRestock = int(math.Ceil(predictedDemand30 - float64(inventory)))
			}
		}

		results = append(results, StockPrediction{
			ProductID:          p.PlatformID,
			ProductTitle:       p.Title,
			ProductType:        p.ProductType,
			CurrentStock:       inventory,
			DailySales:         math.Round(dailySales*100) / 100,
			DaysUntilStockout:  math.Round(daysUntil*100) / 100,
			Status:             status,
			RecommendedRestock: recommendedRestock,
		})
	}

	// Sort by urgency: out_of_stock → critical → warning → healthy
	sort.Slice(results, func(i, j int) bool {
		order := map[string]int{"out_of_stock": 0, "critical": 1, "warning": 2, "healthy": 3}
		return order[results[i].Status] < order[results[j].Status]
	})

	return results, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Return Anomaly Detection
// ──────────────────────────────────────────────────────────────────────────────

// ReturnAnomaly holds a single product return anomaly detection result.
type ReturnAnomaly struct {
	ProductID    string  `json:"product_id"`
	ProductTitle string  `json:"product_title"`
	ReturnRate   float64 `json:"return_rate"`   // percentage
	StoreAvg     float64 `json:"store_avg"`     // store-wide average return rate
	Deviation    float64 `json:"deviation"`     // return_rate / store_avg ratio
	ReturnCount  int     `json:"return_count"`
	OrderCount   int     `json:"order_count"`
	IsAnomaly    bool    `json:"is_anomaly"`
}

// ComputeReturnAnomalies calculates product-level return rates and flags
// anomalies where a product's return rate exceeds the store average × 2.
func ComputeReturnAnomalies(ctx context.Context, db *gorm.DB, shopID uuid.UUID) ([]ReturnAnomaly, error) {
	// 1. Count total orders (denominator for store-level return rate)
	var totalOrders int64
	if err := db.WithContext(ctx).Model(&model.SyncedOrder{}).
		Where("shop_id = ?", shopID).
		Count(&totalOrders).Error; err != nil {
		return nil, fmt.Errorf("supply_insight: count orders: %w", err)
	}
	if totalOrders == 0 {
		return []ReturnAnomaly{}, nil
	}

	// 2. Count total returns (use both returns + synced_returns)
	var totalReturns int64
	if err := db.WithContext(ctx).Model(&model.Return{}).
		Where("shop_id = ?", shopID).
		Count(&totalReturns).Error; err != nil {
		return nil, fmt.Errorf("supply_insight: count returns: %w", err)
	}
	var totalSyncedReturns int64
	db.WithContext(ctx).Model(&model.SyncedReturn{}).
		Where("shop_id = ?", shopID).
		Count(&totalSyncedReturns)
	totalReturns += totalSyncedReturns

	storeAvgReturnRate := float64(totalReturns) / float64(totalOrders) * 100

	// 3. Get per-product return counts from return_items
	type productReturnCount struct {
		ProductID    string
		ProductTitle string
		Count        int
	}
	var returnCounts []productReturnCount
	db.WithContext(ctx).Model(&model.ReturnItem{}).
		Select("product_id, product_title, SUM(quantity) as count").
		Joins("JOIN returns ON returns.id = return_items.return_id").
		Where("returns.shop_id = ?", shopID).
		Group("product_id, product_title").
		Scan(&returnCounts)

	// Also count synced_returns per product from line_items
	var syncedReturns []model.SyncedReturn
	db.WithContext(ctx).Where("shop_id = ?", shopID).Find(&syncedReturns)
	srMap := make(map[string]int)
	for _, sr := range syncedReturns {
		var items []model.ReturnLineItemPayload
		if err := json.Unmarshal(sr.LineItems, &items); err != nil {
			continue
		}
		for _, li := range items {
			pid := fmt.Sprintf("%d", li.LineItemID)
			if pid == "0" {
				continue
			}
			srMap[pid] += li.Quantity
		}
	}

	// 4. Get per-product order counts from synced_orders line items
	//    Build product_id → order count map
	productOrders := make(map[string]int)
	var allOrders []model.SyncedOrder
	db.WithContext(ctx).Where("shop_id = ?", shopID).Find(&allOrders)
	for _, o := range allOrders {
		var items []shopifyLineItem
		if err := json.Unmarshal(o.LineItems, &items); err != nil {
			continue
		}
		for _, li := range items {
			pid := fmt.Sprintf("%d", li.ProductID)
			if pid == "0" {
				// fallback: match by title → look up product
				pid = resolveProductIDByTitle(ctx, db, shopID, li.Title)
			}
			if pid != "" && pid != "0" {
				productOrders[pid] += li.Quantity
			}
		}
	}

	// 5. Build result set: merge return counts from both sources
	productReturnMap := make(map[string]*ReturnAnomaly)
	for _, rc := range returnCounts {
		productReturnMap[rc.ProductID] = &ReturnAnomaly{
			ProductID:    rc.ProductID,
			ProductTitle: rc.ProductTitle,
			ReturnCount:  rc.Count,
		}
	}
	for pid, count := range srMap {
		if existing, ok := productReturnMap[pid]; ok {
			existing.ReturnCount += count
		} else {
			// Look up product title
			title := resolveProductTitle(ctx, db, shopID, pid)
			productReturnMap[pid] = &ReturnAnomaly{
				ProductID:    pid,
				ProductTitle: title,
				ReturnCount:  count,
			}
		}
	}

	// 6. Compute per-product return rate
	results := make([]ReturnAnomaly, 0, len(productReturnMap))
	for _, anom := range productReturnMap {
		anom.OrderCount = productOrders[anom.ProductID]
		if anom.OrderCount > 0 {
			anom.ReturnRate = math.Round(float64(anom.ReturnCount)/float64(anom.OrderCount)*10000) / 100
		}
		anom.StoreAvg = math.Round(storeAvgReturnRate*100) / 100
		if anom.StoreAvg > 0 {
			anom.Deviation = math.Round(anom.ReturnRate/anom.StoreAvg*100) / 100
		}
		anom.IsAnomaly = anom.ReturnRate > anom.StoreAvg*2 && anom.StoreAvg > 0 && anom.OrderCount >= 5

		results = append(results, *anom)
	}

	// Sort by deviation descending (most anomalous first)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Deviation > results[j].Deviation
	})

	return results, nil
}

// resolveProductIDByTitle looks up a product's platform_id by title match.
func resolveProductIDByTitle(ctx context.Context, db *gorm.DB, shopID uuid.UUID, title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	var p model.SyncedProduct
	err := db.WithContext(ctx).
		Where("shop_id = ? AND LOWER(title) = ?", shopID, strings.ToLower(title)).
		First(&p).Error
	if err != nil {
		return ""
	}
	return p.PlatformID
}

// resolveProductTitle looks up a product's title by platform_id.
func resolveProductTitle(ctx context.Context, db *gorm.DB, shopID uuid.UUID, platformID string) string {
	var p model.SyncedProduct
	err := db.WithContext(ctx).
		Where("shop_id = ? AND platform_id = ?", shopID, platformID).
		First(&p).Error
	if err != nil {
		return "Unknown Product"
	}
	return p.Title
}

// ──────────────────────────────────────────────────────────────────────────────
// Trend Match
// ──────────────────────────────────────────────────────────────────────────────

// TrendMatchResult holds trend-vs-inventory matching data.
type TrendMatchResult struct {
	TrendingKeywords []TrendingKeyword `json:"trending_keywords"`
	MissingProducts  []MissingProduct  `json:"missing_products"`
}

// TrendingKeyword is a single keyword with search volume change data.
type TrendingKeyword struct {
	Keyword       string  `json:"keyword"`
	SearchChange  float64 `json:"search_change_pct"` // percentage change
	TrendStatus   string  `json:"trend_status"`      // rising, breakout, top, stable
	RelevanceScore int    `json:"relevance_score"`
}

// MissingProduct describes a trending category/product the merchant doesn't carry.
type MissingProduct struct {
	Keyword     string `json:"keyword"`
	TrendStatus string `json:"trend_status"`
	Suggestion  string `json:"suggestion"`
}

// ComputeTrendMatch uses Google Trends to find trending keywords related to the
// merchant's categories, then identifies missing product opportunities.
func ComputeTrendMatch(ctx context.Context, db *gorm.DB, trendsClient *externaldata.GoogleTrendsClient, shopID uuid.UUID) (*TrendMatchResult, error) {
	result := &TrendMatchResult{
		TrendingKeywords: []TrendingKeyword{},
		MissingProducts:  []MissingProduct{},
	}

	if trendsClient == nil || db == nil {
		return result, nil
	}

	// 1. Extract merchant's product categories/types
	var categories []string
	if err := db.WithContext(ctx).Model(&model.SyncedProduct{}).
		Select("DISTINCT product_type").
		Where("shop_id = ? AND product_type != ''", shopID).
		Pluck("product_type", &categories).Error; err != nil {
		return nil, fmt.Errorf("supply_insight: query categories: %w", err)
	}

	if len(categories) == 0 {
		categories = append(categories, "fashion")
	}

	// Collect all merchant product titles for matching
	var productTitles []string
	db.WithContext(ctx).Model(&model.SyncedProduct{}).
		Where("shop_id = ? AND status = ?", shopID, "active").
		Pluck("LOWER(title)", &productTitles)

	titleSet := make(map[string]bool, len(productTitles))
	for _, t := range productTitles {
		titleSet[strings.TrimSpace(t)] = true
	}

	// 2. For each top category, query Google Trends for related queries
	seenKeywords := make(map[string]bool)
	trendingKeywords := make([]TrendingKeyword, 0)

	// Limit to top 3 categories to avoid excessive API calls
	maxCats := 3
	if len(categories) > maxCats {
		// Sort by frequency to pick top categories
		type catFreq struct {
			cat  string
			freq int
		}
		var freqs []catFreq
		for _, cat := range categories {
			var count int64
			db.WithContext(ctx).Model(&model.SyncedProduct{}).
				Where("shop_id = ? AND product_type = ?", shopID, cat).
				Count(&count)
			freqs = append(freqs, catFreq{cat, int(count)})
		}
		sort.Slice(freqs, func(i, j int) bool { return freqs[i].freq > freqs[j].freq })
		categories = make([]string, 0, maxCats)
		for i := 0; i < maxCats && i < len(freqs); i++ {
			categories = append(categories, freqs[i].cat)
		}
	}

	for _, cat := range categories {
		related, err := trendsClient.GetRelatedQueries(ctx, cat)
		if err != nil {
			continue
		}
		for _, q := range related {
			if seenKeywords[q.Query] {
				continue
			}
			seenKeywords[q.Query] = true

			searchChange := 0.0
			switch q.Status {
			case "breakout":
				searchChange = 500.0
			case "rising":
				searchChange = float64(q.Score)
			case "top":
				searchChange = 0.0
			default:
				searchChange = float64(q.Score)
			}

			tk := TrendingKeyword{
				Keyword:        q.Query,
				SearchChange:   searchChange,
				TrendStatus:    q.Status,
				RelevanceScore: q.Score,
			}
			trendingKeywords = append(trendingKeywords, tk)
		}
	}

	result.TrendingKeywords = trendingKeywords

	// 3. Match trending keywords against existing products
	//    A keyword is "missing" if the merchant has no product whose title contains it
	missingProducts := make([]MissingProduct, 0)
	for _, tk := range trendingKeywords {
		if tk.TrendStatus == "stable" || tk.TrendStatus == "top" {
			continue // only flag rising/breakout as opportunities
		}

		keywordLower := strings.ToLower(tk.Keyword)
		found := false
		for title := range titleSet {
			if strings.Contains(title, keywordLower) || strings.Contains(keywordLower, title) {
				found = true
				break
			}
		}
		if !found {
			suggestion := fmt.Sprintf("考虑添加与「%s」相关的产品以抓住上升趋势", tk.Keyword)
			missingProducts = append(missingProducts, MissingProduct{
				Keyword:     tk.Keyword,
				TrendStatus: tk.TrendStatus,
				Suggestion:  suggestion,
			})
		}
	}
	result.MissingProducts = missingProducts

	return result, nil
}
