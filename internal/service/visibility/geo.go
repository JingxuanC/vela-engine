package visibility

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
)

// ── GEO Check Engine ─────────────────────────────────────────────────────────

// GEOEngine runs GEO AI search visibility checks.
type GEOEngine struct {
	db *gorm.DB
}

// NewGEOEngine creates a new GEOEngine.
func NewGEOEngine(db *gorm.DB) *GEOEngine {
	return &GEOEngine{db: db}
}

// CheckShop runs all GEO checks for a shop (shop-wide concerns like llms.txt).
func (e *GEOEngine) CheckShop(ctx context.Context, shopID string, llmsDeployed bool) SubScore {
	checks := []CheckItem{
		e.checkLLMsTxt(llmsDeployed),
		e.checkLLMsFull(llmsDeployed),
	}

	// Add per-product aggregate checks: sample top 10 products
	var products []model.SyncedProduct
	e.db.WithContext(ctx).
		Where("shop_id = ? AND status = ?", shopID, "active").
		Limit(10).Find(&products)

	descScore := e.checkDescriptionQuality(products)
	faqScore := e.checkFAQPresence(products)
	titleMatchScore := e.checkTitleSearchability(products)

	checks = append(checks, descScore, faqScore, titleMatchScore)
	checks = append(checks, e.checkReviewStructured(ctx, shopID))
	checks = append(checks, e.checkBrandAuthority(products))

	return CalculateSubScore(checks)
}

// ── Individual GEO Checks ────────────────────────────────────────────────────

// checkLLMsTxt verifies llms.txt is deployed.
// Weight: 25%
func (e *GEOEngine) checkLLMsTxt(deployed bool) CheckItem {
	if deployed {
		return NewPassCheck("LLMs.txt 部署", 0.25, "llms.txt 已部署，AI 爬虫可快速理解店铺结构", true)
	}
	return NewFailCheck(
		"LLMs.txt 部署", 0.25, 0,
		"llms.txt 未部署。AI 爬虫缺少结构化导航信息，可能遗漏重要产品",
		true, // auto-fix: generate + deploy
	)
}

// checkLLMsFull verifies llms-full.txt is deployed.
// Weight: 10% (folded into LLMs check — not a separate item in GEO spec, but useful)
func (e *GEOEngine) checkLLMsFull(deployed bool) CheckItem {
	if deployed {
		return NewPassCheck("LLMs-full.txt 部署", 0.05, "完整版 AI 导航文件已部署", true)
	}
	return NewFailCheck(
		"LLMs-full.txt 部署", 0.05, 30,
		"llms-full.txt 未部署。建议生成全量版以提供完整产品信息给 AI",
		true,
	)
}

// checkDescriptionQuality checks if product descriptions contain structured info.
// Weight: 20%
func (e *GEOEngine) checkDescriptionQuality(products []model.SyncedProduct) CheckItem {
	if len(products) == 0 {
		return NewFailCheck("描述结构化", 0.20, 50, "无产品数据", false)
	}

	shortCount := 0
	for _, p := range products {
		if utf8.RuneCountInString(strings.TrimSpace(p.Description)) < 100 {
			shortCount++
		}
	}

	ratio := float64(shortCount) / float64(len(products))
	if ratio > 0.5 {
		return NewFailCheck(
			"描述结构化", 0.20, 30,
			fmt.Sprintf("%d/%d 产品描述偏短。AI 搜索偏好丰富的结构化描述（≥100 字符）",
				shortCount, len(products)),
			true, // auto-fix via GEO optimizer
		)
	}
	if ratio > 0.2 {
		return NewFailCheck(
			"描述结构化", 0.20, 70,
			fmt.Sprintf("%d 个产品描述可进一步优化", shortCount),
			true,
		)
	}
	return NewPassCheck("描述结构化", 0.20, "产品描述长度充足，AI 可提取关键信息", false)
}

