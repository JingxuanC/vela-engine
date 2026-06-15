package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/JingxuanC/vela-engine/internal/model"
)

// RecommendationEngine computes FBT and trending product recommendations.
type RecommendationEngine struct {
	db *gorm.DB
}

// NewRecommendationEngine creates a new RecommendationEngine.
func NewRecommendationEngine(db *gorm.DB) *RecommendationEngine {
	return &RecommendationEngine{db: db}
}

// lineItem mirrors shopifyLineItem for parsing SyncedOrder.LineItems JSONB.
type lineItem struct {
	ID        int64  `json:"id"`
	ProductID int64  `json:"product_id"`
	VariantID int64  `json:"variant_id"`
	Title     string `json:"title"`
	Quantity  int    `json:"quantity"`
	Price     string `json:"price"`
	Sku       string `json:"sku"`
}

// productPair represents a pair of products (a < b always).
type productPair struct {
	A int64
	B int64
}

// RecommendationResult is a single recommendation.
type RecommendationResult struct {
	ProductID int64   `json:"product_id"`
	Title     string  `json:"title"`
	ImageURL  string  `json:"image_url"`
	Price     string  `json:"price"`
	Score     float64 `json:"score"`
	Reason    string  `json:"reason"`
}

// BuildFBT computes Frequently Bought Together pairs for a shop.
// Loads orders from the past 90 days, parses JSONB line_items,
// builds product pair co-occurrence counts, and upserts to product_affinities.
func (e *RecommendationEngine) BuildFBT(ctx context.Context, shopID uuid.UUID) error {
	cutoff := time.Now().Add(-90 * 24 * time.Hour)

	var orders []model.SyncedOrder
	if err := e.db.WithContext(ctx).
		Where("shop_id = ? AND created_at > ?", shopID, cutoff).
		Find(&orders).Error; err != nil {
		return fmt.Errorf("BuildFBT: load orders: %w", err)
	}

	if len(orders) == 0 {
		slog.Info("recommendation_engine: BuildFBT — no recent orders", "shop_id", shopID)
		return nil
	}

	// Build pair co-occurrence map
	pairCount := make(map[productPair]int)
	for _, order := range orders {
		var items []lineItem
		if err := json.Unmarshal(order.LineItems, &items); err != nil {
			continue
		}
		// All products in the same order are paired
		for i := 0; i < len(items); i++ {
			if items[i].ProductID == 0 {
				continue
			}
			for j := i + 1; j < len(items); j++ {
				if items[j].ProductID == 0 {
					continue
				}
				a := items[i].ProductID
				b := items[j].ProductID
				if a > b {
					a, b = b, a
				}
				pairCount[productPair{A: a, B: b}]++
			}
		}
	}

	if len(pairCount) == 0 {
		slog.Info("recommendation_engine: BuildFBT — no pairs found", "shop_id", shopID)
		return nil
	}

	// Filter: co_count >= 3
	// Collect all unique product IDs
	productIDs := make(map[int64]bool)
	for pair, count := range pairCount {
		if count < 3 {
			delete(pairCount, pair)
			continue
		}
		productIDs[pair.A] = true
		productIDs[pair.B] = true
	}

	if len(pairCount) == 0 {
		slog.Info("recommendation_engine: BuildFBT — no pairs with co_count >= 3", "shop_id", shopID)
		return nil
	}

	// Batch fetch product info
	productMap, err := e.batchGetProducts(ctx, shopID, productIDs)
	if err != nil {
		slog.Warn("recommendation_engine: BuildFBT — failed to fetch product info", "error", err)
	}

	// Compute max co_count for normalization
	var maxCoCount int
	for _, count := range pairCount {
		if count > maxCoCount {
			maxCoCount = count
		}
	}

	// Normalize and upsert
	now := time.Now().UTC()
	for pair, count := range pairCount {
		score := float64(count) / float64(maxCoCount)
		score = math.Round(score*100) / 100

		affinity := model.ProductAffinity{
			ProductID_A: pair.A,
			ProductID_B: pair.B,
			ShopID:      shopID,
			Score:       score,
			CoCount:     count,
			Strategy:    "fbt",
			ComputedAt:  now,
		}

		// Fill product info for product_id_b (the recommended product)
		if info, ok := productMap[pair.B]; ok {
			affinity.ProductTitle = info.Title
			affinity.ProductImage = info.Image
			affinity.ProductPrice = info.Price
		}

		if err := e.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "shop_id"},
				{Name: "product_id_a"},
				{Name: "product_id_b"},
				{Name: "strategy"},
			},
			DoUpdates: clause.AssignmentColumns([]string{
				"score", "co_count", "product_title", "product_image",
				"product_price", "computed_at",
			}),
		}).Create(&affinity).Error; err != nil {
			slog.Error("recommendation_engine: BuildFBT — upsert failed",
				"product_id_a", pair.A, "product_id_b", pair.B, "error", err)
		}
	}

	slog.Info("recommendation_engine: BuildFBT complete",
		"shop_id", shopID, "pairs", len(pairCount), "max_co_count", maxCoCount)
	return nil
}

