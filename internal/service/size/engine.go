// Package size provides a rule-based size recommendation engine.
package size

import (
	"fmt"
	"math"
	"sync"
)

// SizeLabel represents a clothing size label.
type SizeLabel string

// SizeChartEntry is a single row in a size chart.
// All measurements are in centimeters and represent the recommended
// body measurement range for that size.
type SizeChartEntry struct {
	Label     SizeLabel
	ChestMin  float64
	ChestMax  float64
	WaistMin  float64
	WaistMax  float64
	HipMin    float64
	HipMax    float64
	HeightMin float64
	HeightMax float64
	WeightMin float64
	WeightMax float64
}

// SizeRecommendResult is the output of the recommendation engine.
type SizeRecommendResult struct {
	RecommendedSize string
	Confidence      float64
	AlternativeSize string
	FitNotes        string
	Error           string
}

// ReturnRecord represents a single return/exchange log entry.
type ReturnRecord struct {
	ProductID     string
	PurchasedSize string
	Returned      bool
	Reason        string // "too_small", "too_large"
	Measurements  map[string]float64
}

// Dimension weights for scoring.
var dimWeights = map[string]float64{
	"chest_cm":  0.30,
	"waist_cm":  0.25,
	"hip_cm":    0.15,
	"height_cm": 0.15,
	"weight_kg": 0.15,
}

// DefaultSizeChart provides a standard unisex size chart.
var DefaultSizeChart = []SizeChartEntry{
	{"XS", 76, 84, 60, 68, 82, 90, 155, 165, 45, 55},
	{"S", 84, 92, 68, 76, 90, 96, 160, 172, 55, 65},
	{"M", 92, 100, 76, 84, 96, 102, 165, 178, 62, 75},
	{"L", 100, 108, 84, 92, 102, 108, 170, 185, 72, 85},
	{"XL", 108, 116, 92, 100, 108, 114, 175, 190, 82, 98},
	{"2XL", 116, 126, 100, 110, 114, 124, 180, 195, 95, 110},
	{"3XL", 126, 140, 110, 125, 124, 138, 185, 200, 108, 130},
}

// SizeRecommendationEngine provides size recommendations from body measurements.
type SizeRecommendationEngine struct {
	mu            sync.RWMutex
	chart         []SizeChartEntry
	productCharts map[string][]SizeChartEntry
	adjustments   map[string]map[string]map[string]float64
}

// NewSizeRecommendationEngine creates a new engine with the given size chart.
func NewSizeRecommendationEngine(chart []SizeChartEntry) *SizeRecommendationEngine {
	if chart == nil {
		chartCopy := make([]SizeChartEntry, len(DefaultSizeChart))
		copy(chartCopy, DefaultSizeChart)
		chart = chartCopy
	}
	return &SizeRecommendationEngine{
		chart:         chart,
		productCharts: make(map[string][]SizeChartEntry),
		adjustments:   make(map[string]map[string]map[string]float64),
	}
}

