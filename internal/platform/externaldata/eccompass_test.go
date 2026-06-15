package externaldata

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewECCompassClient(t *testing.T) {
	c := NewECCompassClient("test-key")
	assert.NotNil(t, c)
	assert.Equal(t, "test-key", c.apiKey)
}

func TestECCompassClient_GetCategoryPricing(t *testing.T) {
	c := NewECCompassClient("test-key")
	ctx := context.Background()

	t.Run("returns pricing data for known category", func(t *testing.T) {
		data, err := c.GetCategoryPricing(ctx, "electronics")
		require.NoError(t, err)
		require.NotNil(t, data)

		assert.Equal(t, "electronics", data.Category)
		assert.Equal(t, 89.99, data.AveragePrice)
		assert.Equal(t, 74.99, data.MedianPrice)
		assert.Equal(t, 9.99, data.PriceRange.Min)
		assert.Equal(t, 499.99, data.PriceRange.Max)
		assert.Equal(t, 84.50, data.CompetitorAvg)
		assert.NotZero(t, data.SampleSize)
		assert.False(t, data.UpdatedAt.IsZero())
	})

	t.Run("returns pricing for clothing", func(t *testing.T) {
		data, err := c.GetCategoryPricing(ctx, "clothing")
		require.NoError(t, err)
		require.NotNil(t, data)
		assert.Equal(t, "clothing", data.Category)
		assert.Equal(t, 39.99, data.AveragePrice)
		assert.True(t, data.SuggestedMin < data.SuggestedMax)
	})

	t.Run("returns pricing for home_garden", func(t *testing.T) {
		data, err := c.GetCategoryPricing(ctx, "home_garden")
		require.NoError(t, err)
		require.NotNil(t, data)
		assert.Equal(t, "home_garden", data.Category)
	})

	t.Run("returns default pricing for unknown category", func(t *testing.T) {
		data, err := c.GetCategoryPricing(ctx, "unknown_category_xyz")
		require.NoError(t, err)
		require.NotNil(t, data)
		assert.Equal(t, "unknown_category_xyz", data.Category)
		assert.Equal(t, 49.99, data.AveragePrice)
		assert.Equal(t, float64(500), float64(data.SampleSize))
	})

	t.Run("suggested range is reasonable", func(t *testing.T) {
		categories := []string{"electronics", "clothing", "home_garden", "toys"}
		for _, cat := range categories {
			data, err := c.GetCategoryPricing(ctx, cat)
			require.NoError(t, err)
			assert.True(t, data.SuggestedMin > 0, "%s: SuggestedMin should be > 0", cat)
			assert.True(t, data.SuggestedMax > data.SuggestedMin,
				"%s: SuggestedMax (%f) should be > SuggestedMin (%f)", cat, data.SuggestedMax, data.SuggestedMin)
			assert.True(t, data.AveragePrice >= data.PriceRange.Min,
				"%s: AveragePrice should be >= PriceRange.Min", cat)
			assert.True(t, data.AveragePrice <= data.PriceRange.Max,
				"%s: AveragePrice should be <= PriceRange.Max", cat)
		}
	})
}

func TestECCompassClient_GetStoreInfo(t *testing.T) {
	c := NewECCompassClient("test-key")
	ctx := context.Background()

	t.Run("returns store info for a domain", func(t *testing.T) {
		info, err := c.GetStoreInfo(ctx, "example.myshopify.com")
		require.NoError(t, err)
		require.NotNil(t, info)

		assert.Equal(t, "example.myshopify.com", info.Domain)
		assert.NotEmpty(t, info.StoreName)
		assert.True(t, info.MonthlySales > 0)
		assert.True(t, info.MonthlyRevenue > 0)
		assert.True(t, info.AvgRating >= 1.0 && info.AvgRating <= 5.0)
		assert.True(t, info.ReviewCount > 0)
		assert.NotEmpty(t, info.Category)
		assert.NotEmpty(t, info.Country)
	})

	t.Run("store name defaults to domain", func(t *testing.T) {
		info, err := c.GetStoreInfo(ctx, "my-store-123.com")
		require.NoError(t, err)
		assert.Equal(t, "my-store-123.com", info.StoreName)
	})
}
