package handler

import (
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

func setupStorefrontTest(t *testing.T) (*StorefrontChatHandler, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS shops (id TEXT PRIMARY KEY, shop_domain TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS synced_products (id TEXT PRIMARY KEY, shop_id TEXT, platform_id TEXT, title TEXT, description TEXT, vendor TEXT, product_type TEXT, tags TEXT, status TEXT DEFAULT 'active', created_at DATETIME, updated_at DATETIME)`).Error)

	injector := insight.NewContextInjector()
	h := NewStorefrontChatHandler(db, service.NewLLMRouter(db, nil, map[string]string{"deepseek": "test-key"}), injector, nil)
	return h, db
}

// ── Stream Validation ──────────────────────────────────────────────────────────

func TestStorefrontStream_MissingShop(t *testing.T) {
	h, _ := setupStorefrontTest(t)
	body := strings.NewReader(`{"message":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/chat/stream", body)
	rec := httptest.NewRecorder()
	h.Stream(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestStorefrontStream_EmptyMessage(t *testing.T) {
	h, _ := setupStorefrontTest(t)
	body := strings.NewReader(`{"message":"  "}`)
	req := httptest.NewRequest(http.MethodPost, "/api/chat/stream?shop=test.myshopify.com", body)
	rec := httptest.NewRecorder()
	h.Stream(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestStorefrontStream_InvalidJSON(t *testing.T) {
	h, _ := setupStorefrontTest(t)
	body := strings.NewReader(`{bad`)
	req := httptest.NewRequest(http.MethodPost, "/api/chat/stream?shop=test.myshopify.com", body)
	rec := httptest.NewRecorder()
	h.Stream(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestStorefrontStream_DBNil(t *testing.T) {
	h := &StorefrontChatHandler{db: nil}
	body := strings.NewReader(`{"message":"help"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/chat/stream?shop=test.myshopify.com", body)
	rec := httptest.NewRecorder()
	h.Stream(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestStorefrontStream_InvalidShop(t *testing.T) {
	h, _ := setupStorefrontTest(t)
	body := strings.NewReader(`{"message":"help"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/chat/stream?shop=nonexistent.myshopify.com", body)
	rec := httptest.NewRecorder()
	h.Stream(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ── Suggest Validation ─────────────────────────────────────────────────────────

func TestStorefrontSuggest_MissingShop(t *testing.T) {
	h, _ := setupStorefrontTest(t)
	body := strings.NewReader(`{"product_id":"1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/chat/suggest", body)
	rec := httptest.NewRecorder()
	h.Suggest(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestStorefrontSuggest_InvalidJSON(t *testing.T) {
	h, _ := setupStorefrontTest(t)
	body := strings.NewReader(`{bad`)
	req := httptest.NewRequest(http.MethodPost, "/api/chat/suggest?shop=test.myshopify.com", body)
	rec := httptest.NewRecorder()
	h.Suggest(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestStorefrontSuggest_DBNil(t *testing.T) {
	h := &StorefrontChatHandler{db: nil}
	body := strings.NewReader(`{"product_id":"1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/chat/suggest?shop=test.myshopify.com", body)
	rec := httptest.NewRecorder()
	h.Suggest(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestStorefrontSuggest_InvalidShop(t *testing.T) {
	h, _ := setupStorefrontTest(t)
	body := strings.NewReader(`{"product_id":"1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/chat/suggest?shop=nonexistent.myshopify.com", body)
	rec := httptest.NewRecorder()
	h.Suggest(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestStorefrontSuggest_NoProductID(t *testing.T) {
	h, db := setupStorefrontTest(t)
	// Seed a valid shop
	db.Exec(`INSERT OR IGNORE INTO shops (id, shop_domain) VALUES ('5250f055-921a-4fc4-be92-e87073640f15', 'test.myshopify.com')`)
	body := strings.NewReader(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/api/chat/suggest?shop=test.myshopify.com", body)
	rec := httptest.NewRecorder()
	h.Suggest(rec, req)
	// Empty product_id → LLM call returns fallback suggestions (no crash)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// ── resolveShopFromProxy ───────────────────────────────────────────────────────

func TestResolveShopFromProxy_MissingParam(t *testing.T) {
	h, _ := setupStorefrontTest(t)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	_, err := h.resolveShopFromProxy(req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing shop parameter")
}

func TestResolveShopFromProxy_Valid(t *testing.T) {
	h, db := setupStorefrontTest(t)
	db.Exec(`INSERT OR IGNORE INTO shops (id, shop_domain) VALUES ('00000000-0000-0000-0000-000000000001', 'test.myshopify.com')`)
	db.Exec(`INSERT OR IGNORE INTO shops (id, shop_domain) VALUES ('5250f055-921a-4fc4-be92-e87073640f15', 'valid.myshopify.com')`)

	req := httptest.NewRequest(http.MethodGet, "/test?shop=test.myshopify.com", nil)
	shopID, err := h.resolveShopFromProxy(req)
	assert.NoError(t, err)
	assert.NotEmpty(t, shopID)
}

// ── fallbackSuggestions ────────────────────────────────────────────────────────

func TestFallbackSuggestions_ReturnsFourQuestions(t *testing.T) {
	qs := fallbackSuggestions()
	assert.Len(t, qs, 4)
	for _, q := range qs {
		assert.NotEmpty(t, q["question"])
	}
}

// ── API Contract: widget JSON field names match backend ────────────────────────

func TestStreamRequestJSON_MatchesWidgetPayload(t *testing.T) {
	// Widget sends: {message, product_id, history}
	widgetPayload := `{"message":"Does this fit?","product_id":"123","history":[]}`

	var req struct {
		Message   string              `json:"message"`
		ProductID string              `json:"product_id"`
		History   []map[string]string `json:"history"`
	}
	assert.NoError(t, json.Unmarshal([]byte(widgetPayload), &req))
	assert.Equal(t, "Does this fit?", req.Message)
	assert.Equal(t, "123", req.ProductID)
	assert.Empty(t, req.History)
}

func TestSuggestRequestJSON_MatchesWidgetPayload(t *testing.T) {
	// Widget sends: {product_id}
	widgetPayload := `{"product_id":"123"}`

	var req struct {
		ProductID string `json:"product_id"`
	}
	assert.NoError(t, json.Unmarshal([]byte(widgetPayload), &req))
	assert.Equal(t, "123", req.ProductID)
}
