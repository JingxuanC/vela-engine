package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JingxuanC/vela-engine/internal/platform/datapipeline"
)

// currencyRates maps ISO currency codes to their approximate USD conversion
// rates. These are static reference rates for the pipeline; in production
// they would be fetched from a live FX API.
var currencyRates = map[string]float64{
	"USD": 1.0,
	"EUR": 1.08,
	"GBP": 1.26,
	"JPY": 0.0067,
	"CAD": 0.74,
	"AUD": 0.66,
	"CNY": 0.14,
	"KRW": 0.00075,
	"SGD": 0.74,
	"HKD": 0.128,
	"INR": 0.012,
	"MXN": 0.058,
	"BRL": 0.20,
	"SEK": 0.096,
	"NOK": 0.094,
}

// currencyToUSD returns the USD conversion rate for an ISO 4217 currency code.
// It normalizes the code to uppercase before lookup. Returns (rate, true) on
// success or (0, false) if the currency is unknown.
func currencyToUSD(code string) (float64, bool) {
	rate, ok := currencyRates[code]
	return rate, ok
}

// NormalizeCurrencyStage extracts a price from the raw payload and converts
// it to USD using built-in exchange rates.
//
// Expected payload fields (JSON):
//
//	"price" — a number, or
//	"variants" — an array whose first element has "price"
//	"currency" — the source currency code (default: "USD")
type NormalizeCurrencyStage struct{}

func init() {
	datapipeline.RegisterStage("normalize_currency",
		"Converts product price to USD using reference exchange rates",
		func() datapipeline.Stage { return &NormalizeCurrencyStage{} },
	)
}

func (s *NormalizeCurrencyStage) Name() string { return "normalize_currency" }

func (s *NormalizeCurrencyStage) IsCritical() bool { return false }

func (s *NormalizeCurrencyStage) Process(ctx context.Context, data *datapipeline.CleanData) {
	var rawMap map[string]any
	if err := json.Unmarshal(data.Raw.Payload, &rawMap); err != nil {
		data.Errors = append(data.Errors, fmt.Sprintf("normalize_currency: unmarshal payload: %v", err))
		return
	}

	// Determine source currency (default USD). Normalize to uppercase
	// since currency codes are case-insensitive per ISO 4217.
	currency, _ := rawMap["currency"].(string)
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "USD"
	}

	rate, ok := currencyToUSD(currency)
	if !ok {
		data.Errors = append(data.Errors, fmt.Sprintf("normalize_currency: unknown currency %q", currency))
		return
	}

	// Extract price — try top-level "price" first, then first variant's price.
	price := extractPrice(rawMap)
	if price == 0 {
		data.Errors = append(data.Errors, "normalize_currency: no price found in payload")
		return
	}

	usdPrice := price * rate
	data.Fields["currency_normalized"] = "USD"
	data.Fields["price_usd"] = usdPrice
	data.Fields["price_original"] = price
	data.Fields["currency_src"] = currency
}

// extractPrice attempts to find a numeric price from raw payload fields.
func extractPrice(m map[string]any) float64 {
	// Try top-level "price".
	if p, ok := m["price"].(float64); ok && p > 0 {
		return p
	}
	// String price at top level.
	if ps, ok := m["price"].(string); ok {
		var fp float64
		if _, err := fmt.Sscanf(ps, "%f", &fp); err == nil && fp > 0 {
			return fp
		}
	}

	// Try "variants" array, first variant's "price".
	if variants, ok := m["variants"].([]any); ok && len(variants) > 0 {
		if v, ok := variants[0].(map[string]any); ok {
			if p, ok := v["price"].(float64); ok && p > 0 {
				return p
			}
			// Some JSON parsers deliver string prices.
			if ps, ok := v["price"].(string); ok {
				var fp float64
				if _, err := fmt.Sscanf(ps, "%f", &fp); err == nil && fp > 0 {
					return fp
				}
			}
		}
	}
	return 0
}
