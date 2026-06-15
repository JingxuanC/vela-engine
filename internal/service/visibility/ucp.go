package visibility

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
)

// ── UCP Check Engine ─────────────────────────────────────────────────────────

// UCPEngine runs UCP Agent readiness checks against synced product data.
type UCPEngine struct {
	db *gorm.DB
}

// NewUCPEngine creates a new UCPEngine.
func NewUCPEngine(db *gorm.DB) *UCPEngine {
	return &UCPEngine{db: db}
}

// CheckProduct runs all UCP checks for a single product.
func (e *UCPEngine) CheckProduct(ctx context.Context, p *model.SyncedProduct) SubScore {
	checks := []CheckItem{
		e.checkTitle(p),       // 0.20
		e.checkTaxonomy(p),    // 0.15
		e.checkVariants(p),    // 0.15
		e.checkInventory(p),   // 0.10
		e.checkPrice(p),       // 0.10
		e.checkImages(p),      // 0.15
		e.checkDescription(p), // 0.10
		e.checkGlobalCatalog(p), // 0.05 (total = 1.00)
	}

	return CalculateSubScore(checks)
}

// CheckAllProducts runs UCP checks for all active products of a shop.
func (e *UCPEngine) CheckAllProducts(ctx context.Context, shopID string, maxProducts int) ([]ProductReport, error) {
	if maxProducts <= 0 {
		maxProducts = 500
	}

	var products []model.SyncedProduct
	if err := e.db.WithContext(ctx).
		Where("shop_id = ? AND status = ?", shopID, "active").
		Limit(maxProducts).
		Find(&products).Error; err != nil {
		return nil, fmt.Errorf("ucp: query products: %w", err)
	}

	reports := make([]ProductReport, 0, len(products))
	for _, p := range products {
		ucpScore := e.CheckProduct(ctx, &p)
		// GEO and SEO are shop-level concerns — set to neutral for per-product
		geoNeutral := SubScore{Score: 100, Weight: WeightGEO}
		seoNeutral := SubScore{Score: 100, Weight: WeightSEO}
		score := BuildScore(ucpScore, geoNeutral, seoNeutral, p.PlatformID)

		reports = append(reports, ProductReport{
			ProductID: p.PlatformID,
			Title:     p.Title,
			Overall:   score.Overall,
			UCP:       ucpScore,
			GEO:       geoNeutral,
			SEO:       seoNeutral,
			Issues:    score.Issues,
		})
	}

	return reports, nil
}

// ── Individual Checks ────────────────────────────────────────────────────────

// checkTitle verifies the product title includes category/material/key attributes.
// Weight: 20%
func (e *UCPEngine) checkTitle(p *model.SyncedProduct) CheckItem {
	title := strings.TrimSpace(p.Title)

	if title == "" {
		return NewFailCheck(
			"标题具象度", 0.20, 0,
			"产品标题为空，AI 无法理解这是什么产品",
			false,
		)
	}

	score := 100.0
	var issues []string

	// Check length — very short titles lack context
	if utf8.RuneCountInString(title) < 10 {
		score -= 40
		issues = append(issues, "标题过短（少于 10 个字符）")
	}

	// Check for marketing hype patterns
	hypePatterns := []string{"🔥", "!!!", "！！", "爆款", "神器", "限时", "抢购", "必入"}
	for _, hp := range hypePatterns {
		if strings.Contains(title, hp) {
			score -= 30
			issues = append(issues, fmt.Sprintf("标题包含营销用语（%s），AI 偏好事实性描述", hp))
			break
		}
	}

	if len(issues) == 0 {
		return NewPassCheck("标题具象度", 0.20, "标题清晰，AI 可准确理解产品", false)
	}

	return NewFailCheck("标题具象度", 0.20, score,
		fmt.Sprintf("标题质量问题：%s。建议：包含品牌+材质+核心功能", strings.Join(issues, "；")),
		false,
	)
}

// checkTaxonomy verifies the product has a Shopify standard product type.
// Weight: 15%
func (e *UCPEngine) checkTaxonomy(p *model.SyncedProduct) CheckItem {
	pt := strings.TrimSpace(p.ProductType)
	if pt == "" {
		return NewFailCheck(
			"标准分类", 0.15, 0,
			"未设置 Shopify Taxonomy 品类。AI 代理根据品类筛选时可能遗漏此产品",
			false,
		)
	}
	return NewPassCheck("标准分类", 0.15, fmt.Sprintf("已设置品类：%s", pt), false)
}

// checkVariants verifies variant attributes are complete (color, size, material).
// Weight: 15%
func (e *UCPEngine) checkVariants(p *model.SyncedProduct) CheckItem {
	variants := parseVariantArray(p.Variants)
	if len(variants) == 0 {
		return NewFailCheck(
			"变体属性", 0.15, 0,
			"产品没有变体数据，AI 无法了解可选规格",
			false,
		)
	}

	// Check for common variant attributes in the options JSONB
	options := parseOptionsArray(p.Options)
	hasSize := false
	hasColor := false
	for _, opt := range options {
		name := strings.ToLower(opt.Name)
		if strings.Contains(name, "size") || strings.Contains(name, "尺码") || strings.Contains(name, "码") {
			hasSize = true
		}
		if strings.Contains(name, "color") || strings.Contains(name, "colour") || strings.Contains(name, "颜色") || strings.Contains(name, "色") {
			hasColor = true
		}
	}

	score := 100.0
	var missing []string
	if !hasSize {
		score -= 50
		missing = append(missing, "尺码")
	}
	if !hasColor {
		score -= 50
		missing = append(missing, "颜色")
	}

	if len(missing) == 0 {
		return NewPassCheck("变体属性", 0.15, "变体属性（颜色/尺码）完整", false)
	}

	return NewFailCheck("变体属性", 0.15, score,
		fmt.Sprintf("缺少变体属性：%s。AI 代理需要结构化属性来匹配用户需求", strings.Join(missing, "、")),
		false,
	)
}

