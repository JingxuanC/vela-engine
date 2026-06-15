// Package chat provides AI-powered product Q&A using Qwen LLM.
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/JingxuanC/vela-engine/internal/service"
)

// AnswerResult holds the AI response to a product question.
type AnswerResult struct {
	Answer          string   `json:"answer"`
	Sources         []string `json:"sources"`
	RelatedProducts []string `json:"related_products"`
}

// SuggestionResult holds suggested FAQ questions.
type SuggestionResult struct {
	Questions []string `json:"questions"`
}

const chatSystemPrompt = `You are a helpful e-commerce product assistant. Answer customer questions about products based on the provided product information.

## Guidelines
- Answer clearly and concisely using only the product context provided.
- If the answer is not available in the provided context, politely say so.
- Be friendly and professional.
- Always respond in the same language as the user's question.
- Return your answer as a JSON object with keys: "answer" (str), "sources" (list of str), "related_products" (list of str).`

// AnswerQuestion answers a product-related question using RAG-style prompting.
func AnswerQuestion(ctx context.Context, client service.LLMProvider, productID string, question string, history []map[string]string, productContext map[string]interface{}) (AnswerResult, error) {
	slog.Info("chat answer", "product_id", productID, "question_len", len(question))

	// Build product context
	ctxBlock := buildContextBlock(productID, productContext)

	// Build messages
	messages := []service.ChatMessage{
		{Role: "system", Content: chatSystemPrompt},
	}

	// Inject conversation history
	for _, h := range history {
		role := h["role"]
		if role == "" {
			role = "user"
		}
		messages = append(messages, service.ChatMessage{Role: role, Content: h["content"]})
	}

	// Final user message with context
	messages = append(messages, service.ChatMessage{
		Role: "user",
		Content: fmt.Sprintf(
			"Here is the product information you can reference:\n%s\n\nThe user asks: %s\n\nReturn a JSON object with keys: answer, sources, related_products.",
			ctxBlock, question,
		),
	})

	req := &service.ChatCompletionRequest{
		Messages:    messages,
		Temperature: 0.7,
		MaxTokens:   1024,
	}

	raw, err := client.ChatCompletion(ctx, req)
	if err != nil {
		slog.Warn("llm", "module", "product_qa", "status", "error", "error", err)
		slog.Warn("chat: LLM call failed, using fallback", "error", err)
		return AnswerResult{
			Answer: "I'm unable to answer your question right now. Our AI service is temporarily unavailable. Please try again in a moment.",
		}, nil
	}
	slog.Info("llm", "module", "product_qa", "model", req.Model, "status", "ok")

	var result AnswerResult
	if err := json.Unmarshal(service.ExtractJSONHelper(raw), &result); err != nil {
		return AnswerResult{Answer: raw}, nil
	}

	return result, nil
}

// SuggestQuestions generates suggested FAQ questions for a product.
func SuggestQuestions(ctx context.Context, client service.LLMProvider, product map[string]interface{}, count int) (SuggestionResult, error) {
	if count <= 0 {
		count = 5
	}

	productInfo := "General e-commerce products"
	if product != nil {
		if b, err := json.Marshal(product); err == nil {
			productInfo = string(b)
		}
	}

	messages := []service.ChatMessage{
		{Role: "system", Content: "You are an e-commerce FAQ specialist. Return a JSON object with key 'questions' containing an array of strings."},
		{Role: "user", Content: fmt.Sprintf("Based on this product:\n%s\n\nSuggest %d FAQ questions.", productInfo, count)},
	}

	req := &service.ChatCompletionRequest{
		Messages:    messages,
		Temperature: 0.6,
		MaxTokens:   512,
	}

	raw, err := client.ChatCompletion(ctx, req)
	if err != nil {
		return SuggestionResult{
			Questions: []string{
				"What materials is this product made from?",
				"What sizes are available?",
				"How do I care for this product?",
				"What is the return policy?",
				"How long does shipping take?",
			},
		}, nil
	}

	var result SuggestionResult
	parsed := service.ExtractJSONHelper(raw)
	if err := json.Unmarshal(parsed, &result); err != nil || len(result.Questions) == 0 {
		return SuggestionResult{Questions: []string{"What are the key features of this product?"}}, nil
	}

	if len(result.Questions) > count {
		result.Questions = result.Questions[:count]
	}

	return result, nil
}

func buildContextBlock(productID string, ctx map[string]interface{}) string {
	if ctx == nil {
		ctx = map[string]interface{}{}
	}
	name := stringVal(ctx, "name", "Unknown")
	category := stringVal(ctx, "category", "")
	description := stringVal(ctx, "description", "")
	price := stringVal(ctx, "price", "")

	specs, _ := json.Marshal(ctx["specs"])
	features := strSlice(ctx, "features")
	tags := strSlice(ctx, "tags")

	return fmt.Sprintf(
		"Product ID: %s\nName: %s\nCategory: %s\nDescription: %s\nSpecifications: %s\nFeatures: %s\nPrice: %s\nTags: %s",
		productID, name, category, description, string(specs),
		joinStr(features, ", "), price, joinStr(tags, ", "),
	) + buildExtraContext(ctx)
}

// buildExtraContext appends additional context blocks (RAG, fulfillment, recent updates, etc.) to the prompt.
func buildExtraContext(ctx map[string]interface{}) string {
	var sb strings.Builder

	if ragCtx, ok := ctx["rag_context"]; ok {
		if s, ok := ragCtx.(string); ok && s != "" {
			sb.WriteString("\n\n--- Knowledge Base ---\n")
			sb.WriteString(s)
		}
	}

	if fulfillmentCtx, ok := ctx["fulfillment_context"]; ok {
		if fc, ok := fulfillmentCtx.(map[string]interface{}); ok {
			sb.WriteString("\n\n--- Order Tracking ---\n")
			for k, v := range fc {
				sb.WriteString(fmt.Sprintf("%s: %v\n", k, v))
			}
		}
	}

	// Recent fulfillment updates: background context for the AI assistant
	// to proactively mention order shipping status
	if recentUpdates, ok := ctx["recent_fulfillment_updates"]; ok {
		if s, ok := recentUpdates.(string); ok && s != "" {
			sb.WriteString("\n\n--- Recent Fulfillment Updates ---\n")
			sb.WriteString(s)
			sb.WriteString("\n(You may proactively mention these updates if relevant to the conversation.)")
		}
	}

	return sb.String()
}
func stringVal(m map[string]interface{}, key, def string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

func strSlice(m map[string]interface{}, key string) []string {
	if v, ok := m[key]; ok {
		switch arr := v.(type) {
		case []string:
			return arr
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

func joinStr(items []string, sep string) string {
	result := ""
	for i, item := range items {
		if i > 0 {
			result += sep
		}
		result += item
	}
	return result
}
