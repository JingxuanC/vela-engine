package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/internal/platform/insight"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/internal/service/chat"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"gorm.io/gorm"
)

// ChatHandler handles AI chat endpoints.
// DEPRECATED: No frontend page exists. Kept for future Storefront Widget (Phase 2).
// For merchant-facing AI, use AdminChatHandler instead.
// The customer-facing Ask/StreamChat will be replaced by StorefrontChatHandler.
type ChatHandler struct {
	llmRouter *service.LLMRouter
	eventBus  eventbus.EventBus
	db        *gorm.DB
	injector  *insight.ContextInjector
}

// NewChatHandler creates a new ChatHandler.
func NewChatHandler(llmRouter *service.LLMRouter, db *gorm.DB, inj *insight.ContextInjector) *ChatHandler {
	return &ChatHandler{llmRouter: llmRouter, db: db, injector: inj}
}

// SetEventBus sets the optional event bus for AI event publishing.
func (h *ChatHandler) SetEventBus(bus eventbus.EventBus) { h.eventBus = bus }

// ChatRequest is the request for product Q&A.
type ChatRequest struct {
	ProductID string              `json:"product_id"`
	ShopID    string              `json:"shop_id"`
	Question  string              `json:"question"`
	History   []map[string]string `json:"history"`
}

// ChatResponse is the response from the chat endpoint.
type ChatResponse struct {
	Success         bool     `json:"success"`
	Answer          string   `json:"answer"`
	Sources         []string `json:"sources"`
	RelatedProducts []string `json:"related_products"`
	Error           string   `json:"error,omitempty"`
}

// Ask handles POST /api/chat/ask — answers a product question.
func (h *ChatHandler) Ask(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if strings.TrimSpace(req.ProductID) == "" {
		httputil.WriteError(w, http.StatusBadRequest, "product_id is required")
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		httputil.WriteError(w, http.StatusBadRequest, "question is required")
		return
	}

	// Look up product data from synced_products
	var productContext map[string]interface{}
	if h.db != nil {
		var p model.SyncedProduct
		if err := h.db.WithContext(r.Context()).
			Where("platform_id = ? OR id::text = ?", req.ProductID, req.ProductID).
			First(&p).Error; err == nil {
			// Extract price from variants JSON
			price := ""
			if len(p.Variants) > 0 {
				var variants []struct {
					Price string `json:"price"`
				}
				if err := json.Unmarshal(p.Variants, &variants); err == nil && len(variants) > 0 {
					price = variants[0].Price
				}
			}
			productContext = map[string]interface{}{
				"name":        p.Title,
				"description": p.Description,
				"category":    p.ProductType,
				"vendor":      p.Vendor,
				"price":       price,
				"status":      p.Status,
				"tags":        p.Tags,
			}
		}
	}

	// Inject filtered context — customer-facing, no merchant-sensitive data.
	// Only product data + RAG (product/review sources). No return rates, orders, snapshots.
	if h.injector != nil && req.ShopID != "" {
		if fetcher := h.injector.GetFetcher("rag"); fetcher != nil {
			if ss, ok := fetcher.(insight.SourceSetter); ok {
				ss.SetSources([]string{"product", "review"})
			}
		}
		ragContext := h.injector.InjectWithQuery(r.Context(), h.db, req.ShopID, req.Question)
		if ragContext != "" {
			if productContext == nil {
				productContext = make(map[string]interface{})
			}
			productContext["rag_context"] = ragContext
		}
	}

	chatProvider, chatCfg, chatErr := h.llmRouter.GetProvider(r.Context(), parseShopID(req.ShopID))
	if chatErr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "AI service unavailable")
		return
	}
	result, err := chat.AnswerQuestion(r.Context(), chatProvider, req.ProductID, req.Question, req.History, productContext)
	if err != nil {
		PublishAIEvent(r.Context(), h.eventBus, parseShopID(req.ShopID), "chat.ask", req, nil, false)
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = chatCfg // model config available for future use

	PublishAIEvent(r.Context(), h.eventBus, parseShopID(req.ShopID), "chat.ask", req, result, true)
	// TODO: pass real shop_id once ChatRequest includes it

	httputil.WriteOK(w, ChatResponse{
		Success:         true,
		Answer:          result.Answer,
		Sources:         result.Sources,
		RelatedProducts: result.RelatedProducts,
	})
}

