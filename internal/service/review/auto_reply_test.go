package review

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ── AnalyzeBadReview ────────────────────────────────────────────────────────────

func TestAnalyzeBadReview_SevereRating(t *testing.T) {
	input := ReviewInput{
		ID:     "1",
		Author: "Alice",
		Rating: 1.0,
		Title:  "Terrible quality",
		Body:   "The fabric fell apart after one wash. Very cheap material and poor stitching.",
	}

	result := AnalyzeBadReview(input)

	assert.Equal(t, "severe", result.Severity)
	assert.InDelta(t, 0.85, result.SeverityScore, 0.01)
	assert.Equal(t, "quality", result.ComplaintType)
	assert.True(t, result.IsActionable)
	assert.Equal(t, "refund", result.RecommendedAction)
	assert.Contains(t, result.SpecificIssues, "quality")
}

func TestAnalyzeBadReview_ModerateRating(t *testing.T) {
	input := ReviewInput{
		ID:     "2",
		Author: "Bob",
		Rating: 2.0,
		Title:  "Too small",
		Body:   "Ordered a Large but it fits like a Medium. The size guide is completely wrong.",
	}

	result := AnalyzeBadReview(input)

	assert.Equal(t, "moderate", result.Severity)
	assert.InDelta(t, 0.55, result.SeverityScore, 0.01)
	assert.Equal(t, "size", result.ComplaintType)
	assert.Contains(t, result.SpecificIssues, "size")
}

func TestAnalyzeBadReview_Shipping(t *testing.T) {
	input := ReviewInput{
		ID:     "3",
		Author: "Charlie",
		Rating: 2.0,
		Title:  "Late delivery",
		Body:   "Package arrived 2 weeks late and the box was damaged. Very disappointed with the shipping.",
	}

	result := AnalyzeBadReview(input)

	assert.Equal(t, "shipping", result.ComplaintType)
	assert.Contains(t, result.SpecificIssues, "shipping")
}

func TestAnalyzeBadReview_Service(t *testing.T) {
	input := ReviewInput{
		ID:     "4",
		Author: "Diana",
		Rating: 1.0,
		Title:  "Bad customer service",
		Body:   "Tried to get a refund but customer support was unhelpful. They refused to help me return the item.",
	}

	result := AnalyzeBadReview(input)

	assert.Equal(t, "service", result.ComplaintType)
	assert.Contains(t, result.SpecificIssues, "service")
}

func TestAnalyzeBadReview_OtherComplaint(t *testing.T) {
	input := ReviewInput{
		ID:     "5",
		Author: "Eve",
		Rating: 2.0,
		Title:  "Not what I expected",
		Body:   "The color is different from the photos. It's darker in real life.",
	}

	result := AnalyzeBadReview(input)

	assert.Equal(t, "other", result.ComplaintType)
	assert.Equal(t, "moderate", result.Severity)
}

func TestAnalyzeBadReview_NotActionable(t *testing.T) {
	input := ReviewInput{
		ID:     "6",
		Author: "Frank",
		Rating: 2.0,
		Title:  "Never buying again",
		Body:   "Just want a refund and never buying from this store again. Already returned the item.",
	}

	result := AnalyzeBadReview(input)

	assert.False(t, result.IsActionable)
}

func TestAnalyzeBadReview_MildRating(t *testing.T) {
	// Rating 3 with no specific complaint type → mild, and since isActionable=true → discount
	// "worst" keyword in body triggers extractIssues "general dissatisfaction" fallback
	input := ReviewInput{
		ID:     "7",
		Author: "Grace",
		Rating: 3.0,
		Title:  "Worst purchase ever",
		Body:   "This was the worst thing I have ever bought online. So disappointed and angry about the terrible experience.",
	}

	result := AnalyzeBadReview(input)

	assert.Equal(t, "mild", result.Severity)
	assert.InDelta(t, 0.3, result.SeverityScore, 0.01)
	assert.Equal(t, "other", result.ComplaintType)
}

// ── GenerateReplies ─────────────────────────────────────────────────────────────