// RecommendSize recommends a size for a product based on body measurements.
func (e *SizeRecommendationEngine) RecommendSize(shopID, productID string, measurements map[string]float64) SizeRecommendResult {
	if len(measurements) == 0 {
		return SizeRecommendResult{Error: "No measurements provided for size recommendation."}
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	chart := e.chart
	if pc, ok := e.productCharts[productID]; ok {
		chart = pc
	}

	// Score each size
	scores := e.scoreSizes(shopID, measurements, chart)
	if len(scores) == 0 {
		return SizeRecommendResult{Error: "Could not compute size recommendation."}
	}

	bestLabel, bestScore := scores[0].label, scores[0].score
	altLabel := ""
	if len(scores) > 1 {
		altLabel = scores[1].label
	}

	// Confidence = score * completeness * separation
	completeness := float64(len(measurements)) / float64(len(dimWeights))
	separation := 1.0
	if len(scores) > 1 {
		separation = math.Min(1.0, (bestScore-scores[1].score)*3+0.5)
	}
	confidence := roundTo(bestScore*completeness*separation, 4)
	confidence = math.Max(0.0, math.Min(1.0, confidence))

	notes := generateFitNotes(bestLabel, altLabel, bestScore)

	return SizeRecommendResult{
		RecommendedSize: bestLabel,
		Confidence:      confidence,
		AlternativeSize: altLabel,
		FitNotes:        notes,
	}
}

// LearnFromReturns analyzes return data and adjusts fit thresholds.
func (e *SizeRecommendationEngine) LearnFromReturns(shopID string, returnData []ReturnRecord) map[string]interface{} {
	if len(returnData) == 0 {
		return map[string]interface{}{
			"shop_id":     shopID,
			"adjustments": map[string]float64{},
			"samples":     0,
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	tooSmallBySize := make(map[string]int)
	tooLargeBySize := make(map[string]int)
	totalBySize := make(map[string]int)

	for _, rec := range returnData {
		sz := rec.PurchasedSize
		totalBySize[sz]++
		if rec.Returned {
			switch rec.Reason {
			case "too_small":
				tooSmallBySize[sz]++
			case "too_large":
				tooLargeBySize[sz]++
			}
		}
	}

	sizeAdjustments := make(map[string]map[string]float64)
	const significanceThreshold = 0.20 // 20%

	for sz, count := range totalBySize {
		if count < 5 {
			continue
		}

		adj := make(map[string]float64)

		if smallRatio := float64(tooSmallBySize[sz]) / float64(count); smallRatio >= significanceThreshold {
			shift := math.Min(5.0, (smallRatio-significanceThreshold)*5)
			adj["waist_max"] += shift
			adj["chest_max"] += shift
		}
		if largeRatio := float64(tooLargeBySize[sz]) / float64(count); largeRatio >= significanceThreshold {
			shift := math.Min(5.0, (largeRatio-significanceThreshold)*5)
			adj["waist_min"] += shift
			adj["chest_min"] += shift
		}

		if len(adj) > 0 {
			sizeAdjustments[sz] = adj
		}
	}

	if len(sizeAdjustments) > 0 {
		e.adjustments[shopID] = sizeAdjustments
	}

	// Convert to interface{} for the return map
	adjInterface := make(map[string]interface{})
	for k, v := range sizeAdjustments {
		adjInterface[k] = v
	}

	return map[string]interface{}{
		"shop_id":     shopID,
		"adjustments": adjInterface,
		"samples":     len(returnData),
	}
}

type sizeScore struct {
	label string
	score float64
}

func (e *SizeRecommendationEngine) scoreSizes(shopID string, measurements map[string]float64, chart []SizeChartEntry) []sizeScore {
	var results []sizeScore

	for _, entry := range chart {
		totalWeight := 0.0
		totalScore := 0.0

		for dimKey, dimVal := range measurements {
			weight := dimWeights[dimKey]
			if weight == 0 {
				weight = 0.15
			}
			lo, hi := getBounds(entry, dimKey)
			lo, hi = e.getAdjustedBounds(shopID, string(entry.Label), dimKey, lo, hi)
			s := dimFitScore(dimVal, lo, hi)
			totalScore += weight * s
			totalWeight += weight
		}

		if totalWeight > 0 {
			results = append(results, sizeScore{string(entry.Label), totalScore / totalWeight})
		}
	}

	// Sort by score descending
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].score > results[i].score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	return results
}

func (e *SizeRecommendationEngine) getAdjustedBounds(shopID, sizeLabel, dimKey string, lo, hi float64) (float64, float64) {
	merged := make(map[string]float64)
	if sizeAdj, ok := e.adjustments[shopID]; ok {
		for sizes := range sizeAdj {
			if sizes == sizeLabel {
				for dim, shift := range sizeAdj[sizes] {
					merged[dim] = shift
				}
			}
		}
	}
	if len(merged) == 0 {
		return lo, hi
	}

	adjLoKey := ""
	adjHiKey := ""
	switch dimKey {
	case "chest_cm", "waist_cm", "hip_cm", "height_cm":
		adjLoKey = dimKey[:len(dimKey)-3] + "_min"
		adjHiKey = dimKey[:len(dimKey)-3] + "_max"
	case "weight_kg":
		adjLoKey = "weight_min"
		adjHiKey = "weight_max"
	}

	loAdj := merged[adjLoKey]
	hiAdj := merged[adjHiKey]
	return lo + loAdj, hi + hiAdj
}

func generateFitNotes(size, altSize string, score float64) string {
	notes := fmt.Sprintf("Based on your measurements, size %s should fit well.", size)
	if altSize != "" {
		notes += fmt.Sprintf(" Consider size %s if you prefer a looser or tighter fit.", altSize)
	}
	if score < 0.5 {
		notes += " The fit estimate has lower confidence due to limited measurement data. We recommend checking the brand's specific size guide."
	}
	return notes
}

func getBounds(entry SizeChartEntry, dimKey string) (float64, float64) {
	switch dimKey {
	case "chest_cm":
		return entry.ChestMin, entry.ChestMax
	case "waist_cm":
		return entry.WaistMin, entry.WaistMax
	case "hip_cm":
		return entry.HipMin, entry.HipMax
	case "height_cm":
		return entry.HeightMin, entry.HeightMax
	case "weight_kg":
		return entry.WeightMin, entry.WeightMax
	default:
		return 0, 200
	}
}

// dimFitScore scores how well a value fits within [lo, hi].
// Uses soft-boundary: 1.0 inside, linear ramp to 0 at 15% margin.
func dimFitScore(value, lo, hi float64) float64 {
	if lo >= hi {
		return 0
	}
	margin := (hi - lo) * 0.15
	softLo := lo - margin
	softHi := hi + margin

	if lo <= value && value <= hi {
		return 1.0
	}
	if softLo <= value && value < lo {
		return math.Max(0.0, (value-softLo)/(lo-softLo))
	}
	if hi < value && value <= softHi {
		return math.Max(0.0, (softHi-value)/(softHi-hi))
	}
	return 0
}

func roundTo(v float64, decimals int) float64 {
	pow := 1.0
	for i := 0; i < decimals; i++ {
		pow *= 10
	}
	return float64(int(v*pow+0.5)) / pow
}