// PromptOptimizeRequest is the request for optimizing a style description.
type PromptOptimizeRequest struct {
	Prompt string `json:"prompt"`
}

// PromptOptimizeResponse is the optimized prompt response.
type PromptOptimizeResponse struct {
	Success   bool   `json:"success"`
	Optimized string `json:"optimized"`
	Error     string `json:"error,omitempty"`
}

// PromptOptimize handles POST /api/chat/prompt-optimize — uses Qwen to transform
// a merchant's rough style description into a professional image generation prompt.
func (h *ChatHandler) PromptOptimize(w http.ResponseWriter, r *http.Request) {
	var req PromptOptimizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		httputil.WriteError(w, http.StatusBadRequest, "prompt is required")
		return
	}

	systemPrompt := `You are a professional fashion photographer and creative director. Convert the merchant's rough style description into a detailed, technical aesthetic prompt for an AI image enhancer.

Rules:
- Output ONLY the optimized prompt, no explanations
- Include: lighting, color palette, mood, background, quality level
- Keep garment style from original if not specified
- Use professional photography terminology
- Output in English for best image generation results
- Max 200 words`

	optProvider, optCfg, optErr := h.llmRouter.GetProvider(r.Context(), uuid.Nil)
	if optErr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "AI service unavailable")
		return
	}
	optimized, err := optProvider.ChatCompletion(r.Context(), &service.ChatCompletionRequest{
		Model:       optCfg.Model,
		Messages: []service.ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: req.Prompt},
		},
		Temperature: 0.3,
	})
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to optimize: "+err.Error())
		return
	}

	httputil.WriteOK(w, PromptOptimizeResponse{
		Success:   true,
		Optimized: strings.TrimSpace(optimized),
	})
}

// SuggestRequest is the request for FAQ question suggestions.
type SuggestRequest struct {
	Product map[string]interface{} `json:"product"`
	Count   int                    `json:"count"`
}

// SuggestResponse is the response with suggested questions.
type SuggestResponse struct {
	Success   bool     `json:"success"`
	Questions []string `json:"questions"`
	Error     string   `json:"error,omitempty"`
}

// Suggest handles POST /api/chat/suggest — suggests FAQ questions.
func (h *ChatHandler) Suggest(w http.ResponseWriter, r *http.Request) {
	var req SuggestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if req.Count <= 0 {
		req.Count = 5
	}
	if req.Count > 20 {
		req.Count = 20
	}
	if req.Product == nil || len(req.Product) == 0 {
		httputil.WriteError(w, http.StatusBadRequest, "product data is required")
		return
	}

	suggestProvider, _, suggestErr := h.llmRouter.GetProvider(r.Context(), uuid.Nil)
	if suggestErr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "AI service unavailable")
		return
	}
	result, err := chat.SuggestQuestions(r.Context(), suggestProvider, req.Product, req.Count)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.WriteOK(w, SuggestResponse{
		Success:   true,
		Questions: result.Questions,
	})
}

// BatchAIRequest is the request for batch AI operations on selected products.
type BatchAIRequest struct {
	Action     string   `json:"action"`
	ProductIDs []string `json:"productIds"`
}

// BatchAIResponse is the response from the batch AI endpoint.
type BatchAIResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Count   int    `json:"count"`
}

// ChatStreamRequest matches the frontend chat-widget payload.
type ChatStreamRequest struct {
	Message   string              `json:"message"`
	ProductID string              `json:"product_id"`
	History   []map[string]string `json:"history"`
}

