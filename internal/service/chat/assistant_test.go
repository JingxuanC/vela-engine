package chat

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAnswerQuestion_FallbackMessageIsHonest(t *testing.T) {
	// Verify system prompt contract
	assert.Contains(t, chatSystemPrompt, "e-commerce product assistant")
	assert.Contains(t, chatSystemPrompt, "Answer clearly")
	assert.Contains(t, chatSystemPrompt, "If the answer is not available")
	assert.NotContains(t, chatSystemPrompt, "high-quality")
}

func TestAnswerResult_Defaults(t *testing.T) {
	result := AnswerResult{
		Answer:  "test",
		Sources: []string{},
	}
	assert.Equal(t, "test", result.Answer)
	assert.Empty(t, result.Sources)
	assert.Empty(t, result.RelatedProducts)
}

func TestSuggestQuestions_DefaultFallback(t *testing.T) {
	// Default fallback when LLM is unavailable
	def := []string{
		"What materials is this product made from?",
		"What sizes are available?",
		"How do I care for this product?",
		"What is the return policy?",
		"How long does shipping take?",
	}
	assert.Len(t, def, 5)
}

func TestBuildContextBlock_NilContext(t *testing.T) {
	result := buildContextBlock("prod-123", nil)
	assert.Contains(t, result, "Product ID: prod-123")
	assert.Contains(t, result, "Name: Unknown") // default when no context
}
