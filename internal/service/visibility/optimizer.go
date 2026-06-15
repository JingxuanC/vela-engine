package visibility

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
)

// ── GEO Content Optimizer ────────────────────────────────────────────────────

// Optimizer generates AI-optimized product content for GEO visibility.
type Optimizer struct {
	llmRouter *service.LLMRouter
}

// NewOptimizer creates a new Optimizer.
func NewOptimizer(llmRouter *service.LLMRouter) *Optimizer {
	return &Optimizer{llmRouter: llmRouter}
}

// ── Optimization Results ─────────────────────────────────────────────────────

// OptimizedContent holds AI-optimized product content.
type OptimizedContent struct {
	Title           string `json:"title"`
	Description     string `json:"description"`
	SEOTitle        string `json:"seo_title"`        // max 60 chars
	SEODescription  string `json:"seo_description"`  // max 160 chars
	Keywords        []string `json:"keywords"`
}

// FAQResult holds AI-generated FAQ content for a product.
type FAQResult struct {
	ProductID string    `json:"product_id"`
	Questions []QAItem  `json:"questions"`
}

// QAItem is a single Q&A pair.
type QAItem struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// ── Optimization Methods ─────────────────────────────────────────────────────

const geoOptimizePrompt = `You are an AI e-commerce content optimization expert. Your job is to make product data more understandable to AI agents (ChatGPT, Perplexity, Google AI Overviews).

Rules:
1. Use factual, direct language — avoid marketing hype, emojis, and exaggerated claims
2. Include all key attributes: brand, material, size, color, use case
3. SEO title: max 60 characters
4. SEO description: max 160 characters
5. Description: 150-300 characters with structured key information
6. Keywords: 5-8 relevant search terms
7. Output valid JSON only, no markdown or extra text

Output format:
{
  "title": "optimized product title",
  "description": "optimized description",
  "seo_title": "SEO title (≤60 chars)",
  "seo_description": "SEO meta description (≤160 chars)",
  "keywords": ["keyword1", "keyword2", ...]
}`

// OptimizeDescription generates AI-optimized product content.
func (o *Optimizer) OptimizeDescription(ctx context.Context, p *model.SyncedProduct) (*OptimizedContent, error) {
	if o.llmRouter == nil {
		return nil, fmt.Errorf("llm router not available")
	}

	provider, cfg, err := o.llmRouter.GetProvider(ctx, p.ShopID)
	if err != nil {
		return nil, fmt.Errorf("get provider: %w", err)
	}

	userPrompt := fmt.Sprintf(
		"Product Name: %s\nCurrent Description: %s\nVendor: %s\nType: %s\nTags: %s\n\nGenerate optimized GEO content for this product.",
		p.Title,
		truncateForPrompt(p.Description, 500),
		p.Vendor,
		p.ProductType,
		p.Tags,
	)

	req := &service.ChatCompletionRequest{
		Model: cfg.Model,
		Messages: []service.ChatMessage{
			{Role: "system", Content: geoOptimizePrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.3,
		MaxTokens:   800,
	}
	if req.Model == "" {
		req.Model = "qwen-turbo"
	}

	resp, err := provider.ChatCompletion(ctx, req)

	if err != nil {
		slog.Error("visibility: optimize description failed", "product", p.PlatformID, "error", err)
		return nil, fmt.Errorf("llm call failed: %w", err)
	}

	// Extract JSON from response (may be wrapped in markdown)
	jsonStr := extractJSON(resp)
	var result OptimizedContent
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		slog.Error("visibility: failed to parse optimization result", "product", p.PlatformID, "raw", resp, "error", err)
		return nil, fmt.Errorf("failed to parse AI response: %w", err)
	}

	return &result, nil
}

// GenerateFAQ generates AI-powered FAQ content based on product data.
func (o *Optimizer) GenerateFAQ(ctx context.Context, p *model.SyncedProduct, returnReasons []string) (*FAQResult, error) {
	if o.llmRouter == nil {
		return nil, fmt.Errorf("llm router not available")
	}

	provider, cfg, err := o.llmRouter.GetProvider(ctx, p.ShopID)
	if err != nil {
		return nil, fmt.Errorf("get provider: %w", err)
	}

	returnInfo := ""
	if len(returnReasons) > 0 {
		returnInfo = fmt.Sprintf("\nCommon return reasons: %s", strings.Join(returnReasons, ", "))
	}

	userPrompt := fmt.Sprintf(
		"Product: %s\nDescription: %s\nType: %s\nMaterial: %s%s\n\nGenerate 3-5 FAQ Q&A pairs that AI search engines can reference.",
		p.Title,
		truncateForPrompt(p.Description, 500),
		p.ProductType,
		p.Vendor,
		returnInfo,
	)

	sysPrompt := `You are an e-commerce FAQ generator. Create Q&A pairs that answer real customer questions with specific, factual information.

Rules:
1. Questions should be specific and long-tail (e.g., "Is this jacket suitable for rainy weather?")
2. Answers should be direct and factual — no marketing language
3. Include specific details: materials, measurements, use cases, care instructions
4. If return data is available, address common concerns
5. Output valid JSON only

Output format:
{"questions": [{"question": "...", "answer": "..."}, ...]}`

	req := &service.ChatCompletionRequest{
		Model: cfg.Model,
		Messages: []service.ChatMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.3,
		MaxTokens:   1000,
	}
	if req.Model == "" {
		req.Model = "qwen-turbo"
	}

	resp, err := provider.ChatCompletion(ctx, req)

	if err != nil {
		return nil, fmt.Errorf("llm FAQ generation failed: %w", err)
	}

	jsonStr := extractJSON(resp)
	var result FAQResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("failed to parse FAQ response: %w", err)
	}

	result.ProductID = p.PlatformID
	return &result, nil
}

// ── Batch Methods ────────────────────────────────────────────────────────────

// OptimizeBatch generates optimized content for multiple products.
// Runs up to 3 concurrent DashScope calls. Returns results for successful optimizations.
func (o *Optimizer) OptimizeBatch(ctx context.Context, products []model.SyncedProduct) ([]OptimizedContent, error) {
	results := make([]OptimizedContent, 0, len(products))
	var mu sync.Mutex
	sem := make(chan struct{}, 3) // concurrent limit
	var wg sync.WaitGroup

	for i := range products {
		wg.Add(1)
		go func(p *model.SyncedProduct) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result, err := o.OptimizeDescription(ctx, p)
			if err != nil {
				slog.Warn("visibility: batch optimize skipped", "product_id", p.PlatformID, "error", err)
				return
			}
			mu.Lock()
			results = append(results, *result)
			mu.Unlock()
		}(&products[i])
	}
	wg.Wait()
	return results, nil
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func truncateForPrompt(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// extractJSON extracts the first JSON object from a response that may contain markdown.
func extractJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	// Remove markdown code fences
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) > 2 {
			lines = lines[1 : len(lines)-1] // remove opening and closing ```
			raw = strings.Join(lines, "\n")
		}
	}
	return raw
}