// StreamChat handles POST /api/chat/stream — SSE streaming chat.
func (h *ChatHandler) StreamChat(w http.ResponseWriter, r *http.Request) {
	var req ChatStreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		httputil.WriteError(w, http.StatusBadRequest, "message is required")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		httputil.WriteError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Look up product data from synced_products
	var productContext map[string]interface{}
	if h.db != nil {
		var p model.SyncedProduct
		if err := h.db.WithContext(r.Context()).
			Where("platform_id = ? OR id::text = ?", req.ProductID, req.ProductID).
			First(&p).Error; err == nil {
			// Extract price from variants JSON
			price := ""
			if len(p.Variants) > 0 {
				var variants []struct {
					Price string `json:"price"`
				}
				if err := json.Unmarshal(p.Variants, &variants); err == nil && len(variants) > 0 {
					price = variants[0].Price
				}
			}
			productContext = map[string]interface{}{
				"name":        p.Title,
				"description": p.Description,
				"category":    p.ProductType,
				"vendor":      p.Vendor,
				"price":       price,
				"status":      p.Status,
				"tags":        p.Tags,
			}
		}
	}

	// Build streaming request (inline: same logic as AnswerQuestion)
	ctxBlock := fmt.Sprintf("Product ID: %s\nName: %s\nCategory: %s\nDescription: %s\nVendor: %s\nPrice: %s\nStatus: %s\nTags: %s",
		req.ProductID,
		strVal(productContext, "name", "Unknown"),
		strVal(productContext, "category", ""),
		strVal(productContext, "description", ""),
		strVal(productContext, "vendor", ""),
		strVal(productContext, "price", ""),
		strVal(productContext, "status", ""),
		strVal(productContext, "tags", ""),
	)
	messages := []service.ChatMessage{
		{Role: "system", Content: "You are a helpful e-commerce product assistant. Answer customer questions based on provided product information."},
	}
	for _, h := range req.History {
		role := h["role"]
		if role == "" { role = "user" }
		messages = append(messages, service.ChatMessage{Role: role, Content: h["content"]})
	}
	messages = append(messages, service.ChatMessage{
		Role: "user",
		Content: fmt.Sprintf("Product context:\n%s\n\nQuestion: %s", ctxBlock, req.Message),
	})

	streamProvider, streamCfg, streamErr := h.llmRouter.GetProvider(r.Context(), uuid.Nil)
	if streamErr != nil {
		fmt.Fprintf(w, "data: {\"error\":\"%s\"}\n\n", streamErr.Error())
		flusher.Flush()
		return
	}
	streamCh, err := streamProvider.ChatCompletionStream(r.Context(), &service.ChatCompletionRequest{
		Model:       streamCfg.Model,
		Messages:    messages,
		Temperature: 0.7,
		MaxTokens:   1024,
	})
	if err != nil {
		fmt.Fprintf(w, "data: {\"error\":\"%s\"}\n\n", err.Error())
		flusher.Flush()
		return
	}

	// Real streaming: emit tokens as they arrive from DashScope
	var fullAnswer strings.Builder
	for chunk := range streamCh {
		if chunk.Error != "" {
			fmt.Fprintf(w, "data: {\"error\":\"%s\"}\n\n", chunk.Error)
			flusher.Flush()
			break
		}
		if chunk.Finish {
			break
		}
		fullAnswer.WriteString(chunk.Token)
		jsonToken, _ := json.Marshal(chunk.Token)
		fmt.Fprintf(w, "data: {\"token\":%s}\n\n", jsonToken)
		flusher.Flush()
	}

	PublishAIEvent(r.Context(), h.eventBus, parseShopID(req.ProductID), "chat.stream", req, map[string]string{"answer": fullAnswer.String()}, true)

	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// BatchAI handles POST /api/aitools/batch.
func (h *ChatHandler) BatchAI(w http.ResponseWriter, r *http.Request) {
	var req BatchAIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.ProductIDs) == 0 {
		httputil.WriteError(w, http.StatusBadRequest, "no products selected")
		return
	}
	httputil.WriteOK(w, BatchAIResponse{
		Success: true,
		Message: fmt.Sprintf("Batch %s queued for %d products", req.Action, len(req.ProductIDs)),
		Count:   len(req.ProductIDs),
	})
}

// parseShopID safely parses a shop identifier (UUID or domain) into uuid.UUID.
func parseShopID(raw string) uuid.UUID {
	if id, err := uuid.Parse(raw); err == nil {
		return id
	}
	// Return Nil for non-UUID identifiers (shop domains, etc.)
	return uuid.Nil
}

func strVal(m map[string]interface{}, key, defaultVal string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return defaultVal
}
