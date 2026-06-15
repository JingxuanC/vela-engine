package size

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr(f float64) *float64 { return &f }

func TestRecommendSize_AllMeasurements(t *testing.T) {
	engine := NewSizeRecommendationEngine(nil)
	result := engine.RecommendSize("shop1", "p1", map[string]float64{
		"height_cm": 175.0, "weight_kg": 70.0,
		"chest_cm": 96.0, "waist_cm": 80.0, "hip_cm": 98.0,
	})
	require.Empty(t, result.Error)
	assert.Equal(t, "M", result.RecommendedSize)
	assert.GreaterOrEqual(t, result.Confidence, 0.7)
	assert.NotEmpty(t, result.FitNotes)
}

func TestRecommendSize_NoMeasurements(t *testing.T) {
	engine := NewSizeRecommendationEngine(nil)
	result := engine.RecommendSize("shop1", "p1", nil)
	assert.NotEmpty(t, result.Error)
}

func TestRecommendSize_PartialMeasurements(t *testing.T) {
	engine := NewSizeRecommendationEngine(nil)
	result := engine.RecommendSize("shop1", "p1", map[string]float64{
		"height_cm": 175.0, "weight_kg": 70.0,
	})
	require.Empty(t, result.Error)
	// With only 2 dimensions, confidence should be lower
	assert.Less(t, result.Confidence, 0.7)
}

func TestRecommendSize_CustomChart(t *testing.T) {
	customChart := []SizeChartEntry{
		{"S", 80, 90, 65, 75, 85, 95, 160, 170, 50, 60},
		{"M", 90, 100, 75, 85, 95, 105, 168, 178, 60, 75},
	}
	engine := NewSizeRecommendationEngine(customChart)
	result := engine.RecommendSize("shop1", "p1", map[string]float64{
		"chest_cm": 95.0, "waist_cm": 80.0, "hip_cm": 100.0,
	})
	require.Empty(t, result.Error)
	assert.Equal(t, "M", result.RecommendedSize)
}

func TestLearnFromReturns_TooSmall(t *testing.T) {
	engine := NewSizeRecommendationEngine(nil)

	returns := make([]ReturnRecord, 10)
	for i := 0; i < 10; i++ {
		returns[i] = ReturnRecord{
			ProductID:     "p1",
			PurchasedSize: "M",
			Returned:      true,
			Reason:        "too_small",
		}
	}

	result := engine.LearnFromReturns("shop1", returns)
	adj := result["adjustments"].(map[string]interface{})
	assert.Contains(t, adj, "M")
	mAdj := adj["M"].(map[string]float64)
	assert.Greater(t, mAdj["chest_max"], 0.0)
	assert.Greater(t, mAdj["waist_max"], 0.0)
	assert.Equal(t, 10, result["samples"])
}

func TestLearnFromReturns_TooLarge(t *testing.T) {
	engine := NewSizeRecommendationEngine(nil)

	returns := make([]ReturnRecord, 10)
	for i := 0; i < 10; i++ {
		returns[i] = ReturnRecord{
			ProductID:     "p1",
			PurchasedSize: "L",
			Returned:      true,
			Reason:        "too_large",
		}
	}

	result := engine.LearnFromReturns("shop1", returns)
	adj := result["adjustments"].(map[string]interface{})
	assert.Contains(t, adj, "L")
	lAdj := adj["L"].(map[string]float64)
	assert.Greater(t, lAdj["chest_min"], 0.0)
}

func TestLearnFromReturns_NotEnoughData(t *testing.T) {
	engine := NewSizeRecommendationEngine(nil)

	var returns []ReturnRecord
	for i := 0; i < 3; i++ {
		returns = append(returns, ReturnRecord{
			ProductID:     "p1",
			PurchasedSize: "M",
			Returned:      true,
			Reason:        "too_small",
		})
	}

	result := engine.LearnFromReturns("shop1", returns)
	assert.Equal(t, 3, result["samples"])
	assert.Empty(t, result["adjustments"].(map[string]interface{}))
}

func TestLearnFromReturns_Empty(t *testing.T) {
	engine := NewSizeRecommendationEngine(nil)
	result := engine.LearnFromReturns("shop1", nil)
	assert.Equal(t, 0, result["samples"])
}

func TestDimFitScore_Perfect(t *testing.T) {
	assert.Equal(t, 1.0, dimFitScore(90, 80, 100))
}

func TestDimFitScore_Outside(t *testing.T) {
	assert.Equal(t, 0.0, dimFitScore(50, 80, 100))
}

func TestDimFitScore_Marginal(t *testing.T) {
	// 15% margin: for range [80,100], margin=3, soft_lo=77, soft_hi=103
	score := dimFitScore(78, 80, 100)
	assert.Greater(t, score, 0.0)
	assert.Less(t, score, 1.0)
}

func TestDimFitScore_InvalidRange(t *testing.T) {
	assert.Equal(t, 0.0, dimFitScore(90, 100, 80)) // lo >= hi
}

func TestGetBounds(t *testing.T) {
	entry := SizeChartEntry{Label: "M", ChestMin: 90, ChestMax: 100}
	lo, hi := getBounds(entry, "chest_cm")
	assert.Equal(t, 90.0, lo)
	assert.Equal(t, 100.0, hi)
}

func TestDefaultSizeChart(t *testing.T) {
	assert.Len(t, DefaultSizeChart, 7)
	assert.Equal(t, SizeLabel("XS"), DefaultSizeChart[0].Label)
	assert.Equal(t, SizeLabel("3XL"), DefaultSizeChart[6].Label)
}
