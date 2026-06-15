package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/platform/insight"
	"github.com/JingxuanC/vela-engine/internal/service"
)

// ── Test Helpers ────────────────────────────────────────────────────────────────

func newAdminChatHandler() (*AdminChatHandler, *gorm.DB) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		panic(err)
	}
	db.Exec(`CREATE TABLE IF NOT EXISTS shops (id TEXT PRIMARY KEY, shop_domain TEXT)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS synced_products (id TEXT PRIMARY KEY, shop_id TEXT, platform_id TEXT, title TEXT, description TEXT, vendor TEXT, product_type TEXT, tags TEXT, status TEXT DEFAULT 'active', created_at DATETIME, updated_at DATETIME)`)
	db.Exec(`INSERT OR IGNORE INTO shops (id, shop_domain) VALUES (?, ?)`, "00000000-0000-0000-0000-000000000001", "test.myshopify.com")

	injector := insight.NewContextInjector()
	h := NewAdminChatHandler(db, service.NewLLMRouter(db, nil, map[string]string{"deepseek": "test-key"}), injector)
	return h, db
}

// ── Ask Endpoint Tests ──────────────────────────────────────────────────────────

func TestAdminChatAsk_MissingShopID(t *testing.T) {
	h, _ := newAdminChatHandler()
	body := strings.NewReader(`{"question":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/chat/ask", body)
	rec := httptest.NewRecorder()
	h.Ask(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdminChatAsk_MissingQuestion(t *testing.T) {
	h, _ := newAdminChatHandler()
	body := strings.NewReader(`{"shop_id":"00000000-0000-0000-0000-000000000001"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/chat/ask", body)
	rec := httptest.NewRecorder()
	h.Ask(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdminChatAsk_EmptyQuestion(t *testing.T) {
	h, _ := newAdminChatHandler()
	body := strings.NewReader(`{"shop_id":"00000000-0000-0000-0000-000000000001","question":"  "}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/chat/ask", body)
	rec := httptest.NewRecorder()
	h.Ask(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdminChatAsk_InvalidJSON(t *testing.T) {
	h, _ := newAdminChatHandler()
	body := strings.NewReader(`{bad json`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/chat/ask", body)
	rec := httptest.NewRecorder()
	h.Ask(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdminChatAsk_InvalidShopID(t *testing.T) {
	h, _ := newAdminChatHandler()
	body := strings.NewReader(`{"shop_id":"not-a-valid-shop","question":"help"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/chat/ask", body)
	rec := httptest.NewRecorder()
	h.Ask(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdminChatAsk_DBNil(t *testing.T) {
	h := &AdminChatHandler{db: nil}
	body := strings.NewReader(`{"shop_id":"test","question":"help"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/chat/ask", body)
	rec := httptest.NewRecorder()
	h.Ask(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// ── AskSummary Endpoint Tests ───────────────────────────────────────────────────

func TestAdminChatAskSummary_MissingShopID(t *testing.T) {
	h, _ := newAdminChatHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/chat/summary", nil)
	rec := httptest.NewRecorder()
	h.AskSummary(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdminChatAskSummary_DBNil(t *testing.T) {
	h := &AdminChatHandler{db: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/chat/summary?shop_id=test", nil)
	rec := httptest.NewRecorder()
	h.AskSummary(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestAdminChatAskSummary_InvalidShopID(t *testing.T) {
	h, _ := newAdminChatHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/chat/summary?shop_id=invalid", nil)
	rec := httptest.NewRecorder()
	h.AskSummary(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ── Context Building ────────────────────────────────────────────────────────────

func TestBuildContext_QuickCheck(t *testing.T) {
	h, _ := newAdminChatHandler()
	h.injector.Register(&mockFetcher{name: "shop_snapshot", output: "[店铺快照] 退货率:8.2% 订单:128"})

	ctx := h.buildContext(context.Background(), "test-shop", "how's business today?", sceneQuickCheck)
	// QuickCheck returns snapshot
	assert.NotEmpty(t, ctx)
	assert.Contains(t, ctx, "店铺快照")
}

func TestBuildContext_RootCause(t *testing.T) {
	h, _ := newAdminChatHandler()
	h.injector.Register(&mockFetcher{name: "shop_snapshot", output: "[店铺快照] snapshot data"})
	h.injector.Register(&mockFetcher{name: "return_insight", output: "[退货原因] size:45次"})

	ctx := h.buildContext(context.Background(), "test-shop", "why are returns up?", sceneRootCause)
	assert.Contains(t, ctx, "退货原因")
	assert.Contains(t, ctx, "snapshot")
}

func TestBuildContext_DecisionSupport(t *testing.T) {
	h, _ := newAdminChatHandler()
	h.injector.Register(&mockFetcher{name: "shop_snapshot", output: "[店铺快照] data"})
	h.injector.Register(&mockFetcher{name: "trend_data", output: "[趋势] dresses +85%"})
	h.injector.Register(&mockFetcher{name: "return_insight", output: "[退货原因] data"})

	ctx := h.buildContext(context.Background(), "test-shop", "should I raise prices?", sceneDecisionSupport)
	assert.Contains(t, ctx, "趋势")
	assert.Contains(t, ctx, "退货原因")
}

// ── Daily Summary ───────────────────────────────────────────────────────────────

func TestBuildDailySummary_NoInjector(t *testing.T) {
	h, _ := newAdminChatHandler()
	h.injector = nil
	summary := h.buildDailySummary(context.Background(), "test")
	assert.Nil(t, summary)
}

func TestBuildDailySummary_NoDB(t *testing.T) {
	h := &AdminChatHandler{db: nil, injector: insight.NewContextInjector()}
	summary := h.buildDailySummary(context.Background(), "test")
	assert.Nil(t, summary)
}

// ── System Prompt ───────────────────────────────────────────────────────────────

func TestAdminChatSystemPrompt_Contains(t *testing.T) {
	prompt := buildAdminChatSystemPrompt("friendly")
	assert.Contains(t, prompt, "Vela")
	assert.Contains(t, prompt, "Shopify")
	assert.Contains(t, prompt, "Sidekick")
	assert.Contains(t, prompt, "Store Data")
	assert.Contains(t, prompt, "退货原因")
}

// ── Response DTO ────────────────────────────────────────────────────────────────

func TestAdminChatResponse_Serialization(t *testing.T) {
	resp := AdminChatResponse{
		Success: true,
		Answer:  "Your return rate is normal.",
		DailySummary: &DailySummary{
			ReturnRate:  8.2,
			OrderCount:  128,
			Alerts:      []string{"Low stock: Cotton Tee L"},
			GeneratedAt: "2026-06-10T08:00:00Z",
		},
		SuggestedQuestions: []string{"Q1", "Q2"},
	}

	b, err := json.Marshal(resp)
	require.NoError(t, err)

	var parsed AdminChatResponse
	require.NoError(t, json.Unmarshal(b, &parsed))
	assert.True(t, parsed.Success)
	assert.Equal(t, "Your return rate is normal.", parsed.Answer)
	assert.Equal(t, 8.2, parsed.DailySummary.ReturnRate)
	assert.Equal(t, 128, parsed.DailySummary.OrderCount)
	assert.Len(t, parsed.SuggestedQuestions, 2)
}

// ── StreamAsk Validation Tests ──────────────────────────────────────────────────

func TestAdminChatStreamAsk_MissingShopID(t *testing.T) {
	h, _ := newAdminChatHandler()
	body := strings.NewReader(`{"question":"help"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/chat/stream", body)
	rec := httptest.NewRecorder()
	h.StreamAsk(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdminChatStreamAsk_EmptyQuestion(t *testing.T) {
	h, _ := newAdminChatHandler()
	body := strings.NewReader(`{"shop_id":"00000000-0000-0000-0000-000000000001","question":"  "}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/chat/stream", body)
	rec := httptest.NewRecorder()
	h.StreamAsk(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdminChatStreamAsk_DBNil(t *testing.T) {
	h := &AdminChatHandler{db: nil}
	body := strings.NewReader(`{"shop_id":"test","question":"help"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/chat/stream", body)
	rec := httptest.NewRecorder()
	h.StreamAsk(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestAdminChatStreamAsk_InvalidJSON(t *testing.T) {
	h, _ := newAdminChatHandler()
	body := strings.NewReader(`{bad`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/chat/stream", body)
	rec := httptest.NewRecorder()
	h.StreamAsk(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ── extractRAGQuery Tests ──────────────────────────────────────────────────────

func TestExtractRAGQuery_StripsQuestionWords(t *testing.T) {
	result := extractRAGQuery("should I raise prices on summer dresses?")
	assert.NotContains(t, result, "should ")
	assert.NotContains(t, result, " I ")
	assert.Contains(t, result, "raise prices")
	assert.Contains(t, result, "summer dresses")
}

func TestExtractRAGQuery_PreservesKeywords(t *testing.T) {
	result := extractRAGQuery("why are returns up on cotton tee?")
	assert.Contains(t, result, "returns")
	assert.Contains(t, result, "cotton tee")
	assert.NotContains(t, result, "why")
}

// ── brandVoiceStyle Tests ───────────────────────────────────────────────────────

func TestBrandVoiceStyle(t *testing.T) {
	t.Run("friendly", func(t *testing.T) {
		result := brandVoiceStyle("friendly")
		assert.Contains(t, result, "warm")
		assert.Contains(t, result, "conversational")
	})

	t.Run("professional", func(t *testing.T) {
		result := brandVoiceStyle("professional")
		assert.Contains(t, result, "formal")
		assert.Contains(t, result, "business-appropriate")
	})

	t.Run("luxury", func(t *testing.T) {
		result := brandVoiceStyle("luxury")
		assert.Contains(t, result, "elegant")
		assert.Contains(t, result, "sophisticated")
	})

	t.Run("playful", func(t *testing.T) {
		result := brandVoiceStyle("playful")
		assert.Contains(t, result, "fun")
		assert.Contains(t, result, "energetic")
	})

	t.Run("unknown_value_falls_back_to_friendly", func(t *testing.T) {
		result := brandVoiceStyle("invalid")
		assert.Contains(t, result, "warm")
		assert.Contains(t, result, "conversational")
	})

	t.Run("empty_string_falls_back_to_friendly", func(t *testing.T) {
		result := brandVoiceStyle("")
		assert.Contains(t, result, "warm")
		assert.Contains(t, result, "conversational")
	})
}

// ── buildAdminChatSystemPrompt Tests ────────────────────────────────────────────

func TestBuildAdminChatSystemPrompt(t *testing.T) {
	t.Run("contains_all_core_sections", func(t *testing.T) {
		prompt := buildAdminChatSystemPrompt("friendly")
		// Core identity
		assert.Contains(t, prompt, "Vela")
		assert.Contains(t, prompt, "Shopify")
		// Sections
		assert.Contains(t, prompt, "Capabilities")
		assert.Contains(t, prompt, "Tone")
		assert.Contains(t, prompt, "Guidelines")
		assert.Contains(t, prompt, "Data Sections")
		assert.Contains(t, prompt, "What NOT to do")
		// Data section references
		assert.Contains(t, prompt, "店铺快照")
		assert.Contains(t, prompt, "退货原因")
		assert.Contains(t, prompt, "销量")
		assert.Contains(t, prompt, "趋势数据")
		assert.Contains(t, prompt, "语义上下文")
		// Guardrails
		assert.Contains(t, prompt, "Sidekick")
		assert.Contains(t, prompt, "Don't make up numbers")
	})

	t.Run("different_brand_voice_produces_different_tone", func(t *testing.T) {
		friendly := buildAdminChatSystemPrompt("friendly")
		professional := buildAdminChatSystemPrompt("professional")
		luxury := buildAdminChatSystemPrompt("luxury")
		playful := buildAdminChatSystemPrompt("playful")

		// Each prompt embeds the tone instruction from brandVoiceStyle
		assert.Contains(t, friendly, "warm")
		assert.Contains(t, friendly, "conversational")

		assert.Contains(t, professional, "formal")
		assert.Contains(t, professional, "business-appropriate")

		assert.Contains(t, luxury, "elegant")
		assert.Contains(t, luxury, "sophisticated")

		assert.Contains(t, playful, "fun")
		assert.Contains(t, playful, "energetic")

		// Prompts with different voices must not be identical
		assert.NotEqual(t, friendly, professional)
		assert.NotEqual(t, friendly, luxury)
		assert.NotEqual(t, friendly, playful)
	})

	t.Run("unknown_brand_voice_uses_friendly_tone_in_prompt", func(t *testing.T) {
		prompt := buildAdminChatSystemPrompt("unknown_value")
		assert.Contains(t, prompt, "warm")
		assert.Contains(t, prompt, "conversational")
		// Core structure still present
		assert.Contains(t, prompt, "Vela")
		assert.Contains(t, prompt, "Capabilities")
	})
}

// ── getBrandVoice Tests ─────────────────────────────────────────────────────────

func TestGetBrandVoice_DBNil(t *testing.T) {
	h := &AdminChatHandler{db: nil}
	voice := h.getBrandVoice(context.Background(), "any-shop")
	assert.Equal(t, "friendly", voice)
}

func TestGetBrandVoice_NoConfigRow(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	db.Exec(`CREATE TABLE IF NOT EXISTS sales_agent_configs (shop_id TEXT PRIMARY KEY, brand_voice TEXT)`)

	h := &AdminChatHandler{db: db}
	voice := h.getBrandVoice(context.Background(), "nonexistent-shop")
	assert.Equal(t, "friendly", voice)
}

func TestGetBrandVoice_EmptyBrandVoiceInDB(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	db.Exec(`CREATE TABLE IF NOT EXISTS sales_agent_configs (shop_id TEXT PRIMARY KEY, brand_voice TEXT)`)
	db.Exec(`INSERT INTO sales_agent_configs (shop_id, brand_voice) VALUES (?, ?)`, "shop-1", "")

	h := &AdminChatHandler{db: db}
	voice := h.getBrandVoice(context.Background(), "shop-1")
	assert.Equal(t, "friendly", voice)
}

func TestGetBrandVoice_ValidVoices(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	db.Exec(`CREATE TABLE IF NOT EXISTS sales_agent_configs (shop_id TEXT PRIMARY KEY, brand_voice TEXT)`)

	testCases := []struct {
		shopID     string
		brandVoice string
	}{
		{"shop-friendly", "friendly"},
		{"shop-professional", "professional"},
		{"shop-luxury", "luxury"},
		{"shop-playful", "playful"},
	}
	for _, tc := range testCases {
		db.Exec(`INSERT INTO sales_agent_configs (shop_id, brand_voice) VALUES (?, ?)`, tc.shopID, tc.brandVoice)
	}

	h := &AdminChatHandler{db: db}
	for _, tc := range testCases {
		t.Run(tc.brandVoice, func(t *testing.T) {
			voice := h.getBrandVoice(context.Background(), tc.shopID)
			assert.Equal(t, tc.brandVoice, voice)
		})
	}
}

func TestGetBrandVoice_InvalidDBValueFallsBack(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	db.Exec(`CREATE TABLE IF NOT EXISTS sales_agent_configs (shop_id TEXT PRIMARY KEY, brand_voice TEXT)`)
	db.Exec(`INSERT INTO sales_agent_configs (shop_id, brand_voice) VALUES (?, ?)`, "shop-evil", "evil")

	h := &AdminChatHandler{db: db}
	voice := h.getBrandVoice(context.Background(), "shop-evil")
	assert.Equal(t, "friendly", voice)
}

func TestGetBrandVoice_SQLInjectionSafety(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	db.Exec(`CREATE TABLE IF NOT EXISTS sales_agent_configs (shop_id TEXT PRIMARY KEY, brand_voice TEXT)`)
	db.Exec(`INSERT INTO sales_agent_configs (shop_id, brand_voice) VALUES (?, ?)`, "normal-shop", "luxury")
	db.Exec(`INSERT INTO sales_agent_configs (shop_id, brand_voice) VALUES (?, ?)`, "admin-shop", "professional")

	h := &AdminChatHandler{db: db}

	t.Run("union_based_injection", func(t *testing.T) {
		// Classic UNION injection attempt — parameterized query treats it as literal
		maliciousID := "' OR 1=1 UNION SELECT 'hacked' --"
		voice := h.getBrandVoice(context.Background(), maliciousID)
		// No row matches the literal string, so fallback to friendly
		assert.Equal(t, "friendly", voice)
	})

	t.Run("tautology_injection", func(t *testing.T) {
		// ' OR '1'='1 should not bypass the WHERE clause
		maliciousID := "' OR '1'='1"
		voice := h.getBrandVoice(context.Background(), maliciousID)
		assert.Equal(t, "friendly", voice, "tautology injection must not leak other rows")
	})

	t.Run("drop_table_attempt", func(t *testing.T) {
		maliciousID := "'; DROP TABLE sales_agent_configs; --"
		voice := h.getBrandVoice(context.Background(), maliciousID)
		assert.Equal(t, "friendly", voice)

		// Verify table integrity — table still exists and data is intact
		var count int64
		err := db.Table("sales_agent_configs").Count(&count).Error
		require.NoError(t, err, "table should still exist after injection attempt")
		assert.Equal(t, int64(2), count, "all rows should remain intact")
	})

	t.Run("normal_query_still_works_after_attacks", func(t *testing.T) {
		// After all injection attempts, normal queries must work correctly
		voice := h.getBrandVoice(context.Background(), "normal-shop")
		assert.Equal(t, "luxury", voice)
		voice2 := h.getBrandVoice(context.Background(), "admin-shop")
		assert.Equal(t, "professional", voice2)
	})

	t.Run("comment_escape_attempt", func(t *testing.T) {
		maliciousID := "admin-shop' --"
		voice := h.getBrandVoice(context.Background(), maliciousID)
		// Must NOT return admin-shop's voice — the comment should not strip the rest of the query
		assert.Equal(t, "friendly", voice)
	})
}