// BuildTrending computes trending products (top sellers by recent order count).
// Uses PostgreSQL jsonb_array_elements for efficient in-DB computation.
func (e *RecommendationEngine) BuildTrending(ctx context.Context, shopID uuid.UUID) error {
	cutoff := time.Now().Add(-7 * 24 * time.Hour)

	type productCount struct {
		ProductID int64 `gorm:"column:product_id"`
		Cnt       int   `gorm:"column:cnt"`
	}

	var rows []productCount
	err := e.db.WithContext(ctx).Raw(`
		SELECT (elem->>'product_id')::bigint AS product_id, COUNT(*) AS cnt
		FROM synced_orders,
			jsonb_array_elements(line_items) AS elem
		WHERE shop_id = ? AND created_at > ?
		GROUP BY product_id
		ORDER BY cnt DESC
		LIMIT 20
	`, shopID, cutoff).Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("BuildTrending: query: %w", err)
	}

	if len(rows) == 0 {
		slog.Info("recommendation_engine: BuildTrending — no recent orders", "shop_id", shopID)
		return nil
	}

	// Collect product IDs and compute total for normalization
	productIDs := make(map[int64]bool)
	var totalOrders int
	for _, r := range rows {
		productIDs[r.ProductID] = true
		totalOrders += r.Cnt
	}

	productMap, _ := e.batchGetProducts(ctx, shopID, productIDs)

	now := time.Now().UTC()
	for _, r := range rows {
		score := float64(r.Cnt) / float64(totalOrders)
		score = math.Round(score*10000) / 10000

		affinity := model.ProductAffinity{
			ProductID_A: r.ProductID,
			ProductID_B: 0, // trending has no pair
			ShopID:      shopID,
			Score:       score,
			CoCount:     r.Cnt,
			Strategy:    "trending",
			ComputedAt:  now,
		}

		if info, ok := productMap[r.ProductID]; ok {
			affinity.ProductTitle = info.Title
			affinity.ProductImage = info.Image
			affinity.ProductPrice = info.Price
		}

		if err := e.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "shop_id"},
				{Name: "product_id_a"},
				{Name: "product_id_b"},
				{Name: "strategy"},
			},
			DoUpdates: clause.AssignmentColumns([]string{
				"score", "co_count", "product_title", "product_image",
				"product_price", "computed_at",
			}),
		}).Create(&affinity).Error; err != nil {
			slog.Error("recommendation_engine: BuildTrending — upsert failed",
				"product_id", r.ProductID, "error", err)
		}
	}

	slog.Info("recommendation_engine: BuildTrending complete",
		"shop_id", shopID, "products", len(rows))
	return nil
}

// GetRecommendations returns recommendations for a given product and strategy.
// For FBT, queries both product_id_a and product_id_b since pairs are stored
// with product_id_a < product_id_b, and either side can be the target product.
func (e *RecommendationEngine) GetRecommendations(
	ctx context.Context, shopID uuid.UUID, productID int64,
	strategy string, limit int,
) ([]RecommendationResult, error) {
	var affinities []model.ProductAffinity
	err := e.db.WithContext(ctx).
		Where("shop_id = ? AND strategy = ? AND (product_id_a = ? OR product_id_b = ?)",
			shopID, strategy, productID, productID).
		Order("score DESC").
		Limit(limit).
		Find(&affinities).Error
	if err != nil {
		return nil, fmt.Errorf("GetRecommendations: query: %w", err)
	}

	reason := "Frequently bought together"
	if strategy == "trending" {
		reason = "Trending now"
	}

	var results []RecommendationResult
	for _, a := range affinities {
		// Determine which product ID is the recommendation target
		recProductID := a.ProductID_B
		if a.ProductID_A != productID {
			recProductID = a.ProductID_A
		}
		results = append(results, RecommendationResult{
			ProductID: recProductID,
			Title:     a.ProductTitle,
			ImageURL:  a.ProductImage,
			Price:     a.ProductPrice,
			Score:     a.Score,
			Reason:    reason,
		})
	}
	if len(results) > 0 {
		return results, nil
	}

	// Fallback 1: same product_type
	results = e.querySameCategory(ctx, shopID, productID, limit)
	if len(results) > 0 {
		return results, nil
	}

	// Fallback 2: trending
	return e.queryTrendingFallback(ctx, shopID, limit)
}

// GetTrending returns the top trending products for a shop.
func (e *RecommendationEngine) GetTrending(
	ctx context.Context, shopID uuid.UUID, limit int,
) ([]RecommendationResult, error) {
	return e.queryTrendingFallback(ctx, shopID, limit)
}

// productInfo holds basic product info for display.
type productInfo struct {
	Title string
	Image string
	Price string
}

