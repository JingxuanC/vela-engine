package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// DescriptionResult holds generated product description.
type DescriptionResult struct {
	Success      bool     `json:"success"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	BulletPoints []string `json:"bullet_points"`
	SEOKeywords  []string `json:"seo_keywords"`
	Error        string   `json:"error,omitempty"`
}

// GenerateDescription creates an AI-generated product description.
func GenerateDescription(ctx context.Context, client LLMProvider, productName, category string, features []string, toneOfVoice string, targetKeywords []string, language string) (*DescriptionResult, error) {
	if toneOfVoice == "" {
		toneOfVoice = "professional"
	}
	if language == "" {
		language = "English"
	}

	messages := []ChatMessage{
		{Role: "system", Content: fmt.Sprintf(
			`You are an expert e-commerce copywriter. Write product descriptions in %s with a %s tone.
Return JSON with:
- "title": compelling product title
- "description": 2-3 paragraph product description
- "bullet_points": 4-6 key selling points
- "seo_keywords": 5-8 target SEO keywords`, language, toneOfVoice,
		)},
		{Role: "user", Content: fmt.Sprintf(
			"Product Name: %s\nCategory: %s\nFeatures: %s\nTarget Keywords: %s\n\nGenerate the product description JSON.",
			productName, category, strings.Join(features, ", "), strings.Join(targetKeywords, ", "),
		)},
	}

	req := &ChatCompletionRequest{
		Messages:    messages,
		Temperature: 0.7,
		MaxTokens:   1024,
	}

	raw, err := client.ChatCompletion(ctx, req)
	if err != nil {
		return &DescriptionResult{
			Success:      false,
			Title:        productName,
			Description:  productName + " - " + category,
			BulletPoints: features,
			SEOKeywords:  targetKeywords,
			Error:        err.Error(),
		}, nil
	}

	var result DescriptionResult
	if err := json.Unmarshal(ExtractJSONHelper(raw), &result); err != nil {
		result.Success = false
		result.Error = "Failed to parse description response"
	} else {
		result.Success = true
	}
	return &result, nil
}
