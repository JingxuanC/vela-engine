// Package review provides AI-powered review summarization and fake review detection.
package review

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/JingxuanC/vela-engine/internal/service"
)

// SummarizeResult holds review summarization output.
type SummarizeResult struct {
	Summary           string   `json:"summary"`
	SentimentPositive float64  `json:"sentiment_positive"`
	SentimentNegative float64  `json:"sentiment_negative"`
	SentimentNeutral  float64  `json:"sentiment_neutral"`
	TopPros           []string `json:"top_pros"`
	TopCons           []string `json:"top_cons"`
	KeyThemes         []string `json:"key_themes"`
}

// FakeDetectionResult holds fake review detection output.
type FakeDetectionResult struct {
	FakeIDs       []string `json:"fake_ids"`
	SuspiciousIDs []string `json:"suspicious_ids"`
	Analysis      string   `json:"analysis"`
}

// ReviewInput represents a single customer review for analysis.
type ReviewInput struct {
	ID     string  `json:"id"`
	Author string  `json:"author"`
	Rating float64 `json:"rating"`
	Title  string  `json:"title"`
	Body   string  `json:"body"`
	Date   string  `json:"date"`
}

// SummarizeReviews aggregates and summarizes customer reviews.
func SummarizeReviews(ctx context.Context, client service.LLMProvider, productID string, reviews []ReviewInput) (SummarizeResult, error) {
	slog.Info("review summarize", "product_id", productID, "count", len(reviews))

	if len(reviews) == 0 {
		return SummarizeResult{
			Summary:           "No reviews available for this product.",
			SentimentPositive: 0,
			SentimentNegative: 0,
			SentimentNeutral:  1.0,
		}, nil
	}

	// Compute basic sentiment distribution
	var pos, neg, neu int
	for _, r := range reviews {
		if r.Rating >= 4 {
			pos++
		} else if r.Rating <= 2 {
			neg++
		} else {
			neu++
		}
	}
	total := float64(len(reviews))

	reviewData, _ := json.Marshal(reviews)
	messages := []service.ChatMessage{
		{Role: "system", Content: `You are a review analyst. Based on the provided reviews, return a JSON object with:
- "summary": concise 2-3 sentence overview
- "top_pros": list of 3-5 positive highlights
- "top_cons": list of 3-5 negative points
- "key_themes": list of 3-5 recurring themes`},
		{Role: "user", Content: fmt.Sprintf("Product ID: %s\nReviews:\n%s\n\nProvide the analysis JSON.", productID, string(reviewData))},
	}

	req := &service.ChatCompletionRequest{
		Messages:    messages,
		Temperature: 0.4,
		MaxTokens:   1024,
	}

	raw, err := client.ChatCompletion(ctx, req)
	if err != nil {
		slog.Warn("review: LLM call failed", "error", err)
		return SummarizeResult{
			Summary:           fmt.Sprintf("Based on %d reviews, this product has a generally positive reception.", len(reviews)),
			SentimentPositive: roundTo(float64(pos)/total*100, 1),
			SentimentNegative: roundTo(float64(neg)/total*100, 1),
			SentimentNeutral:  roundTo(float64(neu)/total*100, 1),
		}, nil
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(service.ExtractJSONHelper(raw), &parsed); err != nil {
		slog.Warn("review: failed to parse LLM JSON in summarize", "error", err, "raw_prefix", safePrefix(raw, 200))
		return SummarizeResult{
			Summary:           fmt.Sprintf("Based on %d reviews, this product has a generally positive reception.", len(reviews)),
			SentimentPositive: roundTo(float64(pos)/total*100, 1),
			SentimentNegative: roundTo(float64(neg)/total*100, 1),
			SentimentNeutral:  roundTo(float64(neu)/total*100, 1),
		}, nil
	}

	return SummarizeResult{
		Summary:           stringVal(parsed, "summary", ""),
		TopPros:           strSliceVal(parsed, "top_pros"),
		TopCons:           strSliceVal(parsed, "top_cons"),
		KeyThemes:         strSliceVal(parsed, "key_themes"),
		SentimentPositive: roundTo(float64(pos)/total*100, 1),
		SentimentNegative: roundTo(float64(neg)/total*100, 1),
		SentimentNeutral:  roundTo(float64(neu)/total*100, 1),
	}, nil
}

// DetectFakeReviews identifies potentially fake or fraudulent reviews.
func DetectFakeReviews(ctx context.Context, client service.LLMProvider, productID string, reviews []ReviewInput, threshold float64) (FakeDetectionResult, error) {
	slog.Info("fake review detection", "product_id", productID, "count", len(reviews))

	if len(reviews) == 0 {
		return FakeDetectionResult{Analysis: "No reviews to analyze."}, nil
	}

	reviewData, _ := json.Marshal(reviews)
	messages := []service.ChatMessage{
		{Role: "system", Content: `Detect potentially fake reviews. Return JSON:
- "fake_ids": list of highly likely fake review IDs
- "suspicious_ids": list of possibly fake review IDs
- "analysis": brief explanation of detection reasoning`},
		{Role: "user", Content: fmt.Sprintf(
			"Product ID: %s\nConfidence threshold: %.0f%%\nReviews:\n%s\n\nReturn the detection JSON.",
			productID, threshold*100, string(reviewData),
		)},
	}

	req := &service.ChatCompletionRequest{
		Messages:    messages,
		Temperature: 0.3,
		MaxTokens:   1024,
	}

	raw, err := client.ChatCompletion(ctx, req)
	if err != nil {
		slog.Warn("review: fake detection LLM call failed", "error", err)
		return FakeDetectionResult{Analysis: "Detection unavailable due to processing error."}, nil
	}

	var result FakeDetectionResult
	if err := json.Unmarshal(service.ExtractJSONHelper(raw), &result); err != nil {
		slog.Warn("review: failed to parse LLM JSON in fake detection", "error", err, "raw_prefix", safePrefix(raw, 200))
		return FakeDetectionResult{
			FakeIDs:       []string{},
			SuspiciousIDs: []string{},
			Analysis:      "Detection analysis unavailable due to processing error.",
		}, nil
	}
	return result, nil
}

func roundTo(v float64, decimals int) float64 {
	pow := 1.0
	for i := 0; i < decimals; i++ {
		pow *= 10
	}
	return float64(int(v*pow+0.5)) / pow
}

func safePrefix(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func stringVal(m map[string]interface{}, key, def string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

func strSliceVal(m map[string]interface{}, key string) []string {
	if v, ok := m[key]; ok {
		switch arr := v.(type) {
		case []interface{}:
			var result []string
			for _, item := range arr {
				if s, ok := item.(string); ok {
					result = append(result, s)
				}
			}
			return result
		}
	}
	return nil
}
