package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/JingxuanC/vela-engine/internal/platform/datapipeline"
)

// materialPattern matches percentage-based material declarations such as
// "100% cotton", "65% polyester 35% cotton". Case-insensitive.
var materialPattern = regexp.MustCompile(`(?i)(\d+)\s*%\s*(cotton|polyester|nylon|spandex|elastane|wool|silk|linen|rayon|viscose|acrylic|leather|cashmere|bamboo|modal|lycra|denim|lace|satin|velvet|tweed|corduroy|jersey|fleece|mesh|suede|chiffon|organza|tulle)`)

// ExtractMaterialStage extracts material composition from product body_html
// or title using regex matching.
//
// Expected payload fields:
//   - "body_html" — HTML description (preferred, often contains material tags)
//   - "title" — product title (fallback)
//   - "description" — alternative field name for body_html
//
// Output fields:
//   - "materials" — []map[string]any (each: {"material": "...", "percentage": float64})
type ExtractMaterialStage struct{}

func init() {
	datapipeline.RegisterStage("extract_material",
		"Extracts material composition from body_html/title using regex",
		func() datapipeline.Stage { return &ExtractMaterialStage{} },
	)
}

func (s *ExtractMaterialStage) Name() string { return "extract_material" }

func (s *ExtractMaterialStage) IsCritical() bool { return false }

func (s *ExtractMaterialStage) Process(ctx context.Context, data *datapipeline.CleanData) {
	var rawMap map[string]any
	if err := json.Unmarshal(data.Raw.Payload, &rawMap); err != nil {
		data.Errors = append(data.Errors, fmt.Sprintf("extract_material: unmarshal payload: %v", err))
		return
	}

	// Collect text sources to search.
	var sources []string
	if body, ok := rawMap["body_html"].(string); ok && body != "" {
		sources = append(sources, body)
	}
	if title, ok := rawMap["title"].(string); ok && title != "" {
		sources = append(sources, title)
	}
	if desc, ok := rawMap["description"].(string); ok && desc != "" {
		sources = append(sources, desc)
	}

	allMatches := extractMaterials(sources)
	if len(allMatches) == 0 {
		data.Errors = append(data.Errors, "extract_material: no material composition found")
		return
	}

	data.Fields["materials"] = allMatches
}

// extractMaterials searches the given text sources for material patterns and
// returns a deduplicated list of material entries.
func extractMaterials(sources []string) []map[string]any {
	seen := make(map[string]bool)
	var result []map[string]any

	for _, src := range sources {
		matches := materialPattern.FindAllStringSubmatch(src, -1)
		for _, m := range matches {
			mat := strings.ToLower(m[2])
			if seen[mat] {
				continue
			}
			seen[mat] = true

			var pct float64
			if n, _ := fmt.Sscanf(m[1], "%f", &pct); n != 1 {
				// Percentage parsing failed; use 0 but this shouldn't
				// happen since the regex guarantees a digit sequence.
				pct = 0
			}

			result = append(result, map[string]any{
				"material":   mat,
				"percentage": pct,
			})
		}
	}
	return result
}
