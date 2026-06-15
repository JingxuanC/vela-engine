package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JingxuanC/vela-engine/internal/platform/datapipeline"
)

// sizeMapping converts common size labels to a standardised form. The keys
// are lowercased for case-insensitive matching.
var sizeMapping = map[string]string{
	"xs":     "Extra Small",
	"s":      "Small",
	"m":      "Medium",
	"l":      "Large",
	"xl":     "Extra Large",
	"xxl":    "Double Extra Large",
	"xxxl":   "Triple Extra Large",
	"small":  "Small",
	"medium": "Medium",
	"large":  "Large",

	// Numeric US shoe sizes (already standard, kept as-is via mapping).
	"us 5":  "US 5",
	"us 6":  "US 6",
	"us 7":  "US 7",
	"us 8":  "US 8",
	"us 9":  "US 9",
	"us 10": "US 10",
	"us 11": "US 11",
	"us 12": "US 12",

	// EU shoe size → US conversion (approximate).
	"eu 35": "US 5",
	"eu 36": "US 6",
	"eu 37": "US 7",
	"eu 38": "US 8",
	"eu 39": "US 8.5",
	"eu 40": "US 9",
	"eu 41": "US 10",
	"eu 42": "US 10.5",
	"eu 43": "US 11",
	"eu 44": "US 12",
	"eu 45": "US 13",
}

// NormalizeSizeStage converts variant size strings to a standard form.
//
// Expected payload structure:
//
//	"variants" — array where each variant may have "size" or "title"
//	— or — top-level "size"
//
// The first variant's size is used for single-product normalization.
type NormalizeSizeStage struct{}

func init() {
	datapipeline.RegisterStage("normalize_size",
		"Normalizes variant size labels (e.g. M→Medium, EU 38→US 8)",
		func() datapipeline.Stage { return &NormalizeSizeStage{} },
	)
}

func (s *NormalizeSizeStage) Name() string { return "normalize_size" }

func (s *NormalizeSizeStage) IsCritical() bool { return false }

func (s *NormalizeSizeStage) Process(ctx context.Context, data *datapipeline.CleanData) {
	var rawMap map[string]any
	if err := json.Unmarshal(data.Raw.Payload, &rawMap); err != nil {
		data.Errors = append(data.Errors, fmt.Sprintf("normalize_size: unmarshal payload: %v", err))
		return
	}

	var rawSize string

	// Try top-level "size".
	if s, ok := rawMap["size"].(string); ok && s != "" {
		rawSize = s
	}

	// Try first variant's "size" or "title".
	if rawSize == "" {
		if variants, ok := rawMap["variants"].([]any); ok && len(variants) > 0 {
			if v, ok := variants[0].(map[string]any); ok {
				if s, ok := v["size"].(string); ok && s != "" {
					rawSize = s
				} else if s, ok := v["title"].(string); ok && s != "" {
					// Some Shopify variants encode size in the title as
					// "M / Black" or "Size 8 / Blue". Extract the first
					// token (the size part) before the separator.
					if idx := strings.Index(s, " / "); idx >= 0 {
						s = strings.TrimSpace(s[:idx])
					}
					rawSize = s
				}
			}
		}
	}

	if rawSize == "" {
		data.Errors = append(data.Errors, "normalize_size: no size found in payload")
		return
	}

	normalized := normalizeSize(rawSize)
	data.Fields["size_raw"] = rawSize
	data.Fields["size_normalized"] = normalized
}

func normalizeSize(raw string) string {
	key := strings.ToLower(strings.TrimSpace(raw))
	if mapped, ok := sizeMapping[key]; ok {
		return mapped
	}

	// If the raw value already looks standardised (e.g. "US 8", numeric),
	// return it as-is.
	if len(raw) > 0 && raw[0] >= '0' && raw[0] <= '9' {
		return raw
	}

	// For single-letter values like "M", apply generic upper-case.
	if len(raw) <= 3 && !strings.ContainsAny(raw, " ") {
		return strings.ToUpper(raw)
	}

	// Keep unrecognised as-is but log it in the data for downstream inspection.
	return raw
}
