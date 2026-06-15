package salesagent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// SalesAgent is an AI shopping assistant that uses ReAct pattern with SSE streaming.
type SalesAgent struct {
	db         *gorm.DB
	cache      *service.CacheService
	llmRouter  *service.LLMRouter
	httpClient *http.Client
	eventBus   eventbus.EventBus // VCI signal publishing
}

// NewSalesAgent creates a new SalesAgent.
func NewSalesAgent(db *gorm.DB, cache *service.CacheService, llmRouter *service.LLMRouter) *SalesAgent {
	return &SalesAgent{
		db:         db,
		cache:      cache,
		llmRouter:  llmRouter,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// SetEventBus injects the EventBus for VCI signal publishing.
func (a *SalesAgent) SetEventBus(bus eventbus.EventBus) {
	a.eventBus = bus
}

// appendUnique adds item to slice if not already present.
func appendUnique(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}

// Chat runs the Sales Agent conversation loop and streams output via SSE.
func (a *SalesAgent) Chat(ctx context.Context, sse *httputil.SSEWriter, shopID, customerID, customerEmail, userMsg string) error {
	// 1. Load session from Redis (24h TTL)
	sess, err := a.getAgentSession(ctx, shopID, customerID)
	if err != nil {
		slog.Error("salesagent: failed to load session", "shop_id", shopID, "customer_id", customerID, "err", err)
		sse.WriteToken("Sorry, I'm having trouble right now. Please try again in a moment.")
		sse.WriteDone(nil)
		return nil
	}

	// 1.5 Load persistent customer profile + merge into session (long-term memory)
	profile, _ := a.loadCustomerProfile(ctx, shopID, customerID)
	if profile != nil {
		mergeProfileIntoSession(sess, profile)
		// Increment session count (once per conversation, not per signal)
		if a.db != nil {
			a.db.WithContext(ctx).Model(profile).UpdateColumn("total_chat_sessions", gorm.Expr("total_chat_sessions + 1"))
		}
	}

	// 2. Build messages: system + persona + history + latest user message
	messages := a.buildMessages(sess, userMsg, profile)

	// 3. ReAct loop (max 5 iterations)
	const maxIterations = 5
	var lastProducts interface{} // cached for SSE streaming to widget
	var lastDiscount interface{}
	for i := 0; i < maxIterations; i++ {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// 3a. Call LLM with streaming (token-by-token SSE)
		llmCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		shopUUID, _ := uuid.Parse(shopID)
		response, isToolCall, err := a.callLLMStream(llmCtx, sse, messages, shopUUID)
		cancel()
		if err != nil {
			slog.Warn("salesagent: LLM call failed", "err", err)
			a.saveAgentSession(ctx, sess) // P1-5: preserve conversation
			sse.WriteToken("Sorry, I'm having trouble right now. Please try again or browse our products directly.")
			sse.WriteDone(nil)
			return nil
		}

		// 3b. Check for tool call
		tc := parseToolCall(response)
		if !isToolCall || tc == nil {
			// No tool call → tokens already streamed to user, extract reply for history
			reply := parseReply(response)
			if reply == "" {
				reply = stripXMLTags(response)
			}

			// Send product cards and discount to widget via SSE
			doneMeta := map[string]interface{}{"tool_calls": i}
			if lastProducts != nil {
				doneMeta["products"] = lastProducts
			}
			if lastDiscount != nil {
				if dm, ok := lastDiscount.(map[string]interface{}); ok {
					for k, v := range dm {
						doneMeta[k] = v
					}
				}
			}
			sse.WriteDone(doneMeta)

			sess.MessageHistory = append(sess.MessageHistory,
				service.ChatMessage{Role: "user", Content: userMsg},
				service.ChatMessage{Role: "assistant", Content: reply},
			)

			// ── VCI: extract + publish intent signals ──
			signals := a.extractSignals(sess, userMsg, reply)
			if len(signals) > 0 && a.eventBus != nil {
				shopUUID, err := uuid.Parse(shopID)
				if err != nil {
					slog.Warn("salesagent: invalid shop UUID for signal publish", "shop_id", shopID)
				} else {
					for _, sig := range signals {
						evt, err := eventbus.NewEvent(
							eventbus.EventType("chat.intent."+sig.Type),
							shopUUID,
							sig,
							"sales_agent",
						)
						if err != nil {
							slog.Warn("salesagent: failed to create signal event", "err", err)
							continue
						}
						if err := a.eventBus.Publish(ctx, evt); err != nil {
							slog.Warn("salesagent: failed to publish signal", "type", sig.Type, "err", err)
						}
					}
					// Store signals in Preferences (no LLM context pollution)
					applySignalsToSession(sess, signals)
				}
			}

			a.saveAgentSession(ctx, sess)
			return nil
		}

		// 3c. Execute tool — inject shop/customer/session context
		tc.Args["shop_id"] = shopID
		tc.Args["customer_email"] = customerEmail
		tc.Args["customer_id"] = customerID
		tc.Args["session_id"] = fmt.Sprintf("chat_session:%s:%s", shopID, customerID)
		// Inject customer preferences for search ranking
		if sess.Preferences != nil {
			if v, ok := sess.Preferences["size"]; ok { tc.Args["customer_size"] = v }
			if v, ok := sess.Preferences["colors"]; ok { tc.Args["customer_colors"] = v }
			if v, ok := sess.Preferences["price_sensitive"]; ok { tc.Args["customer_price_sensitive"] = v }
		}

		toolResult, toolErr := a.execTool(ctx, tc)

		// Capture product/discount data for SSE streaming + session memory
		if toolErr == nil && toolResult != nil {
			if rm, ok := toolResult.(map[string]interface{}); ok {
				if products, hasProducts := rm["products"]; hasProducts {
					// Accumulate products from multiple searches (merge, don't overwrite)
					if plist, ok := products.([]ProductResult); ok {
						for _, pr := range plist {
							sess.DiscussedProducts = appendUnique(sess.DiscussedProducts, pr.ID)
						}
						// Merge with previously found products, dedup by ID
						if existing, ok := lastProducts.([]ProductResult); ok {
							seen := map[string]bool{}
							for _, p := range existing { seen[p.ID] = true }
							for _, p := range plist {
								if !seen[p.ID] { existing = append(existing, p) }
							}
							lastProducts = existing
						} else {
							lastProducts = plist
						}
					}
				}
				if _, hasCode := rm["code"]; hasCode {
					lastDiscount = rm
				}
				if _, hasURL := rm["url"]; hasURL {
					sess.Stage = "closing"
				} else if _, hasProducts := rm["products"]; hasProducts {
					sess.Stage = "recommendation"
				}
			}
		}

		sess.MessageHistory = append(sess.MessageHistory,
			service.ChatMessage{Role: "assistant", Content: response},
		)

		if toolErr != nil {
			slog.Warn("salesagent: tool failed", "tool", tc.Name, "err", toolErr)
			sess.MessageHistory = append(sess.MessageHistory,
				service.ChatMessage{Role: "tool", Content: fmt.Sprintf("Tool %s failed. Do NOT retry. Tell customer this action is unavailable.", tc.Name)},
			)
		} else {
			resultJSON, _ := json.Marshal(toolResult)
			sess.MessageHistory = append(sess.MessageHistory,
				service.ChatMessage{Role: "tool", Content: string(resultJSON)},
			)
		}

		// 3d. Rebuild messages with updated history, continue loop
		messages = a.buildMessages(sess, userMsg, profile)
	}

	// 4. Exceeded max iterations — force close with session preserved
	a.saveAgentSession(ctx, sess)
	sse.WriteToken("I've been chatting for a while! Feel free to browse our store directly, or ask me a specific question anytime.")
	sse.WriteDone(nil)
	return nil
}

// ── Message Builder ──────────────────────────────────────────────────────────

func (a *SalesAgent) buildMessages(sess *ChatSession, userMsg string, profile *model.CustomerChatProfile) []service.ChatMessage {
	msgs := make([]service.ChatMessage, 0, len(sess.MessageHistory)+3)

	// System prompt base
	systemContent := getSystemBlock()

	// Inject customer persona context (long-term memory from VCI)
	if personaCtx := buildPersonaContext(profile, sess); personaCtx != "" {
		systemContent += "\n" + personaCtx
	}

	msgs = append(msgs, service.ChatMessage{
		Role:    "system",
		Content: systemContent,
	})

	for _, m := range sess.MessageHistory {
		msgs = append(msgs, m)
	}

	// Add user message if not already the last message
	if len(sess.MessageHistory) == 0 ||
		sess.MessageHistory[len(sess.MessageHistory)-1].Role != "user" ||
		sess.MessageHistory[len(sess.MessageHistory)-1].Content != userMsg {
		msgs = append(msgs, service.ChatMessage{Role: "user", Content: userMsg})
	}

	return msgs
}

// ── LLM Call (Streaming) ──────────────────────────────────────────────────────

// callLLMStream calls the LLM with streaming and returns the full response.
// Tokens are streamed to the SSE writer UNLESS the response contains a tool call.
func (a *SalesAgent) callLLMStream(ctx context.Context, sse *httputil.SSEWriter, messages []service.ChatMessage, shopID uuid.UUID) (fullResponse string, isToolCall bool, err error) {
	start := time.Now()
	provider, cfg, err := a.llmRouter.GetProvider(ctx, shopID)
	if err != nil {
		slog.Warn("llm", "module", "salesagent", "status", "error", "error", err)
		return "", false, fmt.Errorf("get provider: %w", err)
	}

	req := &service.ChatCompletionRequest{
		Model:       cfg.Model,
		Messages:    messages,
		Temperature: 0.7,
		MaxTokens:   600,
	}
	if req.Model == "" {
		req.Model = "qwen-plus"
	}

	stream, err := provider.ChatCompletionStream(ctx, req)
	if err != nil {
		slog.Warn("llm", "module", "salesagent", "model", req.Model, "status", "error", "error", err)
		return "", false, err
	}

	var sb strings.Builder
	toolDetected := false
	streaming := true  // start streaming to user; suppress if tool call detected
	tokenCount := 0

	for chunk := range stream {
		if chunk.Error != "" {
			return sb.String(), false, fmt.Errorf("stream error: %s", chunk.Error)
		}
		if chunk.Finish {
			break
		}
		sb.WriteString(chunk.Token)
		tokenCount++

		// Tool call detection: check first 80 accumulated chars for <tool>
		if !toolDetected && sb.Len() >= 10 {
			prefix := sb.String()
			if len(prefix) > 80 {
				prefix = prefix[:80]
			}
			if strings.Contains(prefix, "<tool>") {
				toolDetected = true
				streaming = false
			}
		}

		// Stream token to user — strip XML tags for replies
		if streaming {
			select {
			case <-ctx.Done():
				return sb.String(), false, ctx.Err()
			default:
			}
			// Strip known XML tags from each token before streaming to user
			clean := knownXMLTag.ReplaceAllString(chunk.Token, "")
			if clean != "" {
				if err := sse.WriteToken(clean); err != nil {
					return sb.String(), false, nil // client disconnected, stop
				}
			}
		}
	}

	durationMs := float64(time.Since(start).Microseconds()) / 1000.0
	slog.Info("llm", "module", "salesagent", "model", req.Model, "tokens_out", tokenCount, "duration_ms", int(durationMs), "tool_call", toolDetected)

	return sb.String(), toolDetected, nil
}

// ── Tool Dispatcher ──────────────────────────────────────────────────────────

func (a *SalesAgent) execTool(ctx context.Context, tc *ToolCall) (interface{}, error) {
	switch tc.Name {
	case "search_products":
		return a.searchProducts(ctx, tc.Args)
	case "generate_link":
		return a.generateLink(ctx, tc.Args)
	case "generate_discount":
		return a.generateDiscount(ctx, tc.Args)
	case "get_session":
		shopID := getStringArg(tc.Args, "shop_id")
		customerID := getStringArg(tc.Args, "customer_id")
		sess, err := a.getAgentSession(ctx, shopID, customerID)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"preferences":        sess.Preferences,
			"discussed_products": sess.DiscussedProducts,
			"stage":              sess.Stage,
		}, nil
	case "analyze_returns":
		return a.analyzeReturns(ctx, tc.Args)
	case "send_email":
		return a.sendEmail(ctx, tc.Args)
	case "generate_report":
		return a.generateReport(ctx, tc.Args)
	case "web_search":
		return a.webSearch(ctx, tc.Args)
	case "get_tracking":
		return a.getTracking(ctx, tc.Args)
	default:
		return nil, fmt.Errorf("unknown tool: %s", tc.Name)
	}
}

// ── Tool Call Parser ─────────────────────────────────────────────────────────

// parseToolCall extracts <tool>CALL</tool><name>X</name><args>{}</args> from LLM output.
func parseToolCall(llmResponse string) *ToolCall {
	toolMarker := "<tool>CALL</tool>"
	toolIdx := strings.Index(llmResponse, toolMarker)
	if toolIdx == -1 {
		return nil
	}

	afterTool := llmResponse[toolIdx+len(toolMarker):]

	nameStart := strings.Index(afterTool, "<name>")
	nameEnd := strings.Index(afterTool, "</name>")
	argsStart := strings.Index(afterTool, "<args>")
	argsEnd := strings.Index(afterTool, "</args>")

	if nameStart == -1 || nameEnd == -1 || argsStart == -1 || argsEnd == -1 {
		return nil
	}

	name := strings.TrimSpace(afterTool[nameStart+6 : nameEnd])
	argsRaw := strings.TrimSpace(afterTool[argsStart+6 : argsEnd])

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsRaw), &args); err != nil {
		slog.Warn("salesagent: tool args parse failed", "args", argsRaw, "err", err)
		return nil
	}

	return &ToolCall{Name: name, Args: args}
}

// parseReply extracts <reply>message</reply> from LLM output.
func parseReply(llmResponse string) string {
	start := strings.Index(llmResponse, "<reply>")
	end := strings.Index(llmResponse, "</reply>")
	if start != -1 && end != -1 && end > start {
		return strings.TrimSpace(llmResponse[start+7 : end])
	}
	// Fallback: extract text after tool call block
	toolEnd := strings.Index(llmResponse, "</tool>")
	if toolEnd != -1 {
		after := strings.TrimSpace(llmResponse[toolEnd+7:])
		if after != "" && !strings.HasPrefix(after, "<") {
			return after
		}
	}
	return ""
}

// knownXMLTag matches only our known tool/reply XML tags.
var knownXMLTag = regexp.MustCompile(`</?(tool|name|args|reply|CALL)>`)

// stripXMLTags removes known XML tags from text without destroying legitimate content.
func stripXMLTags(text string) string {
	return strings.TrimSpace(knownXMLTag.ReplaceAllString(text, ""))
}
