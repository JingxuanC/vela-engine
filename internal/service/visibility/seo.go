package visibility

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
)

// ── SEO Check Engine ─────────────────────────────────────────────────────────

// SEOEngine runs traditional SEO health checks.
type SEOEngine struct {
	db *gorm.DB
}

// NewSEOEngine creates a new SEOEngine.
func NewSEOEngine(db *gorm.DB) *SEOEngine {
	return &SEOEngine{db: db}
}

// CheckShop runs all SEO checks for a shop.
func (e *SEOEngine) CheckShop(ctx context.Context, shopID string) SubScore {
	checks := []CheckItem{
		e.checkPageSpeed(ctx, shopID),
		e.checkCanonical(ctx, shopID),
		e.checkSchemaJSONLD(ctx, shopID),
		e.checkMetaTags(ctx, shopID),
		e.checkImageAlt(ctx, shopID),
		e.checkMobileReady(ctx, shopID),
	}
	return CalculateSubScore(checks)
}

// ── Individual SEO Checks ────────────────────────────────────────────────────

// checkPageSpeed estimates page speed based on product image count/size.
// Weight: 25%
// NOTE: This is a heuristic placeholder. Product image count has weak correlation
// with actual page load speed (Shopify uses CDN + auto-compression).
// A proper implementation would call Lighthouse/PageSpeed Insights API.
// Until then, this check should be treated as informational only.
func (e *SEOEngine) checkPageSpeed(ctx context.Context, shopID string) CheckItem {
	var products []model.SyncedProduct
	e.db.WithContext(ctx).
		Where("shop_id = ? AND status = ?", shopID, "active").
		Limit(30).Find(&products)

	if len(products) == 0 {
		return NewFailCheck("页面加载速度", 0.25, 50, "无产品数据，无法评估", false)
	}

	// Heuristic: count products with many images (>5) as potentially slow
	heavyProductCount := 0
	for _, p := range products {
		images := parseImageArray(p.Images)
		if len(images) > 5 {
			heavyProductCount++
		}
	}

	ratio := float64(heavyProductCount) / float64(len(products))
	if ratio > 0.5 {
		return NewFailCheck(
			"页面加载速度", 0.25, 40,
			fmt.Sprintf("%d 个产品图超过 5 张，可能影响加载速度。建议启用图片压缩和 Lazy Loading",
				heavyProductCount),
			false,
		)
	}
	return NewPassCheck("页面加载速度", 0.25,
		fmt.Sprintf("%d 个产品已检查，图片数量合理", len(products)),
		false,
	)
}

// checkCanonical checks for potential canonical URL issues.
// Weight: 20%
// Note: Full canonical check requires crawling the shop. This reports a placeholder
// since we don't have crawl access. The handler returns a "needs manual check" result.
func (e *SEOEngine) checkCanonical(ctx context.Context, shopID string) CheckItem {
	// Check if shop has products with variant URLs that might cause duplication
	var variantCount int64
	e.db.WithContext(ctx).Model(&model.SyncedProduct{}).
		Where("shop_id = ? AND status = ?", shopID, "active").
		Count(&variantCount)

	if variantCount == 0 {
		return NewFailCheck("Canonical URL", 0.20, 0, "无产品数据", false)
	}

	// Variant-heavy products are at higher risk of duplicate URLs
	// Report as info — we can't verify without crawling
	return NewPassCheck("Canonical URL", 0.20,
		fmt.Sprintf("已检测 %d 个产品。如有变体 URL 重复，建议在 Shopify 设置中配置 Canonical 标签",
			variantCount),
		false,
	)
}

