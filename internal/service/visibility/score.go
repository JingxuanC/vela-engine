package visibility

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// ── Score Calculator ─────────────────────────────────────────────────────────

// CalculateOverall computes the weighted overall score from three dimension scores.
func CalculateOverall(ucp, geo, seo float64) float64 {
	raw := ucp*WeightUCP + geo*WeightGEO + seo*WeightSEO
	return math.Round(raw*10) / 10 // round to 1 decimal
}

// CalculateSubScore computes a dimension sub-score as the weighted average
// of its individual checks.
func CalculateSubScore(checks []CheckItem) SubScore {
	if len(checks) == 0 {
		return SubScore{Score: 0, Checks: checks}
	}

	var totalWeight float64
	var weightedSum float64
	for _, c := range checks {
		weightedSum += c.Score * c.Weight
		totalWeight += c.Weight
	}

	// Normalize if weights don't sum to 1.0
	if totalWeight > 0 {
		weightedSum = weightedSum / totalWeight
	}

	return SubScore{
		Score:  math.Round(weightedSum*10) / 10,
		Checks: checks,
	}
}

// CollectIssues extracts Issues from all three dimension SubScores.
func CollectIssues(ucp, geo, seo SubScore, productID string) []Issue {
	var issues []Issue

	collectFrom := func(dim string, sub SubScore) {
		for _, c := range sub.Checks {
			if c.Passed {
				continue
			}
			severity := SeverityWarning
			if c.Weight >= 0.15 {
				severity = SeverityCritical
			} else if c.Weight <= 0.05 {
				severity = SeverityInfo
			}
			issues = append(issues, Issue{
				Severity:  severity,
				Dimension: dim,
				CheckName: c.Name,
				ProductID: productID,
				Message:   c.Message,
				AutoFix:   c.AutoFix,
				FixAction: fixActionForCheck(dim, c.Name),
			})
		}
	}

	collectFrom(DimUCP, ucp)
	collectFrom(DimGEO, geo)
	collectFrom(DimSEO, seo)

	// Sort: critical first, then warning, then info
	sort.Slice(issues, func(i, j int) bool {
		order := map[string]int{SeverityCritical: 0, SeverityWarning: 1, SeverityInfo: 2}
		return order[issues[i].Severity] < order[issues[j].Severity]
	})

	return issues
}

// fixActionForCheck returns a fix action identifier for a given check.
func fixActionForCheck(dimension, checkName string) string {
	return fmt.Sprintf("%s:%s", dimension, checkName)
}

// NewPassCheck creates a passing CheckItem helper.
func NewPassCheck(name string, weight float64, message string, autoFix bool) CheckItem {
	return CheckItem{
		Name:    name,
		Weight:  weight,
		Score:   100,
		Passed:  true,
		Message: message,
		AutoFix: autoFix,
	}
}

// NewFailCheck creates a failing CheckItem helper.
func NewFailCheck(name string, weight float64, score float64, message string, autoFix bool) CheckItem {
	return CheckItem{
		Name:    name,
		Weight:  weight,
		Score:   score,
		Passed:  false,
		Message: message,
		AutoFix: autoFix,
	}
}

// BuildScore assembles a full VisibilityScore from three dimension results.
func BuildScore(ucp, geo, seo SubScore, productID string) VisibilityScore {
	overall := CalculateOverall(ucp.Score, geo.Score, seo.Score)

	// Set weights on sub-scores for serialization
	ucp.Weight = WeightUCP
	geo.Weight = WeightGEO
	seo.Weight = WeightSEO

	return VisibilityScore{
		Overall:   overall,
		UCP:       ucp,
		GEO:       geo,
		SEO:       seo,
		Grade:     Grade(overall),
		Issues:    CollectIssues(ucp, geo, seo, productID),
		ScannedAt: time.Now().UTC().Format(time.RFC3339),
	}
}
