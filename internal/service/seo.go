package service

import (
	"context"
	"encoding/json"
	"fmt"
)

// SEOResult holds SEO analysis results.
type SEOResult struct {
	Success     bool     `json:"success"`
	Score       int      `json:"score"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Keywords    []string `json:"keywords"`
	Suggestions []string `json:"suggestions"`
	JSONLD      string   `json:"json_ld"`
	Error       string   `json:"error,omitempty"`
}

// GenerateSEO analyzes a product page and generates SEO recommendations.
func GenerateSEO(ctx context.Context, client LLMProvider, url, productName, description string) (*SEOResult, error) {
	messages := []ChatMessage{
		{Role: "system", Content: `You are an SEO expert. Analyze the product and return JSON with:
- "score": integer 0-100 SEO quality score
- "title": optimized SEO title (max 60 chars)
- "description": optimized meta description (max 160 chars)
- "keywords": array of 5-10 target keywords
- "suggestions": array of 3-5 improvement tips
- "json_ld": valid JSON-LD Product schema`},
		{Role: "user", Content: fmt.Sprintf(
			"URL: %s\nProduct Name: %s\nCurrent Description: %s\n\nProvide the SEO analysis JSON.",
			url, productName, description,
		)},
	}

	req := &ChatCompletionRequest{
		Messages:    messages,
		Temperature: 0.3,
		MaxTokens:   1024,
	}

	raw, err := client.ChatCompletion(ctx, req)
	if err != nil {
		return &SEOResult{
			Success:     false,
			Score:       50,
			Title:       productName,
			Description: description,
			Error:       err.Error(),
		}, nil
	}

	var result SEOResult
	if err := json.Unmarshal(ExtractJSONHelper(raw), &result); err != nil {
		result.Success = false
		result.Error = "Failed to parse SEO response"
	} else {
		result.Success = true
	}
	return &result, nil
}
