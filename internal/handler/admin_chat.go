// Package handler provides the AI Operations Assistant (Admin Chat).
// This is the merchant-facing AI that answers business questions using
// Vela's Data Platform: ShopSnapshot, Insight Engine, RAG, and ContextInjector.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/internal/platform/insight"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// ── Types ──────────────────────────────────────────────────────────────────────

// chatScene classifies the merchant's question into a data-fetching strategy.
type chatScene int

const (
	sceneQuickCheck      chatScene = iota // "how's business?" → snapshot only
	sceneRootCause                        // "why are returns up?" → snapshot + PG + RAG
	sceneDecisionSupport                  // "should I raise prices?" → full data fetch
)

// AdminChatHandler is the AI Operations Assistant.
type AdminChatHandler struct {
	db        *gorm.DB
	llmRouter *service.LLMRouter
	injector  *insight.ContextInjector
	eventBus  eventbus.EventBus
}

// NewAdminChatHandler creates a new AdminChatHandler.
func NewAdminChatHandler(db *gorm.DB, llmRouter *service.LLMRouter, injector *insight.ContextInjector) *AdminChatHandler {
	return &AdminChatHandler{db: db, llmRouter: llmRouter, injector: injector}
}

// SetEventBus sets the optional event bus for AI interaction event publishing.
func (h *AdminChatHandler) SetEventBus(bus eventbus.EventBus) { h.eventBus = bus }

// ── Request / Response DTOs ────────────────────────────────────────────────────

// AdminChatRequest is the merchant's question.
type AdminChatRequest struct {
	ShopID   string              `json:"shop_id"`
	Question string              `json:"question"`
	History  []map[string]string `json:"history"`
}

// AdminChatResponse is the AI's answer with optional data cards.
type AdminChatResponse struct {
	Success        bool              `json:"success"`
	Answer         string            `json:"answer"`
	DailySummary   *DailySummary     `json:"daily_summary,omitempty"`
	SuggestedQuestions []string      `json:"suggested_questions,omitempty"`
	Error          string            `json:"error,omitempty"`
}

// DailySummary is the pre-computed store snapshot shown at session start.
type DailySummary struct {
	ReturnRate    float64  `json:"return_rate"`
	OrderCount    int      `json:"order_count"`
	TopCategories []string `json:"top_categories"`
	Alerts        []string `json:"alerts"`
	GeneratedAt   string   `json:"generated_at"`
}

// ── Question Classification ────────────────────────────────────────────────────

// classifyQuestion routes a merchant question to the appropriate data-fetching scene.
// Uses keyword matching (no LLM call) for fast routing.
func classifyQuestion(question string) chatScene {
	q := strings.ToLower(question)

	// Scene 1: Quick check — "how's business", "any issues", "what's happening"
	quickPatterns := []string{
		"how's business", "hows business", "how is business",
		"what's happening", "what is happening", "any issues",
		"anything wrong", "how are things", "status update",
		"daily summary", "today", "yesterday", "this week",
		"overview", "summary", "快报", "今天怎么样", "有什么异常",
	}
	for _, p := range quickPatterns {
		if strings.Contains(q, p) {
			return sceneQuickCheck
		}
	}

	// Scene 3: Decision support — pricing, strategy, what-if
	decisionPatterns := []string{
		"should i", "what if", "raise price", "lower price",
		"pricing strategy", "competitor", "market price",
		"launch", "discontinue", "expand", "投资", "要不要",
	}
	for _, p := range decisionPatterns {
		if strings.Contains(q, p) {
			return sceneDecisionSupport
		}
	}

	// Default: Scene 2 — root cause analysis
	// "why returns up", "which product", "what size", "how to fix"
	return sceneRootCause
}

// ── Context Injection ──────────────────────────────────────────────────────────

