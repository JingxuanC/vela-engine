package salesagent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/service"
)

// ── parseToolCall Tests ──────────────────────────────────────────────────────

func TestParseToolCall_ValidSearchProducts(t *testing.T) {
	llmOutput := `<tool>CALL</tool><name>search_products</name><args>{"query":"blue dress","color":"blue"}</args>`
	tc := parseToolCall(llmOutput)
	require.NotNil(t, tc)
	assert.Equal(t, "search_products", tc.Name)
	assert.Equal(t, "blue dress", tc.Args["query"])
	assert.Equal(t, "blue", tc.Args["color"])
}

func TestParseToolCall_ValidGenerateLink(t *testing.T) {
	llmOutput := `<tool>CALL</tool><name>generate_link</name><args>{"product_id":"gid://shopify/Product/123456"}</args>`
	tc := parseToolCall(llmOutput)
	require.NotNil(t, tc)
	assert.Equal(t, "generate_link", tc.Name)
	assert.Equal(t, "gid://shopify/Product/123456", tc.Args["product_id"])
}

func TestParseToolCall_NoToolCall(t *testing.T) {
	llmOutput := `<reply>Here is some helpful information about our products.</reply>`
	tc := parseToolCall(llmOutput)
	assert.Nil(t, tc)
}

func TestParseToolCall_MalformedXML_MissingToolTag(t *testing.T) {
	llmOutput := `<name>search_products</name><args>{"query":"shoes"}</args>`
	tc := parseToolCall(llmOutput)
	assert.Nil(t, tc)
}

func TestParseToolCall_MalformedXML_MissingNameEnd(t *testing.T) {
	llmOutput := `<tool>CALL</tool><name>search_products<args>{"query":"shoes"}</args>`
	tc := parseToolCall(llmOutput)
	assert.Nil(t, tc)
}

func TestParseToolCall_BrokenJSONArgs(t *testing.T) {
	llmOutput := `<tool>CALL</tool><name>search_products</name><args>{broken json}</args>`
	tc := parseToolCall(llmOutput)
	assert.Nil(t, tc)
}

func TestParseToolCall_ToolWithReplyText(t *testing.T) {
	// LLM sometimes outputs tool call + reply text
	llmOutput := `<tool>CALL</tool><name>search_products</name><args>{"query":"hat"}</args><reply>Let me search for hats for you!</reply>`
	tc := parseToolCall(llmOutput)
	require.NotNil(t, tc)
	assert.Equal(t, "search_products", tc.Name)
	assert.Equal(t, "hat", tc.Args["query"])
}

func TestParseToolCall_WhitespaceInArgs(t *testing.T) {
	llmOutput := `<tool>CALL</tool><name>search_products</name><args>{"query": "winter coat", "color": "navy"}</args>`
	tc := parseToolCall(llmOutput)
	require.NotNil(t, tc)
	assert.Equal(t, "winter coat", tc.Args["query"])
	assert.Equal(t, "navy", tc.Args["color"])
}

// ── parseReply Tests ─────────────────────────────────────────────────────────

func TestParseReply_ValidReply(t *testing.T) {
	llmOutput := `<reply>Yes, we have the Summer Blue Dress in stock!</reply>`
	reply := parseReply(llmOutput)
	assert.Equal(t, "Yes, we have the Summer Blue Dress in stock!", reply)
}

func TestParseReply_NoReplyTag(t *testing.T) {
	llmOutput := `Here is a plain text response without any tags.`
	reply := parseReply(llmOutput)
	assert.Equal(t, "", reply)
}

func TestParseReply_FallbackToTextAfterTool(t *testing.T) {
	// Text after </tool> that doesn't start with < is used as fallback
	llmOutput := `<tool>CALL</tool>I found some great shoes for you!`
	reply := parseReply(llmOutput)
	assert.Equal(t, "I found some great shoes for you!", reply)
}

func TestParseReply_ToolEndButStartsWithTag(t *testing.T) {
	// If the text after </tool> starts with < it should not be used
	llmOutput := `<tool>CALL</tool><name>search_products</name><args>{"query":"shoes"}</args></tool><reply>test</reply>`
	reply := parseReply(llmOutput)
	assert.Equal(t, "test", reply)
}

func TestParseReply_EmptyContent(t *testing.T) {
	llmOutput := `<reply>   </reply>`
	reply := parseReply(llmOutput)
	assert.Equal(t, "", reply)
}

