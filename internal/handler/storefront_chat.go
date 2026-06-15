package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/insight"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/internal/service/chat"
	"github.com/JingxuanC/vela-engine/internal/service/salesagent"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// planCacheTTL controls how long a shop plan check is cached in memory.
const planCacheTTL = 5 * time.Minute

type planCacheEntry struct {
	plan     string
	expireAt time.Time
}

// StorefrontChatHandler serves the AI Shopping Assistant widget on the storefront.
// Routes called via Shopify App Proxy: /apps/vela/chat/stream and /apps/vela/chat/suggest
// The widget calls API_BASE + "/chat/stream" and "/chat/suggest"
type StorefrontChatHandler struct {
	db           *gorm.DB
	llmRouter    *service.LLMRouter
	injector     *insight.ContextInjector
	salesAgent   *salesagent.SalesAgent
	fulfillmentH       *FulfillmentHandler
	fulfillmentInsight *insight.FulfillmentInsight
	planCache    sync.Map // shopID → planCacheEntry
}

// SetFulfillmentHandler injects the fulfillment handler for order tracking queries.
func (h *StorefrontChatHandler) SetFulfillmentHandler(fh *FulfillmentHandler) {
	h.fulfillmentH = fh
}

// SetFulfillmentInsight injects the fulfillment insight engine for recent updates context.
func (h *StorefrontChatHandler) SetFulfillmentInsight(fi *insight.FulfillmentInsight) {
	h.fulfillmentInsight = fi
}

func NewStorefrontChatHandler(db *gorm.DB, llmRouter *service.LLMRouter, injector *insight.ContextInjector, agent *salesagent.SalesAgent) *StorefrontChatHandler {
	return &StorefrontChatHandler{db: db, llmRouter: llmRouter, injector: injector, salesAgent: agent}
}

// resolveShopFromProxy extracts shop domain from the App Proxy query param.
func (h *StorefrontChatHandler) resolveShopFromProxy(r *http.Request) (string, error) {
	shop := r.URL.Query().Get("shop")
	if shop == "" {
		return "", fmt.Errorf("missing shop parameter")
	}
	// Shopify passes the shop as a full myshopify.com domain
	shopID, err := database.ResolveShopID(h.db, shop)
	if err != nil {
		return "", err
	}
	return shopID.String(), nil
}