// checkFAQPresence checks if FAQ content exists.
// Weight: 15%
// NOTE: This is a heuristic check — we scan product descriptions for Q&A patterns
// like "Q:" or "FAQ". It may produce false positives if merchants use these patterns
// outside of a dedicated FAQ section. A proper FAQ detector would need page crawling.
func (e *GEOEngine) checkFAQPresence(products []model.SyncedProduct) CheckItem {
	if len(products) == 0 {
		return NewFailCheck("FAQ 模块", 0.15, 0, "无产品数据", false)
	}

	faqPatterns := []string{"Q:", "问：", "FAQ", "常见问题", "Q&A"}
	hasAnyFAQ := false
	for _, p := range products {
		desc := strings.ToLower(p.Description)
		for _, pat := range faqPatterns {
			if strings.Contains(desc, strings.ToLower(pat)) {
				hasAnyFAQ = true
				break
			}
		}
		if hasAnyFAQ {
			break
		}
	}

	if hasAnyFAQ {
		return NewPassCheck("FAQ 模块", 0.15, "检测到 FAQ 内容", false)
	}
	return NewFailCheck(
		"FAQ 模块", 0.15, 0,
		"未检测到 FAQ 内容。AI 搜索偏好抓取能回答用户具体问题的页面。建议在产品页添加 FAQ 模块",
		true, // auto-fix via AI FAQ generation
	)
}

// checkTitleSearchability checks if titles can be matched by question-style searches.
// Weight: 15%
func (e *GEOEngine) checkTitleSearchability(products []model.SyncedProduct) CheckItem {
	if len(products) == 0 {
		return NewFailCheck("标题可搜索性", 0.15, 50, "无产品数据", false)
	}

	poorTitles := 0
	for _, p := range products {
		title := strings.TrimSpace(p.Title)
		// Good titles contain specific modifiers: material, size, use-case
		hasSpecifics := false
		specificWords := []string{"%", "cm", "mm", "kg", "g ", "cotton", "棉", "wool", "羊毛",
			"waterproof", "防水", "breathable", "透气", "organic", "有机"}
		titleLower := strings.ToLower(title)
		for _, w := range specificWords {
			if strings.Contains(titleLower, w) {
				hasSpecifics = true
				break
			}
		}
		if !hasSpecifics && utf8.RuneCountInString(title) < 20 {
			poorTitles++
		}
	}

	ratio := float64(poorTitles) / float64(len(products))
	if ratio > 0.3 {
		return NewFailCheck(
			"标题可搜索性", 0.15, 40,
			fmt.Sprintf("%d 个产品标题缺少具体属性，难以匹配用户的问题式搜索", poorTitles),
			true, // AI can suggest better titles
		)
	}
	return NewPassCheck("标题可搜索性", 0.15, "产品标题包含具体属性，可匹配问题式搜索", false)
}

// checkReviewStructured checks if review data is available for structured output.
// Weight: 15%
func (e *GEOEngine) checkReviewStructured(ctx context.Context, shopID string) CheckItem {
	var count int64
	e.db.WithContext(ctx).Model(&model.SyncedReview{}).
		Where("shop_id = ?", shopID).Count(&count)

	if count == 0 {
		return NewFailCheck(
			"评价数据结构化", 0.15, 0,
			"没有评价数据。AggregateRating Schema 无法生成，AI 缺少可信度信号",
			false,
		)
	}
	return NewPassCheck("评价数据结构化", 0.15,
		fmt.Sprintf("有 %d 条评价，可生成 AggregateRating Schema", count),
		false,
	)
}

// checkBrandAuthority checks brand/author authority signals.
// Weight: 10%
func (e *GEOEngine) checkBrandAuthority(products []model.SyncedProduct) CheckItem {
	if len(products) == 0 {
		return NewFailCheck("品牌权威性", 0.10, 0, "无产品数据", false)
	}

	hasVendor := 0
	for _, p := range products {
		if strings.TrimSpace(p.Vendor) != "" {
			hasVendor++
		}
	}

	ratio := float64(hasVendor) / float64(len(products))
	if ratio >= 0.8 {
		return NewPassCheck("品牌权威性", 0.10,
			fmt.Sprintf("%d/%d 产品有品牌信息", hasVendor, len(products)),
			false,
		)
	}
	return NewFailCheck(
		"品牌权威性", 0.10, 30,
		fmt.Sprintf("仅 %d/%d 产品有品牌信息。AI 重视品牌可信度，建议补全 Vendor 字段",
			hasVendor, len(products)),
		false,
	)
}