// batchGetProducts fetches product info for a set of product IDs.
func (e *RecommendationEngine) batchGetProducts(
	ctx context.Context, shopID uuid.UUID, productIDs map[int64]bool,
) (map[int64]productInfo, error) {
	if len(productIDs) == 0 {
		return nil, nil
	}

	idList := make([]int64, 0, len(productIDs))
	for id := range productIDs {
		idList = append(idList, id)
	}

	// Convert to string for platform_id comparison
	idStrs := make([]string, len(idList))
	for i, id := range idList {
		idStrs[i] = fmt.Sprintf("%d", id)
	}

	var products []model.SyncedProduct
	if err := e.db.WithContext(ctx).
		Where("shop_id = ? AND platform_id IN ?", shopID, idStrs).
		Find(&products).Error; err != nil {
		return nil, err
	}

	result := make(map[int64]productInfo, len(products))
	for _, p := range products {
		var pid int64
		fmt.Sscanf(p.PlatformID, "%d", &pid)
		if pid == 0 {
			continue
		}
		imgURL := extractProductImage(p.Images)
		price := extractProductPrice(p.Variants)
		result[pid] = productInfo{
			Title: p.Title,
			Image: imgURL,
			Price: price,
		}
	}
	return result, nil
}

// querySameCategory finds products with the same product_type.
func (e *RecommendationEngine) querySameCategory(
	ctx context.Context, shopID uuid.UUID, productID int64, limit int,
) []RecommendationResult {
	// Find the reference product's type
	var ref model.SyncedProduct
	if err := e.db.WithContext(ctx).
		Where("shop_id = ? AND platform_id = ?", shopID, fmt.Sprintf("%d", productID)).
		First(&ref).Error; err != nil {
		return nil
	}

	if ref.ProductType == "" {
		return nil
	}

	var products []model.SyncedProduct
	if err := e.db.WithContext(ctx).
		Where("shop_id = ? AND status = ? AND product_type = ? AND platform_id != ?",
			shopID, "active", ref.ProductType, fmt.Sprintf("%d", productID)).
		Limit(limit).
		Find(&products).Error; err != nil {
		slog.Warn("recommendation_engine: querySameCategory failed", "error", err)
		return nil
	}

	return syncedProductsToRecommendations(products, "Similar product")
}

// queryTrendingFallback returns trending products as a fallback.
func (e *RecommendationEngine) queryTrendingFallback(
	ctx context.Context, shopID uuid.UUID, limit int,
) ([]RecommendationResult, error) {
	var affinities []model.ProductAffinity
	if err := e.db.WithContext(ctx).
		Where("shop_id = ? AND strategy = ? AND product_id_b = 0", shopID, "trending").
		Order("score DESC").
		Limit(limit).
		Find(&affinities).Error; err != nil {
		return nil, fmt.Errorf("queryTrendingFallback: %w", err)
	}

	results := make([]RecommendationResult, 0, len(affinities))
	for _, a := range affinities {
		results = append(results, RecommendationResult{
			ProductID: a.ProductID_A,
			Title:     a.ProductTitle,
			ImageURL:  a.ProductImage,
			Price:     a.ProductPrice,
			Score:     a.Score,
			Reason:    "Trending now",
		})
	}
	return results, nil
}

// extractProductImage extracts the first image URL from the Images JSON field.
func extractProductImage(imagesJSON []byte) string {
	if len(imagesJSON) == 0 {
		return ""
	}
	var images []map[string]interface{}
	if err := json.Unmarshal(imagesJSON, &images); err != nil {
		return ""
	}
	if len(images) > 0 {
		if src, ok := images[0]["src"].(string); ok {
			return src
		}
	}
	return ""
}

// extractProductPrice extracts the price from the first variant.
func extractProductPrice(variantsJSON []byte) string {
	if len(variantsJSON) == 0 {
		return ""
	}
	var variants []map[string]interface{}
	if err := json.Unmarshal(variantsJSON, &variants); err != nil {
		return ""
	}
	if len(variants) > 0 {
		if p, ok := variants[0]["price"].(string); ok {
			return p
		}
		if p, ok := variants[0]["price"].(float64); ok {
			return fmt.Sprintf("%.2f", p)
		}
	}
	return ""
}

// syncedProductsToRecommendations converts SyncedProduct slice to results.
func syncedProductsToRecommendations(products []model.SyncedProduct, reason string) []RecommendationResult {
	results := make([]RecommendationResult, 0, len(products))
	for _, p := range products {
		var pid int64
		fmt.Sscanf(p.PlatformID, "%d", &pid)
		results = append(results, RecommendationResult{
			ProductID: pid,
			Title:     p.Title,
			ImageURL:  extractProductImage(p.Images),
			Price:     extractProductPrice(p.Variants),
			Score:     0.5,
			Reason:    reason,
		})
	}
	return results
}