func TestParseReply_MultilineReply(t *testing.T) {
	llmOutput := `<reply>Here's what I found:

Option 1: Blue Dress - $49.99
Option 2: Red Dress - $59.99

Which one would you like?</reply>`
	reply := parseReply(llmOutput)
	assert.Contains(t, reply, "Blue Dress")
	assert.Contains(t, reply, "Red Dress")
}

// ── stripXMLTags Tests ───────────────────────────────────────────────────────

func TestStripXMLTags_Empty(t *testing.T) {
	assert.Equal(t, "", stripXMLTags(""))
}

func TestStripXMLTags_NoTags(t *testing.T) {
	text := "Hello, how can I help you today?"
	assert.Equal(t, text, stripXMLTags(text))
}

func TestStripXMLTags_SingleTag(t *testing.T) {
	text := "<tool>CALL</tool>Hello there"
	result := stripXMLTags(text)
	assert.Equal(t, "CALLHello there", result) // content between tags is preserved
}

func TestStripXMLTags_MultipleTags(t *testing.T) {
	text := "<reply>Great choice!</reply> Would you like to purchase?"
	result := stripXMLTags(text)
	assert.NotContains(t, result, "<reply>")
	assert.NotContains(t, result, "</reply>")
}

// ── Argument Helpers ─────────────────────────────────────────────────────────

func TestGetStringArg_Present(t *testing.T) {
	args := map[string]interface{}{"query": "blue dress", "count": 5}
	assert.Equal(t, "blue dress", getStringArg(args, "query"))
}

func TestGetStringArg_Missing(t *testing.T) {
	args := map[string]interface{}{"query": "blue dress"}
	assert.Equal(t, "", getStringArg(args, "color"))
}

func TestGetStringArg_IntToString(t *testing.T) {
	args := map[string]interface{}{"limit": 10}
	assert.Equal(t, "10", getStringArg(args, "limit"))
}

func TestExtractShopifyNumericID_WithGID(t *testing.T) {
	assert.Equal(t, "123456789", extractShopifyNumericID("gid://shopify/Product/123456789"))
}

func TestExtractShopifyNumericID_PlainID(t *testing.T) {
	assert.Equal(t, "12345", extractShopifyNumericID("12345"))
}

func TestExtractShopifyNumericID_WithSlash(t *testing.T) {
	assert.Equal(t, "abc123", extractShopifyNumericID("products/abc123"))
}

// ── Variant Helpers ──────────────────────────────────────────────────────────

func TestParseVariantList_ValidJSON(t *testing.T) {
	raw := datatypes.JSON(`[{"id":1,"title":"S / Blue","price":"29.99","inventory_quantity":5}]`)
	variants := parseVariantList(raw)
	require.Len(t, variants, 1)
	assert.Equal(t, int64(1), variants[0].ID)
	assert.Equal(t, "S / Blue", variants[0].Title)
	assert.Equal(t, "29.99", variants[0].Price)
	assert.Equal(t, 5, variants[0].InventoryQuantity)
}

func TestParseVariantList_Empty(t *testing.T) {
	variants := parseVariantList(datatypes.JSON(""))
	assert.Len(t, variants, 0)
}

func TestParseVariantList_InvalidJSON(t *testing.T) {
	variants := parseVariantList(datatypes.JSON("{broken"))
	assert.Len(t, variants, 0)
}

func TestFilterAvailableVariants_AllInStock(t *testing.T) {
	variants := []shopifyVariant{
		{ID: 1, Title: "S", Price: "10", InventoryQuantity: 5},
		{ID: 2, Title: "M", Price: "10", InventoryQuantity: 3},
	}
	available := filterAvailableVariants(variants)
	assert.Len(t, available, 2)
}

func TestFilterAvailableVariants_SomeOutOfStock(t *testing.T) {
	variants := []shopifyVariant{
		{ID: 1, Title: "S", Price: "10", InventoryQuantity: 5},
		{ID: 2, Title: "M", Price: "10", InventoryQuantity: 0},
		{ID: 3, Title: "L", Price: "10", InventoryQuantity: 2},
	}
	available := filterAvailableVariants(variants)
	assert.Len(t, available, 2)
	assert.Equal(t, int64(1), available[0].ID)
	assert.Equal(t, int64(3), available[1].ID)
}

func TestFilterAvailableVariants_AllOutOfStock(t *testing.T) {
	variants := []shopifyVariant{
		{ID: 1, Title: "S", Price: "10", InventoryQuantity: 0},
		{ID: 2, Title: "M", Price: "10", InventoryQuantity: 0},
	}
	available := filterAvailableVariants(variants)
	assert.Len(t, available, 0)
}