// Stream handles SSE streaming chat from the storefront widget.
// Matches the widget's expected API: POST /chat/stream with {message, product_id, history}
func (h *StorefrontChatHandler) Stream(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Service unavailable")
		return
	}

	shopID, err := h.resolveShopFromProxy(r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop")
		return
	}

	// PAID PLAN: Route to Sales Agent
	if h.isProPlan(r.Context(), shopID) && h.salesAgent != nil {
		h.streamWithAgent(w, r, shopID)
		return
	}

	// FREE PLAN: Existing FAQ behavior (unchanged)
	var req struct {
		Message   string              `json:"message"`
		ProductID string              `json:"product_id"`
		History   []map[string]string `json:"history"`
	}
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

	// Build product context (public data only — title, description, category, vendor, tags)
	var productContext map[string]interface{}
	if h.db != nil && req.ProductID != "" {
		var p model.SyncedProduct
		if err := h.db.WithContext(r.Context()).
			Where("platform_id = ? OR id = ?", req.ProductID, req.ProductID).
			First(&p).Error; err == nil {
			productContext = map[string]interface{}{
				"name":        p.Title,
				"description": p.Description,
				"category":    p.ProductType,
				"vendor":      p.Vendor,
				"tags":        p.Tags,
			}
		}
	}

	// Filtered RAG: product + review only — no merchant data exposure
	if h.injector != nil && req.ProductID != "" {
		if fetcher := h.injector.GetFetcher("rag"); fetcher != nil {
			if ss, ok := fetcher.(insight.SourceSetter); ok {
				ss.SetSources([]string{"product", "review"})
			}
		}
		ragContext := h.injector.InjectWithQuery(r.Context(), h.db, shopID, req.Message)
		if ragContext != "" {
			if productContext == nil {
				productContext = make(map[string]interface{})
			}
			productContext["rag_context"] = ragContext
		}
	}

	// Fulfillment tracking context: inject when customer asks about order/shipping/tracking
	if h.fulfillmentH != nil && h.db != nil {
		if isTrackingQuery(req.Message) {
			trackingCtx := h.buildTrackingContext(r, shopID, req.Message)
			if trackingCtx != nil {
				if productContext == nil {
					productContext = make(map[string]interface{})
				}
				productContext["fulfillment_context"] = trackingCtx
			}
		}

		// Always inject recent fulfillment updates as background context
		recentUpdates := h.buildRecentFulfillmentUpdates(r, shopID)
		if recentUpdates != "" {
			if productContext == nil {
				productContext = make(map[string]interface{})
			}
			productContext["recent_fulfillment_updates"] = recentUpdates
		}
	}

	stProvider, _, stErr := h.llmRouter.GetProvider(r.Context(), uuid.Nil)
	if stErr != nil {
		fmt.Fprintf(w, "data: {\"error\":\"AI service unavailable\"}\n\n")
		flusher.Flush()
		return
	}
	result, err := chat.AnswerQuestion(r.Context(), stProvider, req.ProductID, req.Message, req.History, productContext)
	if err != nil {
		fmt.Fprintf(w, "data: {\"error\":\"AI service unavailable\"}\n\n")
		flusher.Flush()
		return
	}

	// Simulate SSE streaming by chunking the answer (legacy: use ChatCompletionStream for real SSE)
	answer := result.Answer
	for i := 0; i < len(answer); i++ {
		chunk := string(answer[i])
		jsonToken, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: {\"token\":%s}\n\n", jsonToken)
		flusher.Flush()
	}
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// Suggest handles FAQ question suggestions from the storefront widget.
// Matches the widget's expected API: POST /chat/suggest with {product_id}
func (h *StorefrontChatHandler) Suggest(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Service unavailable")
		return
	}

	_, err := h.resolveShopFromProxy(r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop")
		return
	}

	var req struct {
		ProductID string `json:"product_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	// Build product info for FAQ generation
	productInfo := map[string]interface{}{}
	if h.db != nil && req.ProductID != "" {
		var p model.SyncedProduct
		if err := h.db.WithContext(r.Context()).
			Where("platform_id = ? OR id = ?", req.ProductID, req.ProductID).
			First(&p).Error; err == nil {
			productInfo["name"] = p.Title
			productInfo["type"] = p.ProductType
			productInfo["vendor"] = p.Vendor
		}
	}

	sgProvider, _, sgErr := h.llmRouter.GetProvider(r.Context(), uuid.Nil)
	if sgErr != nil {
		slog.Warn("storefront: FAQ suggest LLM unavailable", "error", sgErr)
		httputil.WriteOK(w, map[string]interface{}{
			"success":     true,
			"suggestions": fallbackSuggestions(),
		})
		return
	}
	result, err := chat.SuggestQuestions(r.Context(), sgProvider, productInfo, 4)
	if err != nil {
		slog.Warn("storefront: FAQ suggest failed", "error", err)
		// Fallback to generic suggestions
		httputil.WriteOK(w, map[string]interface{}{
			"success":     true,
			"suggestions": fallbackSuggestions(),
		})
		return
	}

	suggestions := make([]map[string]string, len(result.Questions))
	for i, q := range result.Questions {
		suggestions[i] = map[string]string{"question": q}
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success":     true,
		"suggestions": suggestions,
	})
}

func fallbackSuggestions() []map[string]string {
	return []map[string]string{
		{"question": "Does this run true to size?"},
		{"question": "What material is this made of?"},
		{"question": "How should I care for this item?"},
		{"question": "What's your return policy?"},
	}
}


// isProPlan returns true if the shop is on Growth or Pro plan (eligible for Sales Agent).
// Results are cached in memory for planCacheTTL to avoid per-message DB queries.
func (h *StorefrontChatHandler) isProPlan(ctx context.Context, shopID string) bool {
	if h.db == nil {
		return false
	}

	// Check in-memory cache
	if v, ok := h.planCache.Load(shopID); ok {
		entry := v.(planCacheEntry)
		if time.Now().Before(entry.expireAt) {
			return entry.plan == "growth" || entry.plan == "pro"
		}
		// Expired — remove and re-fetch
		h.planCache.Delete(shopID)
	}

	var plan string
	err := h.db.WithContext(ctx).Table("shops").Where("id = ?", shopID).Select("plan").Scan(&plan).Error
	if err != nil {
		return false
	}

	h.planCache.Store(shopID, planCacheEntry{
		plan:     plan,
		expireAt: time.Now().Add(planCacheTTL),
	})
	return plan == "growth" || plan == "pro"
}

// streamWithAgent routes the conversation to the Sales Agent with SSE streaming.
func (h *StorefrontChatHandler) streamWithAgent(w http.ResponseWriter, r *http.Request, shopID string) {
	sse, err := httputil.NewSSEWriter(w)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "SSE not supported")
		return
	}

	// Decode message body (read raw first for debugging)
	bodyBytes, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		slog.Warn("storefront: failed to read body", "err", readErr)
		sse.WriteToken("Sorry, I couldn't understand that. Can you try again?")
		sse.WriteDone(nil)
		return
	}
	var req struct {
		Message   string              `json:"message"`
		ProductID string              `json:"product_id"`
		History   []map[string]string `json:"history"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		slog.Warn("storefront: decode agent body failed", "body", string(bodyBytes), "err", err)
		sse.WriteToken("Sorry, I couldn't understand that. Can you try again?")
		sse.WriteDone(nil)
		return
	}

	customerID := r.Header.Get("X-Vela-Session")     // Shopify customer ID (set by widget)
	customerEmail := r.Header.Get("X-Vela-Customer-Email")  // set by widget if available
	if customerEmail == "" {
		customerEmail = r.URL.Query().Get("customer_email") // legacy fallback
	}
	if customerID == "" {
		customerID = r.Header.Get("X-Vela-Device-ID")
	}
	if customerID == "" {
		customerID = fmt.Sprintf("guest_%d", time.Now().UnixNano())
	}

	err = h.salesAgent.Chat(r.Context(), sse, shopID, customerID, customerEmail, req.Message)
	if err != nil {
		slog.Error("sales agent chat failed", "err", err)
	}
}

