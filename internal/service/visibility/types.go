// Package visibility implements the AI Visibility Suite — UCP, GEO, and SEO
// scoring and optimization for Shopify merchants.
package visibility

import "time"

// ── Top-Level Score ──────────────────────────────────────────────────────────

// VisibilityScore is the top-level AI discovery score for a shop.
type VisibilityScore struct {
	Overall   float64 `json:"overall"` // 0-100, weighted: UCP×0.40 + GEO×0.35 + SEO×0.25
	UCP       SubScore `json:"ucp"`
	GEO       SubScore `json:"geo"`
	SEO       SubScore `json:"seo"`
	Grade     string  `json:"grade"`  // excellent / good / fair / poor
	Issues    []Issue `json:"issues"`
	ScannedAt string  `json:"scanned_at"`
}

// SubScore holds the score and individual checks for one dimension.
type SubScore struct {
	Score  float64     `json:"score"`  // 0-100
	Weight float64     `json:"weight"` // e.g. 0.40, 0.35, 0.25
	Checks []CheckItem `json:"checks"`
}

// CheckItem is a single atomic check within a dimension.
type CheckItem struct {
	Name    string  `json:"name"`
	Weight  float64 `json:"weight"`   // weight within this dimension (sum to 1.0)
	Score   float64 `json:"score"`    // 0-100 for this check
	Passed  bool    `json:"passed"`   // shorthand: score >= threshold
	Message string  `json:"message"`
	AutoFix bool    `json:"auto_fix"`
	FixURL  string  `json:"fix_url,omitempty"`
}

// Issue is a top-level problem surfaced to the merchant.
type Issue struct {
	Severity  string `json:"severity"` // critical / warning / info
	Dimension string `json:"dimension"` // ucp / geo / seo
	CheckName string `json:"check_name"`
	ProductID string `json:"product_id,omitempty"` // empty = shop-wide
	Message   string `json:"message"`
	AutoFix   bool   `json:"auto_fix"`
	FixAction string `json:"fix_action,omitempty"`
}

// ── Grade ────────────────────────────────────────────────────────────────────

// Grade returns a human-readable grade for a 0-100 score.
func Grade(score float64) string {
	switch {
	case score >= 80:
		return "excellent"
	case score >= 60:
		return "good"
	case score >= 40:
		return "fair"
	default:
		return "poor"
	}
}

// GradeEmoji returns an emoji + label for display.
func GradeEmoji(score float64) string {
	switch {
	case score >= 80:
		return "🟢 优秀"
	case score >= 60:
		return "🟡 良好"
	case score >= 40:
		return "🟠 一般"
	default:
		return "🔴 需要优化"
	}
}

// ── Product-Level Report ─────────────────────────────────────────────────────

// ProductReport is a per-product visibility report.
type ProductReport struct {
	ProductID string  `json:"product_id"`
	Title     string  `json:"title"`
	Overall   float64 `json:"overall"`
	UCP       SubScore `json:"ucp"`
	GEO       SubScore `json:"geo"`
	SEO       SubScore `json:"seo"`
	Issues    []Issue `json:"issues"`
}

// ── Scan Options ─────────────────────────────────────────────────────────────

// ScanOptions controls scan behaviour.
type ScanOptions struct {
	ShopID     string   `json:"shop_id"`
	ProductIDs []string `json:"product_ids,omitempty"` // empty = all products
	Dimensions []string `json:"dimensions,omitempty"`  // empty = all (ucp/geo/seo)
	MaxProducts int     `json:"max_products,omitempty"` // 0 = no limit (capped at 500)
}

// ── Dimension Constants ──────────────────────────────────────────────────────

const (
	DimUCP = "ucp"
	DimGEO = "geo"
	DimSEO = "seo"

	WeightUCP = 0.40
	WeightGEO = 0.35
	WeightSEO = 0.25

	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// ── Cache TTL ────────────────────────────────────────────────────────────────

const CacheTTL = 1 * time.Hour