func TestFormatVariantPrices_SinglePrice(t *testing.T) {
	variants := []shopifyVariant{
		{ID: 1, Title: "S", Price: "29.99"},
	}
	assert.Equal(t, "$29.99", formatVariantPrices(variants))
}

func TestFormatVariantPrices_PriceRange(t *testing.T) {
	// Prices should be sorted numerically, regardless of input order
	variants := []shopifyVariant{
		{ID: 2, Title: "M", Price: "15.00"},
		{ID: 3, Title: "L", Price: "20.00"},
		{ID: 1, Title: "S", Price: "10.00"},
	}
	assert.Equal(t, "$10.00 – $20.00", formatVariantPrices(variants))
}

func TestFormatVariantPrices_Empty(t *testing.T) {
	assert.Equal(t, "0", formatVariantPrices([]shopifyVariant{}))
}

func TestFormatVariantOptions(t *testing.T) {
	variants := []shopifyVariant{
		{ID: 1, Title: "S / Blue", Price: "29.99"},
		{ID: 2, Title: "M / Blue", Price: "29.99"},
	}
	options := formatVariantOptions(variants)
	assert.Len(t, options, 2)
	assert.Equal(t, "S / Blue - $29.99", options[0])
	assert.Equal(t, "M / Blue - $29.99", options[1])
}

func TestFirstImageURL_Valid(t *testing.T) {
	raw := datatypes.JSON(`[{"src":"https://cdn.shopify.com/image1.jpg"},{"src":"https://cdn.shopify.com/image2.jpg"}]`)
	assert.Equal(t, "https://cdn.shopify.com/image1.jpg", firstImageURL(raw))
}

func TestFirstImageURL_Empty(t *testing.T) {
	assert.Equal(t, "", firstImageURL(datatypes.JSON("")))
}

func TestFirstImageURL_EmptyArray(t *testing.T) {
	assert.Equal(t, "", firstImageURL(datatypes.JSON("[]")))
}

// ── buildMessages Tests ──────────────────────────────────────────────────────

func TestBuildMessages_FirstMessage(t *testing.T) {
	agent := &SalesAgent{}
	sess := &ChatSession{
		ShopID:         "shop1",
		CustomerID:     "cust1",
		MessageHistory: []service.ChatMessage{},
	}
	msgs := agent.buildMessages(sess, "Hello!", nil)
	require.True(t, len(msgs) >= 2) // system + user
	assert.Equal(t, "system", msgs[0].Role)
	assert.Contains(t, msgs[0].Content, "AI shopping assistant")
	assert.Equal(t, "user", msgs[len(msgs)-1].Role)
	assert.Equal(t, "Hello!", msgs[len(msgs)-1].Content)
}

func TestBuildMessages_WithHistory(t *testing.T) {
	agent := &SalesAgent{}
	sess := &ChatSession{
		ShopID:     "shop1",
		CustomerID: "cust1",
		MessageHistory: []service.ChatMessage{
			{Role: "user", Content: "Hi"},
			{Role: "assistant", Content: "Hello! How can I help?"},
		},
	}
	msgs := agent.buildMessages(sess, "Show me dresses", nil)
	require.True(t, len(msgs) >= 4) // system + history(2) + new user
	assert.Equal(t, "system", msgs[0].Role)
	assert.Equal(t, "user", msgs[1].Role)
	assert.Equal(t, "Hi", msgs[1].Content)
	assert.Equal(t, "assistant", msgs[2].Role)
	assert.Equal(t, "user", msgs[3].Role)
	assert.Equal(t, "Show me dresses", msgs[3].Content)
}

func TestBuildMessages_DuplicateUserMessage(t *testing.T) {
	// If the last message in history is the same user message, don't duplicate
	agent := &SalesAgent{}
	sess := &ChatSession{
		ShopID:     "shop1",
		CustomerID: "cust1",
		MessageHistory: []service.ChatMessage{
			{Role: "user", Content: "Show me dresses"},
		},
	}
	msgs := agent.buildMessages(sess, "Show me dresses", nil)
	// Only system + 1 existing user, no duplicate added
	assert.Equal(t, "system", msgs[0].Role)
	assert.Equal(t, "user", msgs[1].Role)
	assert.Len(t, msgs, 2)
}