func TestGenerateReplies_ReturnsThreeReplies(t *testing.T) {
	analysis := BadReviewAnalysis{
		ComplaintType:     "size",
		Severity:          "moderate",
		SeverityScore:     0.55,
		IsActionable:      true,
		SpecificIssues:    []string{"size", "fit"},
		RecommendedAction: "discount",
	}

	replies := GenerateReplies(analysis, "Summer Dress")

	assert.Len(t, replies, 3)
	for _, r := range replies {
		assert.NotEmpty(t, r)
		assert.Contains(t, r, "Summer Dress")
	}
}

func TestGenerateReplies_QualityComplaint(t *testing.T) {
	analysis := BadReviewAnalysis{
		ComplaintType:     "quality",
		Severity:          "severe",
		SeverityScore:     0.85,
		IsActionable:      true,
		SpecificIssues:    []string{"quality", "material"},
		RecommendedAction: "refund",
	}

	replies := GenerateReplies(analysis, "Cotton T-Shirt")

	assert.Len(t, replies, 3)
	for _, r := range replies {
		assert.NotEmpty(t, r)
	}
}

func TestGenerateReplies_UnknownComplaintType(t *testing.T) {
	analysis := BadReviewAnalysis{
		ComplaintType:     "unknown_type",
		Severity:          "mild",
		SeverityScore:     0.3,
		IsActionable:      false,
		SpecificIssues:    []string{"general dissatisfaction"},
		RecommendedAction: "apology_only",
	}

	replies := GenerateReplies(analysis, "Product")

	assert.Len(t, replies, 3)
	for _, r := range replies {
		assert.NotEmpty(t, r)
	}
}

// ── GenerateDiscount ────────────────────────────────────────────────────────────

func TestGenerateDiscount_Severe(t *testing.T) {
	result := GenerateDiscount("severe")

	assert.Equal(t, 50.0, result.Percent)
	assert.Equal(t, 90, result.ExpiresDays)
	assert.NotEmpty(t, result.Code)
	assert.Contains(t, result.Code, "VT")
	assert.Len(t, result.Code, 12) // "VT" + 10 chars
}

func TestGenerateDiscount_Moderate(t *testing.T) {
	result := GenerateDiscount("moderate")

	assert.Equal(t, 30.0, result.Percent)
	assert.Equal(t, 60, result.ExpiresDays)
	assert.NotEmpty(t, result.Code)
}

func TestGenerateDiscount_Mild(t *testing.T) {
	result := GenerateDiscount("mild")

	assert.Equal(t, 15.0, result.Percent)
	assert.Equal(t, 30, result.ExpiresDays)
	assert.NotEmpty(t, result.Code)
}

func TestGenerateDiscount_Unknown(t *testing.T) {
	result := GenerateDiscount("unknown")

	// Should default to mild
	assert.Equal(t, 15.0, result.Percent)
	assert.Equal(t, 30, result.ExpiresDays)
}

// ── Helpers ─────────────────────────────────────────────────────────────────────

func TestContainsAny(t *testing.T) {
	assert.True(t, containsAny("this product is too small for me", "too small", "doesn't fit"))
	assert.True(t, containsAny("cheap quality fabric", "quality", "material"))
	assert.False(t, containsAny("great product", "too small", "shipping"))
}

func TestExtractIssues(t *testing.T) {
	issues := extractIssues("size is wrong and fit is tight", []string{"size", "fit"})
	assert.Contains(t, issues, "size")
	assert.Contains(t, issues, "fit")

	// No matches should return default
	issues = extractIssues("great product", []string{"size", "quality"})
	assert.Contains(t, issues, "general dissatisfaction")
}

func TestGenerateCouponCode(t *testing.T) {
	code := generateCouponCode()
	assert.Len(t, code, 12)
	assert.Contains(t, code, "VT")
}

func TestCalculateRating(t *testing.T) {
	assert.InDelta(t, 1.0, calculateRating("severe"), 0.01)
	assert.InDelta(t, 2.0, calculateRating("moderate"), 0.01)
	assert.InDelta(t, 3.0, calculateRating("mild"), 0.01)
	// Unknown severity defaults to 3.0
	assert.InDelta(t, 3.0, calculateRating("unknown"), 0.01)
}

func TestEscJSON(t *testing.T) {
	result := escJSON(`hello "world"\n`)
	assert.Equal(t, `hello \"world\"\\n`, result)
}
