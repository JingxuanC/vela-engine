package visibility

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
)

// ── LLMs.txt Generator ──────────────────────────────────────────────────────

// LLMsTxtGenerator generates AI-readable navigation files for shops.
type LLMsTxtGenerator struct {
	db *gorm.DB
}

// NewLLMsTxtGenerator creates a new LLMsTxtGenerator.
func NewLLMsTxtGenerator(db *gorm.DB) *LLMsTxtGenerator {
	return &LLMsTxtGenerator{db: db}
}

// LLMsTxtResult holds the generated llms.txt content and metadata.
type LLMsTxtResult struct {
	ShopDomain  string    `json:"shop_domain"`
	Content     string    `json:"content"`      // llms.txt content
	FullContent string    `json:"full_content"` // llms-full.txt content
	ProductCount int     `json:"product_count"`
	Sections    []string `json:"sections"`
	GeneratedAt string   `json:"generated_at"`
}

// Generate generates both llms.txt and llms-full.txt for a shop.
// llms.txt: concise overview (top 30 products + policies)
// llms-full.txt: comprehensive (all products with full descriptions)
func (g *LLMsTxtGenerator) Generate(ctx context.Context, shopID string) (*LLMsTxtResult, error) {
	// Get shop info
	var shop model.Shop
	if err := g.db.WithContext(ctx).
		Where("shop_domain = ? OR id = ?", shopID, shopID).
		First(&shop).Error; err != nil {
		// Fallback: use shopID as domain
		shop.Domain = shopID
	}

	// Get products for concise version (top 30)
	var conciseProducts []model.SyncedProduct
	g.db.WithContext(ctx).
		Where("shop_id = ? AND status = ?", shop.ID.String(), "active").
		Order("created_at DESC").Limit(30).
		Find(&conciseProducts)

	// Get all products for full version
	var allProducts []model.SyncedProduct
	g.db.WithContext(ctx).
		Where("shop_id = ? AND status = ?", shop.ID.String(), "active").
		Order("created_at DESC").Limit(500).
		Find(&allProducts)

	domain := shop.Domain

	result := &LLMsTxtResult{
		ShopDomain:   domain,
		ProductCount: len(allProducts),
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	// Build llms.txt (concise)
	result.Content = g.buildConcise(domain, conciseProducts)
	result.Sections = []string{"Store Info", "Featured Products", "Policies", "Product Feed"}

	// Build llms-full.txt (comprehensive)
	result.FullContent = g.buildFull(domain, allProducts)

	return result, nil
}

// buildConcise builds the llms.txt content (AI navigation file).
func (g *LLMsTxtGenerator) buildConcise(domain string, products []model.SyncedProduct) string {
	var sb strings.Builder

	// Header
	sb.WriteString(fmt.Sprintf("# %s\n\n", domain))
	sb.WriteString(fmt.Sprintf("> AI-powered commerce store. All products include structured data for AI agent compatibility.\n\n"))

	// Featured Products
	sb.WriteString("## Featured Products\n\n")
	for _, p := range products {
		price := extractFirstPrice(p.Variants)
		handle := slugify(p.Title) // derive URL-friendly handle from title
		line := fmt.Sprintf("- [%s](https://%s/products/%s): %s", p.Title, domain, handle, p.Title)
		if price > 0 {
			line += fmt.Sprintf(" — $%.2f", price)
		}
		if p.Vendor != "" {
			line += fmt.Sprintf(" | Brand: %s", p.Vendor)
		}
		if p.ProductType != "" {
			line += fmt.Sprintf(" | Category: %s", p.ProductType)
		}
		sb.WriteString(line + "\n")
	}

	// Policies
	sb.WriteString("\n## Policies\n\n")
	sb.WriteString(fmt.Sprintf("- [Shipping Policy](https://%s/policies/shipping-policy): Standard shipping available\n", domain))
	sb.WriteString(fmt.Sprintf("- [Return Policy](https://%s/policies/refund-policy): 30-day returns accepted\n", domain))
	sb.WriteString(fmt.Sprintf("- [Privacy Policy](https://%s/policies/privacy-policy)\n", domain))

	// Product Feed links
	sb.WriteString("\n## Machine-Readable Feeds\n\n")
	sb.WriteString(fmt.Sprintf("- [JSON-LD Product Feed](https://%s/apps/vela/api/geo/feed)\n", domain))
	sb.WriteString(fmt.Sprintf("- [llms-full.txt](https://%s/llms-full.txt) — Complete product catalog for AI agents\n", domain))

	return sb.String()
}

// buildFull builds the llms-full.txt content (comprehensive AI catalog).
func (g *LLMsTxtGenerator) buildFull(domain string, products []model.SyncedProduct) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# %s — Complete AI Product Catalog\n\n", domain))
	sb.WriteString(fmt.Sprintf("> Generated: %s\n", time.Now().UTC().Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("> Total Products: %d\n\n", len(products)))
	sb.WriteString("> This file is designed for AI agents (ChatGPT, Perplexity, Google AI Overviews) to fully understand this store's product catalog.\n\n")

	sb.WriteString("---\n\n")

	for i, p := range products {
		sb.WriteString(fmt.Sprintf("## Product %d: %s\n\n", i+1, p.Title))

		// Description
		if p.Description != "" {
			sb.WriteString(fmt.Sprintf("**Description:** %s\n\n", truncateText(p.Description, 500)))
		}

		// Vendor & Category
		if p.Vendor != "" {
			sb.WriteString(fmt.Sprintf("- **Brand:** %s\n", p.Vendor))
		}
		if p.ProductType != "" {
			sb.WriteString(fmt.Sprintf("- **Category:** %s\n", p.ProductType))
		}
		if p.Tags != "" {
			sb.WriteString(fmt.Sprintf("- **Tags:** %s\n", p.Tags))
		}

		// Variants
		variants := parseVariantArray(p.Variants)
		if len(variants) > 0 {
			sb.WriteString("- **Variants:**\n")
			for _, v := range variants {
				price := v.Price
				if price == "" {
					price = "N/A"
				}
				sb.WriteString(fmt.Sprintf("  - %s: $%s", v.Title, price))
				if v.Sku != "" {
					sb.WriteString(fmt.Sprintf(" (SKU: %s)", v.Sku))
				}
				if v.InventoryQuantity > 0 {
					sb.WriteString(fmt.Sprintf(" [%d in stock]", v.InventoryQuantity))
				}
				sb.WriteString("\n")
			}
		}

		// Images
		images := parseImageArray(p.Images)
		if len(images) > 0 {
			sb.WriteString(fmt.Sprintf("- **Images:** %d available\n", len(images)))
		}

		// Product URL
		sb.WriteString(fmt.Sprintf("- **URL:** https://%s/products/%s\n\n", domain, slugify(p.Title)))

		sb.WriteString("---\n\n")
	}

	// Footer with policies
	sb.WriteString("## Store Policies\n\n")
	sb.WriteString(fmt.Sprintf("- **Shipping:** https://%s/policies/shipping-policy\n", domain))
	sb.WriteString(fmt.Sprintf("- **Returns:** https://%s/policies/refund-policy — 30-day returns\n", domain))
	sb.WriteString(fmt.Sprintf("- **Privacy:** https://%s/policies/privacy-policy\n", domain))

	return sb.String()
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func extractFirstPrice(raw datatypes.JSON) float64 {
	if raw == nil {
		return 0
	}
	var variants []struct {
		Price string `json:"price"`
	}
	_ = json.Unmarshal(raw, &variants)
	for _, v := range variants {
		var p float64
		_, _ = fmt.Sscanf(v.Price, "%f", &p)
		if p > 0 {
			return p
		}
	}
	return 0
}

// slugify converts a product title into a URL-friendly handle.
// NOTE: This is a fallback — Shopify handles are generated at product creation
// and do NOT change when the title changes. For accurate handles, SyncedProduct
// should include a Handle field from Shopify REST API. Until then, slugify
// provides a best-effort approximation that may differ from the real handle.
func slugify(title string) string {
	// Simple transformation: lowercase, replace spaces with hyphens,
	// remove special chars. This is an approximation — the actual
	// Shopify handle may differ.
	s := strings.ToLower(strings.TrimSpace(title))
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "&", "and")
	// Remove remaining special characters
	var result strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}
	return result.String()
}

func truncateText(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	cut := s[:maxLen]
	if lastSpace := strings.LastIndex(cut, " "); lastSpace > 0 {
		cut = cut[:lastSpace]
	}
	return cut + "..."
}