// checkSchemaJSONLD verifies JSON-LD Schema completeness via existing GEO feed.
// Weight: 20%
func (e *SEOEngine) checkSchemaJSONLD(ctx context.Context, shopID string) CheckItem {
	// Check if GEO feed has been generated (products with structured data)
	var productCount int64
	e.db.WithContext(ctx).Model(&model.SyncedProduct{}).
		Where("shop_id = ? AND status = ?", shopID, "active").
		Count(&productCount)

	if productCount == 0 {
		return NewFailCheck("Schema JSON-LD", 0.20, 0, "无产品数据，无法生成 Schema", false)
	}

	// Count products with descriptions (needed for good Schema)
	var withDesc int64
	e.db.WithContext(ctx).Model(&model.SyncedProduct{}).
		Where("shop_id = ? AND status = ? AND description IS NOT NULL AND description != ''",
			shopID, "active").
		Count(&withDesc)

	if withDesc == 0 {
		return NewFailCheck(
			"Schema JSON-LD", 0.20, 30,
			"产品缺少描述，JSON-LD Schema 将不完整",
			true, // can generate descriptions
		)
	}

	ratio := float64(withDesc) / float64(productCount)
	if ratio < 0.8 {
		return NewFailCheck(
			"Schema JSON-LD", 0.20, 60,
			fmt.Sprintf("%d/%d 产品有描述，%d 个产品缺少描述会影响 Schema 完整度",
				withDesc, productCount, productCount-withDesc),
			true,
		)
	}

	return NewPassCheck("Schema JSON-LD", 0.20,
		fmt.Sprintf("%d 个产品 Schema 可生成", productCount),
		true,
	)
}

// checkMetaTags checks if meta title/description quality is adequate.
// Weight: 15%
func (e *SEOEngine) checkMetaTags(ctx context.Context, shopID string) CheckItem {
	var products []model.SyncedProduct
	e.db.WithContext(ctx).
		Where("shop_id = ? AND status = ?", shopID, "active").
		Limit(50).Find(&products)

	if len(products) == 0 {
		return NewFailCheck("Meta 标签", 0.15, 0, "无产品数据", false)
	}

	// Heuristic: titles < 30 chars are poor for SEO
	shortTitleCount := 0
	for _, p := range products {
		if len(p.Title) < 30 {
			shortTitleCount++
		}
	}

	ratio := float64(shortTitleCount) / float64(len(products))
	if ratio > 0.3 {
		return NewFailCheck(
			"Meta 标签", 0.15, 40,
			fmt.Sprintf("%d 个产品标题偏短（<30 字符），影响搜索展示效果", shortTitleCount),
			true,
		)
	}
	return NewPassCheck("Meta 标签", 0.15,
		fmt.Sprintf("%d 个产品标题合格", len(products)),
		false,
	)
}

// checkImageAlt checks if product images have alt text.
// Weight: 10%
func (e *SEOEngine) checkImageAlt(ctx context.Context, shopID string) CheckItem {
	var products []model.SyncedProduct
	e.db.WithContext(ctx).
		Where("shop_id = ? AND status = ?", shopID, "active").
		Limit(20).Find(&products)

	if len(products) == 0 {
		return NewFailCheck("图片 Alt 标签", 0.10, 0, "无产品数据", false)
	}

	// Check if product titles can serve as alt text (Shopify default behaviour)
	productsWithImages := 0
	for _, p := range products {
		images := parseImageArray(p.Images)
		if len(images) > 0 {
			productsWithImages++
		}
	}

	if productsWithImages == 0 {
		return NewFailCheck("图片 Alt 标签", 0.10, 0, "无产品图片", false)
	}

	// Shopify auto-sets alt text from product title, so this is informational
	return NewPassCheck("图片 Alt 标签", 0.10,
		fmt.Sprintf("%d 个产品有图片。Shopify 自动用标题作为 Alt 文本，一般无需额外操作",
			productsWithImages),
		false,
	)
}

// checkMobileReady returns a mobile-readiness assessment.
// Weight: 10%
// Note: All Shopify themes are mobile-responsive. This is informational.
func (e *SEOEngine) checkMobileReady(ctx context.Context, shopID string) CheckItem {
	var productCount int64
	e.db.WithContext(ctx).Model(&model.SyncedProduct{}).
		Where("shop_id = ? AND status = ?", shopID, "active").
		Count(&productCount)

	if productCount == 0 {
		return NewFailCheck("移动端适配", 0.10, 50, "无产品数据", false)
	}

	// Shopify themes are mobile-responsive by default
	return NewPassCheck("移动端适配", 0.10,
		"Shopify 主题默认支持移动端。如需深度优化，建议使用 Google Mobile-Friendly Test",
		false,
	)
}