func TestBuildMessages_WithToolHistory(t *testing.T) {
	agent := &SalesAgent{}
	sess := &ChatSession{
		ShopID:     "shop1",
		CustomerID: "cust1",
		MessageHistory: []service.ChatMessage{
			{Role: "user", Content: "blue dress?"},
			{Role: "assistant", Content: "<tool>CALL</tool><name>search_products</name><args>{\"query\":\"blue dress\"}</args>"},
			{Role: "tool", Content: `{"products":[{"id":"1","title":"Blue Dress","price":"$49.99"}]}`},
		},
	}
	msgs := agent.buildMessages(sess, "I like it!", nil)
	require.True(t, len(msgs) >= 5) // system + history(3) + new user
	assert.Equal(t, "user", msgs[4].Role)
	assert.Equal(t, "I like it!", msgs[4].Content)
}

// ── buildSystemBlock Tests ───────────────────────────────────────────────────

func TestBuildSystemBlock_ContainsKeywords(t *testing.T) {
	block := buildSystemBlock()
	assert.Contains(t, block, "AI shopping assistant")
	assert.Contains(t, block, "search_products")
	assert.Contains(t, block, "generate_link")
	assert.Contains(t, block, "generate_discount")
	assert.Contains(t, block, "get_session")
}

func TestBuildSystemBlock_ContainsExamples(t *testing.T) {
	block := buildSystemBlock()
	assert.Contains(t, block, "Summer Blue Dress")
	assert.Contains(t, block, "VELA-SAVE15")
	assert.Contains(t, block, "purple leather jacket")
}

// ── Session Tests (miniredis) ────────────────────────────────────────────────

func setupSessionTest(t *testing.T) (*SalesAgent, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	cache, err := service.NewCacheService(context.Background(), "redis://"+mr.Addr())
	require.NoError(t, err)
	t.Cleanup(func() { cache.Close() })

	agent := &SalesAgent{cache: cache}
	return agent, mr
}

func TestGetAgentSession_New(t *testing.T) {
	agent, _ := setupSessionTest(t)
	sess, err := agent.getAgentSession(context.Background(), "shop1", "cust1")
	require.NoError(t, err)
	assert.Equal(t, "shop1", sess.ShopID)
	assert.Equal(t, "cust1", sess.CustomerID)
	assert.Equal(t, "greeting", sess.Stage)
	assert.Len(t, sess.MessageHistory, 0)
}

func TestSaveAndLoadSession(t *testing.T) {
	agent, _ := setupSessionTest(t)

	// Save
	sess := &ChatSession{
		ShopID:     "shop1",
		CustomerID: "cust1",
		Stage:      "recommendation",
		Preferences: map[string]string{"size": "M"},
		MessageHistory: []service.ChatMessage{
			{Role: "user", Content: "blue dress?"},
			{Role: "assistant", Content: "Yes! We have the Summer Blue Dress."},
		},
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}
	err := agent.saveAgentSession(context.Background(), sess)
	require.NoError(t, err)

	// Load back
	loaded, err := agent.getAgentSession(context.Background(), "shop1", "cust1")
	require.NoError(t, err)
	assert.Equal(t, "recommendation", loaded.Stage)
	assert.Equal(t, "M", loaded.Preferences["size"])
	assert.Len(t, loaded.MessageHistory, 2)
	assert.Equal(t, "blue dress?", loaded.MessageHistory[0].Content)
}

func TestSessionIsolation(t *testing.T) {
	// Different shops/customers should have isolated sessions
	agent, _ := setupSessionTest(t)

	sess1 := &ChatSession{
		ShopID:     "shop1",
		CustomerID: "cust1",
		Stage:      "greeting",
		Preferences: map[string]string{"color": "blue"},
	}
	err := agent.saveAgentSession(context.Background(), sess1)
	require.NoError(t, err)

	sess2 := &ChatSession{
		ShopID:     "shop1",
		CustomerID: "cust2",
		Stage:      "recommendation",
		Preferences: map[string]string{"color": "red"},
	}
	err = agent.saveAgentSession(context.Background(), sess2)
	require.NoError(t, err)

	loaded1, _ := agent.getAgentSession(context.Background(), "shop1", "cust1")
	loaded2, _ := agent.getAgentSession(context.Background(), "shop1", "cust2")

	assert.Equal(t, "blue", loaded1.Preferences["color"])
	assert.Equal(t, "red", loaded2.Preferences["color"])
	assert.Equal(t, "greeting", loaded1.Stage)
	assert.Equal(t, "recommendation", loaded2.Stage)
}