// buildContext assembles the prompt context based on the question scene.
func (h *AdminChatHandler) buildContext(ctx context.Context, shopID, question string, scene chatScene) string {
	if h.injector == nil || h.db == nil {
		return ""
	}

	switch scene {
	case sceneQuickCheck:
		// Snapshot only — sufficient for "how's business"
		return h.injector.InjectOne(ctx, h.db, shopID, "shop_snapshot")

	case sceneRootCause:
		// InjectWithQuery covers ALL registered fetchers (snapshot + return + velocity + RAG)
		return h.injector.InjectWithQuery(ctx, h.db, shopID, question)

	case sceneDecisionSupport:
		// Same full context but with extracted keywords for targeted RAG search
		return h.injector.InjectWithQuery(ctx, h.db, shopID, extractRAGQuery(question))

	default:
		return h.injector.InjectOne(ctx, h.db, shopID, "shop_snapshot")
	}
}

// ── Daily Summary ──────────────────────────────────────────────────────────────

// buildDailySummary generates a morning-check overview from ShopSnapshot.
// Returns nil if no snapshot is available — the frontend will show a text greeting instead.
func (h *AdminChatHandler) buildDailySummary(ctx context.Context, shopID string) *DailySummary {
	if h.injector == nil || h.db == nil {
		return nil
	}

	snap := h.injector.InjectOne(ctx, h.db, shopID, "shop_snapshot")
	if snap == "" {
		slog.Info("admin_chat: no snapshot available for shop", "shop_id", shopID)
		return nil
	}

	return &DailySummary{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// extractRAGQuery derives a semantic search query from the user's question.
// Instead of using raw business questions ("should I raise prices?"), it extracts
// relevant product/category keywords for RAG.
func extractRAGQuery(question string) string {
	q := strings.ToLower(question)
	// Remove question words and extract meaningful terms
	q = strings.NewReplacer(
		"should i ", "", "what if ", "", "how ", "", "why ", "",
		"is ", "", "are ", "", "the ", "", "my ", "", "a ", "",
	).Replace(q)
	// Keep product/category-relevant keywords
	return strings.TrimSpace(q)
}

// suggestFollowUps returns contextual follow-up questions.
func suggestFollowUps(scene chatScene, previousAnswer string) []string {
	switch scene {
	case sceneQuickCheck:
		return []string{
			"Which products have the highest return rate?",
			"What categories are trending up?",
			"Any inventory alerts?",
		}
	case sceneRootCause:
		return []string{
			"What do customer reviews say about this?",
			"How does this compare to last month?",
			"What can I do to fix this?",
		}
	case sceneDecisionSupport:
		return []string{
			"How would this affect my margins?",
			"What are competitors charging?",
			"Show me the data behind this",
		}
	default:
		return []string{
			"How's business today?",
			"Which products should I restock?",
			"What's trending in my category?",
		}
	}
}

// ── Handlers ───────────────────────────────────────────────────────────────────

// Ask handles POST /api/admin/chat/ask — the main Q&A endpoint.
func (h *AdminChatHandler) Ask(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	var req AdminChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.ShopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		httputil.WriteError(w, http.StatusBadRequest, "question is required")
		return
	}

	shopID, err := database.ResolveShopID(h.db, req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	// Classify the question to route data fetching
	scene := classifyQuestion(req.Question)
	slog.Info("admin_chat: ask", "shop_id", shopID, "scene", scene, "question_len", len(req.Question))

	// Build context based on scene
	contextData := h.buildContext(r.Context(), shopID.String(), req.Question, scene)

	// Build messages with conversation history
	messages := []service.ChatMessage{
		{Role: "system", Content: buildAdminChatSystemPrompt(h.getBrandVoice(r.Context(), shopID.String()))},
	}
	for _, hist := range req.History {
		role := hist["role"]
		if role == "" {
			role = "user"
		}
		messages = append(messages, service.ChatMessage{Role: role, Content: hist["content"]})
	}

	// Assemble final user message
	userMsg := req.Question
	if contextData != "" {
		userMsg = fmt.Sprintf("[Store Data]\n%s\n\n[Question]\n%s", contextData, req.Question)
	}
	messages = append(messages, service.ChatMessage{Role: "user", Content: userMsg})

	// Call LLM
	provider, cfg, err := h.llmRouter.GetProvider(r.Context(), shopID)
	if err != nil {
		slog.Error("admin_chat: LLM provider unavailable", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "AI service unavailable: "+err.Error())
		return
	}
	result, err := provider.ChatCompletion(r.Context(), &service.ChatCompletionRequest{
		Model:       cfg.Model,
		Messages:    messages,
		Temperature: 0.5,
		MaxTokens:   1024,
	})
	if err != nil {
		slog.Error("admin_chat: LLM call failed", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "AI service unavailable: "+err.Error())
		return
	}

	resp := AdminChatResponse{
		Success:            true,
		Answer:             result,
		SuggestedQuestions: suggestFollowUps(scene, result),
	}

	PublishAIEvent(r.Context(), h.eventBus, shopID, "admin_chat.ask", req, resp, true)

	httputil.WriteOK(w, resp)
}

// AskSummary handles GET /api/admin/chat/summary — no question, just daily overview.
func (h *AdminChatHandler) AskSummary(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}
	shopIDStr := r.URL.Query().Get("shop_id")
	if shopIDStr == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	shopID, err := database.ResolveShopID(h.db, shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	// Build daily summary from snapshot
	summary := h.buildDailySummary(r.Context(), shopID.String())
	scene := sceneQuickCheck

	// Generate a natural-language morning greeting
	contextData := h.buildContext(r.Context(), shopID.String(), "daily summary", sceneQuickCheck)
	sysPrompt := contextData
	if sysPrompt != "" {
		sysPrompt += "\n\nGenerate a friendly morning briefing for the merchant (2-3 sentences). Be specific with numbers."
	} else {
		sysPrompt = "Generate a short friendly greeting for a Shopify merchant checking their store. No data available yet."
	}

	var result string
	summaryProvider, summaryCfg, summaryErr := h.llmRouter.GetProvider(r.Context(), shopID)
	if summaryErr != nil {
		slog.Error("admin_chat: LLM provider unavailable", "error", summaryErr)
		result = "Good morning! Your store data is still syncing. Check back shortly for your daily briefing."
	} else {
		result, err = summaryProvider.ChatCompletion(r.Context(), &service.ChatCompletionRequest{
			Model:       summaryCfg.Model,
			Messages: []service.ChatMessage{
				{Role: "system", Content: buildAdminChatSystemPrompt(h.getBrandVoice(r.Context(), shopID.String()))},
				{Role: "user", Content: sysPrompt},
			},
			Temperature: 0.5,
			MaxTokens:   512,
		})
		if err != nil {
			slog.Error("admin_chat: summary LLM call failed", "error", err)
			result = "Good morning! Your store data is still syncing. Check back shortly for your daily briefing."
		}
	}

	resp := AdminChatResponse{
		Success:            true,
		Answer:             result,
		DailySummary:       summary,
		SuggestedQuestions: suggestFollowUps(scene, ""),
	}

	PublishAIEvent(r.Context(), h.eventBus, shopID, "admin_chat.summary", nil, resp, true)

	httputil.WriteOK(w, resp)
}

// StreamAsk handles POST /api/admin/chat/stream — SSE streaming variant of Ask.
func (h *AdminChatHandler) StreamAsk(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	var req AdminChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.ShopID == "" || strings.TrimSpace(req.Question) == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id and question are required")
		return
	}

	shopID, err := database.ResolveShopID(h.db, req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
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

	scene := classifyQuestion(req.Question)
	contextData := h.buildContext(r.Context(), shopID.String(), req.Question, scene)

	// Build messages
	messages := []service.ChatMessage{
		{Role: "system", Content: buildAdminChatSystemPrompt(h.getBrandVoice(r.Context(), shopID.String()))},
	}
	for _, hist := range req.History {
		role := hist["role"]
		if role == "" {
			role = "user"
		}
		messages = append(messages, service.ChatMessage{Role: role, Content: hist["content"]})
	}

	userMsg := req.Question
	if contextData != "" {
		userMsg = fmt.Sprintf("[Store Data]\n%s\n\n[Question]\n%s", contextData, req.Question)
	}
	messages = append(messages, service.ChatMessage{Role: "user", Content: userMsg})

	// Real SSE streaming from LLM
	streamProvider, streamCfg, streamErr := h.llmRouter.GetProvider(r.Context(), shopID)
	if streamErr != nil {
		fmt.Fprintf(w, "data: {\"error\":\"AI service unavailable\"}\n\n")
		flusher.Flush()
		return
	}
	streamCh, err := streamProvider.ChatCompletionStream(r.Context(), &service.ChatCompletionRequest{
		Model:       streamCfg.Model,
		Messages:    messages,
		Temperature: 0.5,
		MaxTokens:   1024,
	})
	if err != nil {
		fmt.Fprintf(w, "data: {\"error\":\"%s\"}\n\n", err.Error())
		flusher.Flush()
		return
	}

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

	PublishAIEvent(r.Context(), h.eventBus, shopID, "admin_chat.stream", req, map[string]string{"answer": fullAnswer.String()}, true)

	suggested := suggestFollowUps(scene, fullAnswer.String())
	suggestedJSON, _ := json.Marshal(suggested)
	fmt.Fprintf(w, "data: {\"done\":true,\"suggested\":%s}\n\n", suggestedJSON)
	flusher.Flush()
}

// ── System Prompt ──────────────────────────────────────────────────────────────

// brandVoiceStyle returns the tone instruction for the given brand_voice value.
func brandVoiceStyle(brandVoice string) string {
	switch brandVoice {
	case "professional":
		return "Use a formal, business-appropriate tone. Be concise."
	case "luxury":
		return "Use an elegant, sophisticated tone. Reference premium positioning."
	case "playful":
		return "Use a fun, energetic tone. Be creative and engaging."
	default:
		return "Use a warm, conversational tone. Use emoji occasionally."
	}
}

// buildAdminChatSystemPrompt builds the system prompt with the given brand voice.
func buildAdminChatSystemPrompt(brandVoice string) string {
	return fmt.Sprintf(`You are an AI Operations Assistant for Shopify store owners. Your name is Vela.

## Capabilities
You have access to the merchant's real store data: products, orders, returns, customer reviews, inventory, Google Trends, and competitor pricing. This data is provided in the [Store Data] section of each question.

## Tone
%s

## Guidelines
- Answer concisely in the same language as the question
- Reference specific numbers from the data when available
- If data is missing or insufficient, be honest and suggest what data would help
- For operational questions, give actionable recommendations
- For strategic questions, provide data-backed reasoning
- Keep answers under 200 words unless asked for detail
- When suggesting follow-up questions, make them specific to the merchant's situation

## Data Sections You May See
- [店铺快照] — return rate, order count, top categories, inventory health
- [退货原因] — top return reasons with counts
- [销量] — best-selling products by quantity
- [趋势数据] — Google Trends search interest scores
- [语义上下文] — semantically relevant product/review content from the store

## What NOT to do
- Don't make up numbers not in the data
- Don't give generic advice — always reference the merchant's actual data
- Don't recommend changing Shopify settings (Sidekick does that)`, brandVoiceStyle(brandVoice))
}

// getBrandVoice reads the brand_voice from SalesAgentConfig, defaulting to "friendly".
func (h *AdminChatHandler) getBrandVoice(ctx context.Context, shopID string) string {
	if h.db == nil {
		return "friendly"
	}
	var cfg struct {
		BrandVoice string `gorm:"column:brand_voice"`
	}
	if err := h.db.WithContext(ctx).
		Table("sales_agent_configs").
		Where("shop_id = ?", shopID).
		Select("brand_voice").
		First(&cfg).Error; err != nil {
		return "friendly"
	}
	if cfg.BrandVoice == "" {
		return "friendly"
	}
	// Validate against known values (defense in depth, even though UpdateConfig validates writes)
	switch cfg.BrandVoice {
	case "friendly", "professional", "luxury", "playful":
		return cfg.BrandVoice
	default:
		slog.Warn("admin_chat: unexpected brand_voice in DB, falling back to friendly", "value", cfg.BrandVoice)
		return "friendly"
	}
}
