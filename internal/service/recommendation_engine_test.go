package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClamp(t *testing.T) {
	assert.Equal(t, 0.0, clamp(-1, 0, 100))
	assert.Equal(t, 100.0, clamp(200, 0, 100))
	assert.Equal(t, 50.0, clamp(50, 0, 100))
	assert.Equal(t, 0.0, clamp(0, 0, 100))
	assert.Equal(t, 100.0, clamp(100, 0, 100))
}

func TestParsePrice(t *testing.T) {
	tests := []struct {
		input    string
		expected float64
		hasError bool
	}{
		{"19.99", 19.99, false},
		{"0.01", 0.01, false},
		{"100", 100, false},
		{"", 0, true},
		{"abc", 0, true},
		{" 49.50 ", 49.50, false},
	}
	for _, tc := range tests {
		result, err := parsePrice(tc.input)
		if tc.hasError {
			assert.Error(t, err, "input: %q", tc.input)
		} else {
			assert.NoError(t, err, "input: %q", tc.input)
			assert.Equal(t, tc.expected, result, "input: %q", tc.input)
		}
	}
}

func TestAvgTopN_Earliest(t *testing.T) {
	orders := []singleOrderRow{
		{TotalPrice: 10}, // earliest
		{TotalPrice: 20},
		{TotalPrice: 30},
		{TotalPrice: 40}, // newest
	}
	result := avgTopN(orders, 2, true)
	assert.Equal(t, 15.0, result) // (10+20)/2
}

func TestAvgTopN_Latest(t *testing.T) {
	orders := []singleOrderRow{
		{TotalPrice: 10},
		{TotalPrice: 20},
		{TotalPrice: 30},
		{TotalPrice: 40},
	}
	result := avgTopN(orders, 2, false)
	assert.Equal(t, 35.0, result) // (30+40)/2
}

func TestAvgTopN_NotEnoughOrders(t *testing.T) {
	orders := []singleOrderRow{{TotalPrice: 50}}
	result := avgTopN(orders, 3, false)
	assert.Equal(t, 50.0, result) // only 1, n=3→clamped to 1
}

func TestAvgTopN_Empty(t *testing.T) {
	result := avgTopN(nil, 3, true)
	assert.Equal(t, 0.0, result)
}

func TestRecommendationEngine_ExtractProductImage(t *testing.T) {
	// Valid JSON image array
	img := extractProductImage([]byte(`[{"src":"https://cdn.shopify.com/a.jpg"}]`))
	assert.Equal(t, "https://cdn.shopify.com/a.jpg", img)

	// Empty images
	assert.Equal(t, "", extractProductImage(nil))
	assert.Equal(t, "", extractProductImage([]byte(`[]`)))

	// Invalid JSON
	assert.Equal(t, "", extractProductImage([]byte(`not-json`)))
}

func TestRecommendationEngine_ExtractProductPrice(t *testing.T) {
	// String price
	p := extractProductPrice([]byte(`[{"price":"29.99"}]`))
	assert.Equal(t, "29.99", p)

	// Float price
	p = extractProductPrice([]byte(`[{"price":39.99}]`))
	assert.Equal(t, "39.99", p)

	// Empty
	assert.Equal(t, "", extractProductPrice(nil))
}