func TestSaveSession_TrimsHistory(t *testing.T) {
	agent, _ := setupSessionTest(t)

	sess := &ChatSession{
		ShopID:     "shop1",
		CustomerID: "cust1",
	}
	// Add 25 messages (exceeds maxHistoryMessages=20)
	for i := 0; i < 25; i++ {
		sess.MessageHistory = append(sess.MessageHistory,
			service.ChatMessage{Role: "user", Content: "msg"},
			service.ChatMessage{Role: "assistant", Content: "reply"},
		)
	}
	// 25 * 2 = 50 messages
	err := agent.saveAgentSession(context.Background(), sess)
	require.NoError(t, err)

	loaded, _ := agent.getAgentSession(context.Background(), "shop1", "cust1")
	assert.LessOrEqual(t, len(loaded.MessageHistory), maxHistoryMessages)
}

func TestGetAgentSession_InvalidJSON(t *testing.T) {
	agent, mr := setupSessionTest(t)
	// Inject invalid JSON in Redis
	key := "chat_session:shop1:cust1"
	mr.Set(key, "not-valid-json")

	sess, err := agent.getAgentSession(context.Background(), "shop1", "cust1")
	require.NoError(t, err)
	// Should return fresh session on invalid JSON
	assert.Equal(t, "greeting", sess.Stage)
	assert.Len(t, sess.MessageHistory, 0)
}

// ── SalesAgent Creation ──────────────────────────────────────────────────────

func TestNewSalesAgent(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	cache, err := service.NewCacheService(context.Background(), "redis://localhost:6379")
	if err != nil {
		t.Skip("no redis available for constructor test")
	}
	llmRouter := service.NewLLMRouter(db, cache, map[string]string{"deepseek": "test-key"})
	agent := NewSalesAgent(db, cache, llmRouter)
	assert.NotNil(t, agent)
	assert.NotNil(t, agent.httpClient)
	assert.Equal(t, 10*time.Second, agent.httpClient.Timeout)
}

// ── Integration: ReAct Loop Structure ────────────────────────────────────────

func TestBuildSystemBlock_Length(t *testing.T) {
	block := buildSystemBlock()
	// System prompt should be substantial (has instructions + examples)
	assert.True(t, len(block) > 500, "system block should be substantial, got %d chars", len(block))
}

// ── Marshal / Unmarshal Round-trip ────────────────────────────────────────────