// checkInventory verifies inventory data is precise and recent.
// Weight: 15%
func (e *UCPEngine) checkInventory(p *model.SyncedProduct) CheckItem {
	variants := parseVariantArray(p.Variants)
	if len(variants) == 0 {
		return NewFailCheck("库存精度", 0.10, 0, "无库存数据", false)
	}

	totalInventory := 0
	for _, v := range variants {
		if v.InventoryQuantity < 0 {
			totalInventory = -1
			break
		}
		totalInventory += v.InventoryQuantity
	}

	if totalInventory < 0 {
		return NewFailCheck(
			"库存精度", 0.15, 20,
			"库存数据异常（存在负数），AI 代理会因此跳过推荐",
			false,
		)
	}

	if totalInventory == 0 {
		return NewFailCheck(
			"库存精度", 0.15, 40,
			"库存为 0 或未追踪。AI 代理倾向于推荐有明确库存的产品",
			false,
		)
	}

	return NewPassCheck("库存精度", 0.10,
		fmt.Sprintf("库存数据正常（总计 %d 件）", totalInventory),
		false,
	)
}

// checkPrice verifies price information is present.
// Weight: 15%
func (e *UCPEngine) checkPrice(p *model.SyncedProduct) CheckItem {
	variants := parseVariantArray(p.Variants)
	if len(variants) == 0 {
		return NewFailCheck("价格信息", 0.10, 0, "无价格数据", false)
	}

	hasPrice := false
	for _, v := range variants {
		if v.Price != "" && v.Price != "0.00" {
			hasPrice = true
			break
		}
	}

	if !hasPrice {
		return NewFailCheck("价格信息", 0.10, 0, "所有变体价格缺失或为 0", false)
	}

	return NewPassCheck("价格信息", 0.10, "价格数据完整", false)
}

// checkImages verifies product images exist and are sufficient.
// Weight: 15%
func (e *UCPEngine) checkImages(p *model.SyncedProduct) CheckItem {
	images := parseImageArray(p.Images)
	if len(images) == 0 {
		return NewFailCheck(
			"图片质量", 0.10, 0,
			"产品没有图片。AI 代理和用户都需要视觉信息来确认产品",
			false,
		)
	}
	if len(images) == 1 {
		return NewFailCheck(
			"图片质量", 0.10, 50,
			"仅有 1 张图片。建议至少提供 3 张（正面/背面/细节）",
			false,
		)
	}
	return NewPassCheck("图片质量", 0.15,
		fmt.Sprintf("有 %d 张图片，视觉数据充足", len(images)),
		false,
	)
}

// checkDescription verifies the product description is substantive.
// Weight: 15%
func (e *UCPEngine) checkDescription(p *model.SyncedProduct) CheckItem {
	desc := strings.TrimSpace(p.Description)

	if desc == "" {
		return NewFailCheck(
			"描述完整度", 0.10, 0,
			"产品描述为空。AI 代理需要文字描述来理解产品特性和使用场景",
			true, // auto-fix via GEO content optimizer
		)
	}

	if utf8.RuneCountInString(desc) < 50 {
		return NewFailCheck(
			"描述完整度", 0.10, 40,
			fmt.Sprintf("描述过短（%d 字符）。AI 偏好丰富的结构化描述（建议 ≥100 字符）",
				utf8.RuneCountInString(desc)),
			true,
		)
	}

	return NewPassCheck("描述完整度", 0.10,
		fmt.Sprintf("描述完整（%d 字符）", utf8.RuneCountInString(desc)),
		false,
	)
}

// ── JSONB Parsers ────────────────────────────────────────────────────────────

// checkGlobalCatalog checks if the product is likely included in Shopify Global Catalog.
// Weight: 5% — informational only until Agents API integration.
func (e *UCPEngine) checkGlobalCatalog(p *model.SyncedProduct) CheckItem {
	if p.Status != "active" {
		return NewFailCheck("Global Catalog", 0.05, 0,
			"产品未发布到在线商店，不会出现在 Global Catalog 中", false)
	}
	return NewPassCheck("Global Catalog", 0.05,
		"产品满足 Global Catalog 基本要求（需 Shopify Agents API 验证收录状态）", false)
}

type variantData struct {
	ID                int    `json:"id"`
	Title             string `json:"title"`
	Price             string `json:"price"`
	Sku               string `json:"sku"`
	Barcode           string `json:"barcode"`
	InventoryQuantity int    `json:"inventory_quantity"`
}

type optionData struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

func parseVariantArray(raw datatypes.JSON) []variantData {
	if raw == nil {
		return nil
	}
	var variants []variantData
	_ = json.Unmarshal(raw, &variants)
	return variants
}

func parseOptionsArray(raw datatypes.JSON) []optionData {
	if raw == nil {
		return nil
	}
	var options []optionData
	_ = json.Unmarshal(raw, &options)
	return options
}

func parseImageArray(raw datatypes.JSON) []struct{ Src string } {
	if raw == nil {
		return nil
	}
	var images []struct{ Src string }
	_ = json.Unmarshal(raw, &images)
	return images
}