// ── Fulfillment tracking helpers ──────────────────────────────────────────────

// trackingKeywords are words that indicate a customer is asking about order tracking.
var trackingKeywords = []string{
	"order", "track", "tracking", "ship", "shipping", "delivery", "delivered",
	"where is my", "package", "快递", "物流", "发货", "签收",
	"status", "arrive", "estimate", "carrier", "运单",
}

// isTrackingQuery returns true if the message appears to be about order tracking.
func isTrackingQuery(msg string) bool {
	lower := strings.ToLower(msg)
	for _, kw := range trackingKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// buildTrackingContext queries fulfillment_events and synced_orders for tracking info.
// Returns a context map that can be injected into the chat prompt.
func (h *StorefrontChatHandler) buildTrackingContext(r *http.Request, shopID, msg string) map[string]interface{} {
	ctx := r.Context()
	shopUUID, err := uuid.Parse(shopID)
	if err != nil {
		return nil
	}

	// Try to extract an order number from the message (e.g., #1234, order 1234)
	orderNum := extractOrderNumber(msg)
	if orderNum == 0 {
		return nil
	}

	var order model.SyncedOrder
	if err := h.db.WithContext(ctx).
		Where("shop_id = ? AND order_number = ?", shopUUID, orderNum).
		First(&order).Error; err != nil {
		return nil
	}

	context := map[string]interface{}{
		"order_number": fmt.Sprintf("#%d", order.OrderNumber),
	}

	if order.TrackingNumber != "" {
		context["tracking_number"] = order.TrackingNumber
		context["carrier"] = order.Carrier
		if order.TrackingURL != "" {
			context["tracking_url"] = order.TrackingURL
		}
	}
	if order.LatestEventStatus != "" {
		context["latest_status"] = order.LatestEventStatus
	}

	// Also pull recent events
	var events []model.FulfillmentEvent
	h.db.WithContext(ctx).
		Where("order_id = ?", order.ID).
		Order("happened_at DESC").
		Limit(5).
		Find(&events)

	if len(events) > 0 {
		eventList := make([]map[string]interface{}, 0, len(events))
		for _, e := range events {
			evt := map[string]interface{}{
				"status":  e.Status,
				"message": e.Message,
				"date":    e.HappenedAt.Format("2006-01-02 15:04"),
			}
			if e.City != "" {
				evt["location"] = fmt.Sprintf("%s, %s", e.City, e.Province)
			}
			eventList = append(eventList, evt)
		}
		context["events"] = eventList
	}

	return context
}

// extractOrderNumber attempts to find an order number in the message.
// Looks for patterns like #1234, order 1234, Order #1234.
func extractOrderNumber(msg string) int {
	// Match # followed by digits
	for i := 0; i < len(msg); i++ {
		if msg[i] == '#' {
			num := 0
			j := i + 1
			for j < len(msg) && msg[j] >= '0' && msg[j] <= '9' {
				num = num*10 + int(msg[j]-'0')
				j++
			}
			if num > 0 {
				return num
			}
		}
	}
	// Match "order" followed by digits
	lower := strings.ToLower(msg)
	idx := strings.Index(lower, "order")
	if idx >= 0 {
		// Skip past "order" and optional space/# 
		start := idx + 5
		for start < len(msg) && (msg[start] == ' ' || msg[start] == '#') {
			start++
		}
		num := 0
		for start < len(msg) && msg[start] >= '0' && msg[start] <= '9' {
			num = num*10 + int(msg[start]-'0')
			start++
		}
		if num > 0 {
			return num
		}
	}
	return 0
}

// buildRecentFulfillmentUpdates queries recent fulfillment events for the shop (last 24h)
// and returns a summary string like "Recent order updates: #1001 IN_TRANSIT (Shanghai), #1002 DELIVERED".
// Returns empty string if no recent updates.
func (h *StorefrontChatHandler) buildRecentFulfillmentUpdates(r *http.Request, shopID string) string {
	ctx := r.Context()
	shopUUID, err := uuid.Parse(shopID)
	if err != nil {
		return ""
	}

	type updateRow struct {
		OrderNumber int
		Status      string
		City        string
	}
	var rows []updateRow

	h.db.WithContext(ctx).Raw(`
		SELECT DISTINCT ON (so.order_number)
		       so.order_number,
		       fe.status,
		       fe.city
		FROM fulfillment_events fe
		JOIN synced_orders so ON so.id = fe.order_id
		WHERE fe.shop_id = ?
		  AND fe.happened_at > NOW() - INTERVAL '24 hours'
		ORDER BY so.order_number, fe.happened_at DESC
		LIMIT 10
	`, shopUUID).Scan(&rows)

	if len(rows) == 0 {
		return ""
	}

	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		loc := ""
		if r.City != "" {
			loc = fmt.Sprintf(" (%s)", r.City)
		}
		parts = append(parts, fmt.Sprintf("#%d %s%s", r.OrderNumber, r.Status, loc))
	}

	return "Recent order updates: " + strings.Join(parts, ", ")
}