func TestChatSessionJSONRoundTrip(t *testing.T) {
	original := &ChatSession{
		ShopID:     "shop1",
		CustomerID: "cust1",
		Stage:      "recommendation",
		Preferences: map[string]string{
			"size":  "L",
			"color": "navy",
		},
		DiscussedProducts: []string{"prod_1", "prod_2"},
		MessageHistory: []service.ChatMessage{
			{Role: "user", Content: "Hi"},
			{Role: "assistant", Content: "Hello!"},
		},
		CreatedAt: time.Now().Truncate(time.Second),
		UpdatedAt: time.Now().Truncate(time.Second),
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var restored ChatSession
	err = json.Unmarshal(data, &restored)
	require.NoError(t, err)

	assert.Equal(t, original.ShopID, restored.ShopID)
	assert.Equal(t, original.CustomerID, restored.CustomerID)
	assert.Equal(t, original.Stage, restored.Stage)
	assert.Equal(t, original.Preferences, restored.Preferences)
	assert.Equal(t, original.DiscussedProducts, restored.DiscussedProducts)
	assert.Len(t, restored.MessageHistory, 2)
}

func TestFewShotExample_AllHaveRequiredFields(t *testing.T) {
	for i, ex := range fewShotExamples {
		assert.NotEmpty(t, ex.Scenario, "example %d missing scenario", i)
		assert.NotEmpty(t, ex.UserMessage, "example %d missing user_message", i)
		assert.NotEmpty(t, ex.ExpectedReply, "example %d missing expected_reply", i)
	}
}

// ── Edge Cases (post-review fixes) ─────────────────────────────────────────

func TestFormatVariantPrices_UnsortedInput(t *testing.T) {
	// Regression: unsorted prices should be sorted in output
	variants := []shopifyVariant{
		{ID: 1, Title: "XL", Price: "50.00"},
		{ID: 2, Title: "S", Price: "10.00"},
		{ID: 3, Title: "M", Price: "25.00"},
	}
	assert.Equal(t, "$10.00 – $50.00", formatVariantPrices(variants))
}

func TestFormatVariantPrices_NonNumericPrice(t *testing.T) {
	// Graceful fallback for malformed prices
	variants := []shopifyVariant{
		{ID: 1, Title: "S", Price: "N/A"},
		{ID: 2, Title: "M", Price: "19.99"},
	}
	result := formatVariantPrices(variants)
	// "N/A" parses as 0, so $0 comes first
	assert.Contains(t, result, "$")
}

func TestExtractShopifyNumericID_Empty(t *testing.T) {
	assert.Equal(t, "", extractShopifyNumericID(""))
}

func TestExtractShopifyNumericID_OnlySlash(t *testing.T) {
	assert.Equal(t, "", extractShopifyNumericID("/"))
}

func TestSearchProducts_EmptyQuery(t *testing.T) {
	// Verify empty query returns empty results, not all products
	agent := &SalesAgent{}
	args := map[string]interface{}{
		"shop_id": "shop1",
		"query":   "",
	}
	result, err := agent.searchProducts(context.Background(), args)
	require.NoError(t, err)
	m := result.(map[string]interface{})
	products := m["products"].([]ProductResult)
	assert.Len(t, products, 0)
}

// ── Signal Extraction Tests (VCI) ─────────────────────────────────────────

func TestExtractSignals_PriceObjection(t *testing.T) {
	agent := &SalesAgent{}
	sess := &ChatSession{ShopID: "shop1", CustomerID: "cust1"}
	signals := agent.extractSignals(sess, "that's too expensive for me", "I understand!")
	found := false
	for _, s := range signals {
		if s.Type == "price_objection" {
			found = true
			break
		}
	}
	assert.True(t, found, "should detect price objection")
}

func TestExtractSignals_PriceObjectionWithDiscount(t *testing.T) {
	agent := &SalesAgent{}
	sess := &ChatSession{ShopID: "shop1", CustomerID: "cust1"}
	signals := agent.extractSignals(sess, "do you have any discount code?", "Here's your code: VELA-SAVE15")
	hasPrice := false
	hasDiscount := false
	for _, s := range signals {
		if s.Type == "price_objection" {
			hasPrice = true
		}
		if s.Type == "discount_request" {
			hasDiscount = true
		}
	}
	assert.True(t, hasPrice || hasDiscount, "should detect price/discount signal")
}

func TestExtractSignals_PurchaseIntent(t *testing.T) {
	agent := &SalesAgent{}
	sess := &ChatSession{ShopID: "shop1", CustomerID: "cust1"}
	signals := agent.extractSignals(sess, "I'll take it in M", "Great! Here's your link... utm_source=vela_ai_chat")
	found := false
	for _, s := range signals {
		if s.Type == "purchase_signal" {
			found = true
			break
		}
	}
	assert.True(t, found, "should detect purchase intent")
}

func TestExtractSignals_PurchaseIntentNoLink(t *testing.T) {
	agent := &SalesAgent{}
	sess := &ChatSession{ShopID: "shop1", CustomerID: "cust1"}
	signals := agent.extractSignals(sess, "I want to buy this", "Let me check stock first.")
	hasPurchase := false
	hasNoPurchase := false
	for _, s := range signals {
		if s.Type == "purchase_signal" {
			hasPurchase = true
		}
		if s.Type == "intent_high_no_purchase" {
			hasNoPurchase = true
		}
	}
	assert.True(t, hasPurchase, "should detect purchase intent")
	assert.True(t, hasNoPurchase, "should flag high intent without purchase link")
}

func TestExtractSignals_SizePreference(t *testing.T) {
	agent := &SalesAgent{}
	sess := &ChatSession{ShopID: "shop1", CustomerID: "cust1"}
	signals := agent.extractSignals(sess, "My size is M, do you have it?", "Yes, we have M in stock!")
	found := false
	for _, s := range signals {
		if s.Type == "size_preference" && s.Metadata["size"] == "M" {
			found = true
			break
		}
	}
	assert.True(t, found, "should extract size preference")
}

func TestExtractSignals_SizeNumeric(t *testing.T) {
	agent := &SalesAgent{}
	sess := &ChatSession{ShopID: "shop1", CustomerID: "cust1"}
	signals := agent.extractSignals(sess, "I wear size 8", "Got it!")
	for _, s := range signals {
		if s.Type == "size_preference" {
			assert.Equal(t, "8", s.Metadata["size"])
			return
		}
	}
	t.Error("should detect numeric size preference")
}

func TestExtractSignals_ColorPreference(t *testing.T) {
	agent := &SalesAgent{}
	sess := &ChatSession{ShopID: "shop1", CustomerID: "cust1"}
	signals := agent.extractSignals(sess, "Do you have it in blue?", "Yes, we have blue!")
	for _, s := range signals {
		if s.Type == "color_preference" {
			assert.Contains(t, s.Metadata["color"], "blue")
			return
		}
	}
	t.Error("should detect color preference")
}

func TestExtractSignals_NoSignals(t *testing.T) {
	agent := &SalesAgent{}
	sess := &ChatSession{ShopID: "shop1", CustomerID: "cust1"}
	signals := agent.extractSignals(sess, "Hi, how are you?", "I'm doing great! How can I help?")
	assert.Len(t, signals, 0, "greeting should not trigger signals")
}

func TestIsValidSize_Standard(t *testing.T) {
	assert.True(t, isValidSize("M"))
	assert.True(t, isValidSize("XL"))
	assert.True(t, isValidSize("XXL"))
	assert.False(t, isValidSize("hello"))
	assert.False(t, isValidSize(""))
}

func TestIsValidSize_Numeric(t *testing.T) {
	assert.True(t, isValidSize("8"))
	assert.True(t, isValidSize("0"))
	assert.True(t, isValidSize("24"))
	assert.False(t, isValidSize("99"))
}

func TestExtractColorPreference_DirectMatch(t *testing.T) {
	assert.Equal(t, "navy", extractColorPreference("I want a navy dress"))
	assert.Equal(t, "black", extractColorPreference("looking for black shoes"))
	assert.Equal(t, "", extractColorPreference("hello there"))
}

// ── Error-Path Tests (from backend review) ────────────────────────────────

func TestSearchProducts_NilDB_ReturnsError(t *testing.T) {
	agent := &SalesAgent{} // db is nil
	args := map[string]interface{}{
		"shop_id": "shop1",
		"query":   "dress",
	}
	_, err := agent.searchProducts(context.Background(), args)
	require.Error(t, err, "nil db should return error, not empty results")
	assert.Contains(t, err.Error(), "search products")
}

func TestSearchProducts_EmptyQuery_ReturnsEmptyNotError(t *testing.T) {
	agent := &SalesAgent{}
	args := map[string]interface{}{
		"shop_id": "shop1",
		"query":   "",
	}
	result, err := agent.searchProducts(context.Background(), args)
	require.NoError(t, err) // empty query is not a DB error
	m := result.(map[string]interface{})
	assert.Len(t, m["products"].([]ProductResult), 0)
}

func TestGenerateDiscountCode_Format(t *testing.T) {
	for i := 0; i < 100; i++ {
		code := generateDiscountCode()
		assert.True(t, strings.HasPrefix(code, "VELA-"), "code should start with VELA-: got %s", code)
		// VELA- + 8 hex chars = 13 total
		assert.Len(t, code, 13, "expected VELA-XXXXXXXX format: got %s", code)
		// Suffix should be uppercase hex
		suffix := code[5:]
		assert.Regexp(t, `^[0-9A-F]{8}$`, suffix, "suffix should be 8 hex chars: got %s", suffix)
	}
}

func TestGenerateDiscountCode_Uniqueness(t *testing.T) {
	codes := make(map[string]bool)
	for i := 0; i < 500; i++ {
		code := generateDiscountCode()
		assert.False(t, codes[code], "collision detected: %s", code)
		codes[code] = true
	}
}

func TestIsValidSize_ShoeSize(t *testing.T) {
	assert.True(t, isValidSize("8.5"))
	assert.True(t, isValidSize("10.0"))
	assert.True(t, isValidSize("5.5"))
	assert.True(t, isValidSize("16.0")) // upper bound (inclusive)
	assert.False(t, isValidSize("4.5")) // below shoe range (5-16)
	assert.False(t, isValidSize("17.0")) // above shoe range
}

func TestIsValidSize_Lowercase(t *testing.T) {
	assert.True(t, isValidSize("m"))
	assert.True(t, isValidSize("xl"))
	assert.True(t, isValidSize("8"))
}

func TestIsValidSize_Whitespace(t *testing.T) {
	assert.True(t, isValidSize("  M  "))
	assert.True(t, isValidSize(" 8 "))
}

func TestGetSystemBlock_MatchesBuildSystemBlock(t *testing.T) {
	cached := getSystemBlock()
	built := buildSystemBlock()
	assert.Equal(t, built, cached, "cached system block should match freshly built one")
	assert.Equal(t, cached, getSystemBlock(), "subsequent calls should return same instance")
}

func TestGetSystemBlock_NotEmpty(t *testing.T) {
	assert.NotEmpty(t, getSystemBlock())
	assert.Contains(t, getSystemBlock(), "AI shopping assistant")
}

func TestDeleteShopifyPriceRule_Exists(t *testing.T) {
	// Verify the method compiles and is callable (no-op with invalid URL in test)
	agent := &SalesAgent{httpClient: &http.Client{Timeout: 1 * time.Second}}
	// Should not panic even with invalid domain
	assert.NotPanics(t, func() {
		agent.deleteShopifyPriceRule(context.Background(), "invalid.domain", "fake-token", "123")
	})
}

func TestFallbackDiscount_ReturnsHardcodedCode(t *testing.T) {
	agent := &SalesAgent{}
	result, err := agent.fallbackDiscount(context.Background())
	require.NoError(t, err)
	m := result.(map[string]interface{})
	assert.Equal(t, "VELA-SAVE15", m["code"])
	assert.Equal(t, "15%", m["amount"])
	assert.NotEmpty(t, m["expires_at"])
}

// ── P1 Fix Tests (from formal review) ──────────────────────────────────────

func TestStripXMLTags_PreservesNormalText(t *testing.T) {
	assert.Equal(t, "I <3 this dress", stripXMLTags("I <3 this dress"))
	assert.Equal(t, "price < $50", stripXMLTags("price < $50"))
}

func TestStripXMLTags_RemovesKnownTags(t *testing.T) {
	// Reply tag removal
	assert.Equal(t, "Hello world", stripXMLTags("<reply>Hello world</reply>"))
	// Mixed known tags with normal text (last-resort fallback scenario)
	assert.Equal(t, "That sounds great!", stripXMLTags("<reply>That sounds great!</reply>"))
	// Tool output with content between tags
	assert.Equal(t, "CALLsearch_products", stripXMLTags("<tool>CALL</tool><name>search_products</name>"))
}

func TestGetFloatArg(t *testing.T) {
	assert.Equal(t, 50.0, getFloatArg(map[string]interface{}{"max_price": 50.0}, "max_price"))
	assert.Equal(t, 0.0, getFloatArg(map[string]interface{}{"max_price": "invalid"}, "max_price"))
	assert.Equal(t, 25.5, getFloatArg(map[string]interface{}{"max_price": "25.5"}, "max_price"))
	assert.Equal(t, 0.0, getFloatArg(map[string]interface{}{}, "max_price"))
}

func TestFilterByMaxPrice(t *testing.T) {
	variants := []shopifyVariant{
		{ID: 1, Price: "10.00", InventoryQuantity: 5},
		{ID: 2, Price: "25.00", InventoryQuantity: 3},
		{ID: 3, Price: "50.00", InventoryQuantity: 2},
		{ID: 4, Price: "75.00", InventoryQuantity: 1},
	}
	filtered := filterByMaxPrice(variants, 30.0)
	assert.Len(t, filtered, 2)
	assert.Equal(t, int64(1), filtered[0].ID)
	assert.Equal(t, int64(2), filtered[1].ID)
}

func TestFilterByMaxPrice_AllAbove(t *testing.T) {
	variants := []shopifyVariant{
		{ID: 1, Price: "100.00", InventoryQuantity: 1},
	}
	assert.Len(t, filterByMaxPrice(variants, 50.0), 0)
}

func TestAppendUnique(t *testing.T) {
	slice := []string{"a", "b"}
	assert.Equal(t, []string{"a", "b", "c"}, appendUnique(slice, "c"))
	assert.Equal(t, []string{"a", "b"}, appendUnique(slice, "a")) // no duplicate
}

func TestExtractShopifyNumericID_EdgeCases(t *testing.T) {
	assert.Equal(t, "123", extractShopifyNumericID("gid://shopify/Product/123"))
	assert.Equal(t, "abc", extractShopifyNumericID("products/abc"))
	assert.Equal(t, "", extractShopifyNumericID(""))
	assert.Equal(t, "", extractShopifyNumericID("/"))
	assert.Equal(t, "noid", extractShopifyNumericID("noid")) // no slash
}

// ── Benchmark ────────────────────────────────────────────────────────────────

func BenchmarkParseToolCall_Valid(b *testing.B) {
	output := `<tool>CALL</tool><name>search_products</name><args>{"query":"blue dress","color":"blue"}</args>`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parseToolCall(output)
	}
}

func BenchmarkBuildSystemBlock(b *testing.B) {
	for i := 0; i < b.N; i++ {
		buildSystemBlock()
	}
}

func BenchmarkBuildMessages(b *testing.B) {
	agent := &SalesAgent{}
	sess := &ChatSession{
		ShopID:     "shop1",
		CustomerID: "cust1",
		MessageHistory: []service.ChatMessage{
			{Role: "user", Content: "Hi"},
			{Role: "assistant", Content: "Hello!"},
			{Role: "user", Content: "Show me dresses"},
			{Role: "assistant", Content: "What color?"},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = agent.buildMessages(sess, "Blue please", nil)
	}
}
