package externaldata

import (
	"context"
	"log/slog"
	"time"
)

// PricingData describes category-level pricing insights from EC Compass.
type PricingData struct {
	Category      string     `json:"category"`
	AveragePrice  float64    `json:"average_price"`
	MedianPrice   float64    `json:"median_price"`
	PriceRange    PriceRange `json:"price_range"`
	CompetitorAvg float64    `json:"competitor_avg"`
	SuggestedMin  float64    `json:"suggested_min"`
	SuggestedMax  float64    `json:"suggested_max"`
	SampleSize    int        `json:"sample_size"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// PriceRange defines min/max pricing boundaries.
type PriceRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// StoreInfo holds store-level data from EC Compass.
type StoreInfo struct {
	Domain         string  `json:"domain"`
	StoreName      string  `json:"store_name"`
	MonthlySales   int     `json:"monthly_sales"`
	MonthlyRevenue float64 `json:"monthly_revenue"`
	AvgRating      float64 `json:"avg_rating"`
	ReviewCount    int     `json:"review_count"`
	Category       string  `json:"category"`
	Country        string  `json:"country"`
}

// ECCompassClient is a client for EC Compass (e-commerce data platform).
// When MockMode is true (default), returns simulated demo data.
type ECCompassClient struct {
	apiKey   string
	MockMode bool // default: true (demo data)
}

// NewECCompassClient creates a new ECCompassClient (mock mode by default).
func NewECCompassClient(apiKey string) *ECCompassClient {
	slog.Warn("externaldata: ECCompass running in MOCK mode — all pricing data is simulated")
	return &ECCompassClient{apiKey: apiKey, MockMode: true}
}

// GetCategoryPricing returns pricing data for a category.
// TODO: Integrate with real EC Compass API.
func (c *ECCompassClient) GetCategoryPricing(ctx context.Context, category string) (*PricingData, error) {
	now := time.Now().UTC()
	switch category {
	case "electronics":
		return &PricingData{
			Category:      category,
			AveragePrice:  89.99,
			MedianPrice:   74.99,
			PriceRange:    PriceRange{Min: 9.99, Max: 499.99},
			CompetitorAvg: 84.50,
			SuggestedMin:  49.99,
			SuggestedMax:  129.99,
			SampleSize:    1250,
			UpdatedAt:     now,
		}, nil
	case "clothing":
		return &PricingData{
			Category:      category,
			AveragePrice:  39.99,
			MedianPrice:   34.99,
			PriceRange:    PriceRange{Min: 5.99, Max: 199.99},
			CompetitorAvg: 36.50,
			SuggestedMin:  19.99,
			SuggestedMax:  59.99,
			SampleSize:    3420,
			UpdatedAt:     now,
		}, nil
	case "home_garden":
		return &PricingData{
			Category:      category,
			AveragePrice:  65.00,
			MedianPrice:   49.99,
			PriceRange:    PriceRange{Min: 3.99, Max: 899.99},
			CompetitorAvg: 62.00,
			SuggestedMin:  29.99,
			SuggestedMax:  99.99,
			SampleSize:    890,
			UpdatedAt:     now,
		}, nil
	default:
		return &PricingData{
			Category:      category,
			AveragePrice:  49.99,
			MedianPrice:   44.99,
			PriceRange:    PriceRange{Min: 1.99, Max: 299.99},
			CompetitorAvg: 47.50,
			SuggestedMin:  24.99,
			SuggestedMax:  74.99,
			SampleSize:    500,
			UpdatedAt:     now,
		}, nil
	}
}

// GetStoreInfo returns mock store information for a domain.
// TODO: Integrate with real EC Compass API.
func (c *ECCompassClient) GetStoreInfo(ctx context.Context, domain string) (*StoreInfo, error) {
	return &StoreInfo{
		Domain:         domain,
		StoreName:      domain,
		MonthlySales:   1500,
		MonthlyRevenue: 75000.00,
		AvgRating:      4.2,
		ReviewCount:    340,
		Category:       "general",
		Country:        "US",
	}, nil
}
