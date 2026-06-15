package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JingxuanC/vela-engine/internal/platform/datapipeline"
)

// Region maps countries to major regions.
var countryRegionMap = map[string]string{
	// North America
	"US": "NA", "CA": "NA", "MX": "NA",
	// Europe
	"GB": "EU", "DE": "EU", "FR": "EU", "IT": "EU", "ES": "EU",
	"NL": "EU", "BE": "EU", "CH": "EU", "AT": "EU", "SE": "EU",
	"NO": "EU", "DK": "EU", "FI": "EU", "PT": "EU", "IE": "EU",
	"PL": "EU", "CZ": "EU", "HU": "EU", "RO": "EU", "GR": "EU",
	// Southeast Asia
	"TH": "SEA", "VN": "SEA", "ID": "SEA", "MY": "SEA", "SG": "SEA",
	"PH": "SEA", "MM": "SEA", "KH": "SEA", "LA": "SEA",
	// Middle East
	"AE": "ME", "SA": "ME", "QA": "ME", "KW": "ME", "OM": "ME",
	"BH": "ME", "IL": "ME", "TR": "ME", "EG": "ME",
	// Latin America
	"BR": "LATAM", "AR": "LATAM", "CL": "LATAM", "CO": "LATAM",
	"PE": "LATAM", "UY": "LATAM", "CR": "LATAM", "PA": "LATAM",
	// Asia-Pacific (APAC) — major e-commerce markets.
	"JP": "APAC", "KR": "APAC", "AU": "APAC", "IN": "APAC",
	"NZ": "APAC", "TW": "APAC", "HK": "APAC",
}

// ClassifyRegionStage maps the shipping country to a geographic region.
//
// Expected payload structure:
//
//	"shipping_address" — an object with "country" (ISO 3166-1 alpha-2) or "country_code"
//	— or — top-level "country"
type ClassifyRegionStage struct{}

func init() {
	datapipeline.RegisterStage("classify_region",
		"Maps shipping country code to geographic region (NA/EU/SEA/ME/LATAM)",
		func() datapipeline.Stage { return &ClassifyRegionStage{} },
	)
}

func (s *ClassifyRegionStage) Name() string { return "classify_region" }

func (s *ClassifyRegionStage) IsCritical() bool { return false }

func (s *ClassifyRegionStage) Process(ctx context.Context, data *datapipeline.CleanData) {
	var rawMap map[string]any
	if err := json.Unmarshal(data.Raw.Payload, &rawMap); err != nil {
		data.Errors = append(data.Errors, fmt.Sprintf("classify_region: unmarshal payload: %v", err))
		return
	}

	country := extractCountry(rawMap)
	if country == "" {
		data.Errors = append(data.Errors, "classify_region: no country found in payload")
		return
	}

	region, ok := countryRegionMap[country]
	if !ok {
		region = "OTHER"
	}

	data.Fields["region"] = region
	data.Fields["country"] = country
}

// extractCountry attempts to locate a country code from a parsed JSON map.
func extractCountry(m map[string]any) string {
	// Try shipping_address.country or shipping_address.country_code.
	if sa, ok := m["shipping_address"].(map[string]any); ok {
		if c, ok := sa["country"].(string); ok && c != "" {
			return strings.ToUpper(c)
		}
		if c, ok := sa["country_code"].(string); ok && c != "" {
			return strings.ToUpper(c)
		}
	}

	// Try top-level "country".
	if c, ok := m["country"].(string); ok && c != "" {
		return strings.ToUpper(c)
	}

	return ""
}
