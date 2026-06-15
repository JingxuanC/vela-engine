package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/insight"
	"github.com/JingxuanC/vela-engine/internal/service"
)

// ── Test Helpers ────────────────────────────────────────────────────────────────

func setupContentTest(t *testing.T) (*ContentHandler, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	// Manual table creation — avoids PostgreSQL-specific gen_random_uuid() in GORM tags
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS synced_products (
			id TEXT PRIMARY KEY, shop_id TEXT, platform_id TEXT,
			title TEXT, description TEXT, vendor TEXT,
			product_type TEXT, status TEXT DEFAULT 'active',
			variants BLOB, images BLOB, options BLOB, tags TEXT,
			created_at DATETIME, updated_at DATETIME
		)
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS content_jobs (
			id TEXT PRIMARY KEY, shop_id TEXT, job_type TEXT,
			source_product_ids BLOB, platform_targets BLOB,
			tone TEXT DEFAULT 'professional', language TEXT DEFAULT 'en',
			status TEXT DEFAULT 'pending', result BLOB,
			error_message TEXT,
			created_at DATETIME, updated_at DATETIME
		)
	`).Error)

	injector := insight.NewContextInjector()
	h := NewContentHandler(db, service.NewLLMRouter(db, nil, map[string]string{"deepseek": "test-key"}), injector)
	return h, db
}

func seedProduct(t *testing.T, db *gorm.DB, shopID, platformID, title, ptype, vendor, tags, desc string) {
	t.Helper()
	err := db.Exec(`INSERT INTO synced_products (id, shop_id, platform_id, title, description, vendor, product_type, tags, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
		uuid.New().String(), shopID, platformID, title, desc, vendor, ptype, tags).Error
	require.NoError(t, err)
}

// ── mockFetcher ─────────────────────────────────────────────────────────────────

type mockFetcher struct {
	name   string
	output string
}

func (f *mockFetcher) Name() string                                         { return f.name }
func (f *mockFetcher) Fetch(_ context.Context, _ *gorm.DB, _ string) string { return f.output }

// ── buildContentPrompt Tests ────────────────────────────────────────────────────

func TestBuildContentPrompt_WithProduct(t *testing.T) {
	h, db := setupContentTest(t)
	shopID := uuid.New().String()
	seedProduct(t, db, shopID, "prod-1", "Summer Dress", "Clothing", "Zara", "summer,tropical", "Flowy rayon dress with V-neck")

	prompt := h.buildContentPrompt(context.Background(), uuid.MustParse(shopID), "prod-1", "desc", "professional", "en")

	assert.Contains(t, prompt, "[Product Data]")
	assert.Contains(t, prompt, "Summer Dress")
	assert.Contains(t, prompt, "Clothing")
	assert.Contains(t, prompt, "Zara")
	assert.Contains(t, prompt, "summer,tropical")
	assert.Contains(t, prompt, "Flowy rayon dress")
	assert.Contains(t, prompt, "[Task]")
	assert.Contains(t, prompt, "product description")
	assert.Contains(t, prompt, "100-150 words")
}

func TestBuildContentPrompt_WithoutProduct(t *testing.T) {
	h, _ := setupContentTest(t)
	prompt := h.buildContentPrompt(context.Background(), uuid.New(), "nonexistent", "desc", "casual", "en")

	assert.NotContains(t, prompt, "[Product Data]")
	assert.Contains(t, prompt, "[Task]")
}

func TestBuildContentPrompt_DifferentJobTypes(t *testing.T) {
	h, db := setupContentTest(t)
	shopID := uuid.New().String()
	seedProduct(t, db, shopID, "p1", "Test", "Type", "B", "", "")

	tests := []struct{ jobType, wantLabel string }{
		{"desc", "product description"},
		{"blog", "blog post"},
		{"social", "social media caption"},
	}
	for _, tt := range tests {
		t.Run(tt.jobType, func(t *testing.T) {
			p := h.buildContentPrompt(context.Background(), uuid.MustParse(shopID), "p1", tt.jobType, "friendly", "en")
			assert.Contains(t, p, tt.wantLabel)
		})
	}
}

func TestBuildContentPrompt_BlogHasMoreTokens(t *testing.T) {
	cfg := modeConfig("blog")
	assert.Equal(t, 1000, cfg.maxTokens)
	assert.Equal(t, 0.7, cfg.temperature)
}

func TestBuildContentPrompt_SocialHasLessTokens(t *testing.T) {
	cfg := modeConfig("social")
	assert.Equal(t, 300, cfg.maxTokens)
	assert.Equal(t, 0.8, cfg.temperature)
}

func TestBuildContentPrompt_DescModeConfig(t *testing.T) {
	cfg := modeConfig("desc")
	assert.Equal(t, 500, cfg.maxTokens)
	assert.Equal(t, 0.3, cfg.temperature)
	assert.Equal(t, []string{"product", "review", "ai_reply"}, cfg.ragSources)
}

func TestBuildContentPrompt_WithInjectorContext(t *testing.T) {
	h, db := setupContentTest(t)
	shopID := uuid.New().String()
	seedProduct(t, db, shopID, "p1", "Cotton Tee", "Tops", "Nike", "basic", "Soft cotton tee")
	h.injector.Register(&mockFetcher{name: "trend_data", output: "[Trend] Tops trending +85%"})
	h.injector.Register(&mockFetcher{name: "product_data", output: "[Products] Cotton Tee"})

	prompt := h.buildContentPrompt(context.Background(), uuid.MustParse(shopID), "p1", "desc", "professional", "en")

	assert.Contains(t, prompt, "[Product Data]")
	assert.Contains(t, prompt, "[Trend]")
	assert.Contains(t, prompt, "[Products]")
	assert.Contains(t, prompt, "[Task]")
	assert.Contains(t, prompt, "Cotton Tee")
}

func TestBuildContentPrompt_NilInjector(t *testing.T) {
	h, db := setupContentTest(t)
	shopID := uuid.New().String()
	seedProduct(t, db, shopID, "p1", "Test", "", "", "", "")
	h.injector = nil

	prompt := h.buildContentPrompt(context.Background(), uuid.MustParse(shopID), "p1", "desc", "professional", "en")

	assert.Contains(t, prompt, "[Product Data]")
	assert.NotContains(t, prompt, "semantic")
}

func TestBuildContentPrompt_NilDB(t *testing.T) {
	h, _ := setupContentTest(t)
	h.db = nil

	prompt := h.buildContentPrompt(context.Background(), uuid.New(), "p1", "desc", "professional", "en")
	assert.NotContains(t, prompt, "[Product Data]")
	assert.Contains(t, prompt, "[Task]")
}

func TestBuildContentPrompt_EmptyDescription(t *testing.T) {
	h, db := setupContentTest(t)
	shopID := uuid.New().String()
	seedProduct(t, db, shopID, "p-empty", "No Desc", "", "", "", "")

	prompt := h.buildContentPrompt(context.Background(), uuid.MustParse(shopID), "p-empty", "desc", "professional", "en")
	assert.Contains(t, prompt, "No Desc")
	assert.NotContains(t, prompt, "Current description")
}

func TestBuildContentPrompt_MultipleTags(t *testing.T) {
	h, db := setupContentTest(t)
	shopID := uuid.New().String()
	seedProduct(t, db, shopID, "p1", "Multi Tag Product", "Type", "Brand", "tag1,tag2,tag3", "A great item")

	prompt := h.buildContentPrompt(context.Background(), uuid.MustParse(shopID), "p1", "desc", "professional", "en")
	assert.Contains(t, prompt, "tag1,tag2,tag3")
}

// ── Generate Endpoint Tests ─────────────────────────────────────────────────────

func TestGenerate_MissingShopID(t *testing.T) {
	h, _ := setupContentTest(t)
	body := strings.NewReader(`{"job_type":"desc","product_ids":["1"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/content/generate", body)
	rec := httptest.NewRecorder()

	h.Generate(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGenerate_MissingJobType(t *testing.T) {
	h, _ := setupContentTest(t)
	body := strings.NewReader(`{"shop_id":"test","product_ids":["1"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/content/generate", body)
	rec := httptest.NewRecorder()

	h.Generate(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGenerate_MissingProductIDs(t *testing.T) {
	h, _ := setupContentTest(t)
	body := strings.NewReader(`{"shop_id":"test","job_type":"desc"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/content/generate", body)
	rec := httptest.NewRecorder()

	h.Generate(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGenerate_InvalidJobType(t *testing.T) {
	h, _ := setupContentTest(t)
	body := strings.NewReader(`{"shop_id":"test","job_type":"poem","product_ids":["1"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/content/generate", body)
	rec := httptest.NewRecorder()

	h.Generate(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGenerate_Success(t *testing.T) {
	h, db := setupContentTest(t)
	shopID := uuid.New()
	seedProduct(t, db, shopID.String(), "prod-1", "Dress", "", "", "", "")

	body := strings.NewReader(`{"shop_id":"` + shopID.String() + `","job_type":"desc","product_ids":["1"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/content/generate", body)
	rec := httptest.NewRecorder()

	h.Generate(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.True(t, resp["success"].(bool))
	assert.NotEmpty(t, resp["job_id"])
	assert.Equal(t, "processing", resp["status"])
}

func TestGenerate_DefaultToneAndLanguage(t *testing.T) {
	h, db := setupContentTest(t)
	shopID := uuid.New()
	seedProduct(t, db, shopID.String(), "p1", "Test", "", "", "", "")

	body := strings.NewReader(`{"shop_id":"` + shopID.String() + `","job_type":"desc","product_ids":["p1"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/content/generate", body)
	rec := httptest.NewRecorder()

	h.Generate(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	assert.Equal(t, "processing", resp["status"])
}

// ── Integration: DB persistence ─────────────────────────────────────────────────

func TestJobCreatedInDB(t *testing.T) {
	h, db := setupContentTest(t)
	shopID := uuid.New()
	seedProduct(t, db, shopID.String(), "p1", "Test", "", "", "", "")

	// Before: 0 jobs
	var count int64
	db.Model(&model.ContentJob{}).Count(&count)
	assert.Equal(t, int64(0), count)

	body := strings.NewReader(`{"shop_id":"` + shopID.String() + `","job_type":"desc","product_ids":["p1"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/content/generate", body)
	rec := httptest.NewRecorder()
	h.Generate(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// After: 1 job created (sync, before goroutine starts)
	db.Model(&model.ContentJob{}).Count(&count)
	assert.Equal(t, int64(1), count)
}

// ── Mode-specific prompt structure ────────────────────────────────────────────

func TestBuildContentPrompt_BlogStructure(t *testing.T) {
	h, db := setupContentTest(t)
	shopID := uuid.New().String()
	seedProduct(t, db, shopID, "bp1", "Linen Maxi Dress", "Clothing", "Zara", "summer,linen", "Elegant linen dress")

	prompt := h.buildContentPrompt(context.Background(), uuid.MustParse(shopID), "bp1", "blog", "casual", "en")

	// Blog must include headline, intro, body, closing
	assert.Contains(t, prompt, "Headline")
	assert.Contains(t, prompt, "Intro")
	assert.Contains(t, prompt, "Body")
	assert.Contains(t, prompt, "Closing")
	assert.Contains(t, prompt, "300-500 words")
	assert.Contains(t, prompt, "Linen Maxi Dress")
}

func TestBuildContentPrompt_SocialStructure(t *testing.T) {
	h, db := setupContentTest(t)
	shopID := uuid.New().String()
	seedProduct(t, db, shopID, "sp1", "Cool Sneakers", "Shoes", "Nike", "streetwear", "Limited edition kicks")

	prompt := h.buildContentPrompt(context.Background(), uuid.MustParse(shopID), "sp1", "social", "fun", "en")

	assert.Contains(t, prompt, "50 words")
	assert.Contains(t, prompt, "hashtag")
	assert.Contains(t, prompt, "call to action")
	assert.Contains(t, prompt, "stop the scroll")
	assert.Contains(t, prompt, "Cool Sneakers")
}

func TestBuildContentPrompt_ProductNameFallback(t *testing.T) {
	h, _ := setupContentTest(t)
	// No product seeded — name should fall back to productID

	prompt := h.buildContentPrompt(context.Background(), uuid.New(), "fallback-id-123", "desc", "professional", "en")

	// The productID should appear as the name since no DB record exists
	assert.Contains(t, prompt, "fallback-id-123")
}

func TestBuildContentPrompt_UnknownMode(t *testing.T) {
	h, db := setupContentTest(t)
	shopID := uuid.New().String()
	seedProduct(t, db, shopID, "u1", "Test", "", "", "", "")

	// Unknown mode falls through switch — only has [Product Data] and [Task] sections
	prompt := h.buildContentPrompt(context.Background(), uuid.MustParse(shopID), "u1", "unknown", "friendly", "en")

	assert.Contains(t, prompt, "[Product Data]")
	// Unknown mode falls through switch — no task section, only data
	assert.NotContains(t, prompt, "[Task]")
}

func TestBuildContentPrompt_BlogIncludesTrendContext(t *testing.T) {
	h, db := setupContentTest(t)
	shopID := uuid.New().String()
	seedProduct(t, db, shopID, "bt1", "Trendy Jacket", "Outerwear", "Brand", "trendy", "")
	// Blog mode uses RAG source "insight" which includes trend data
	cfg := modeConfig("blog")
	assert.Contains(t, cfg.ragSources, "insight", "blog should include insight/trend data")

	prompt := h.buildContentPrompt(context.Background(), uuid.MustParse(shopID), "bt1", "blog", "professional", "en")
	assert.Contains(t, prompt, "[Task]")
	assert.Contains(t, prompt, "blog post")
}

func TestBuildContentPrompt_SocialExcludesInsight(t *testing.T) {
	cfg := modeConfig("social")
	assert.NotContains(t, cfg.ragSources, "insight", "social should not include insight data")
	assert.NotContains(t, cfg.ragSources, "ai_reply", "social should not include ai_reply")
	assert.Contains(t, cfg.ragSources, "product")
	assert.Contains(t, cfg.ragSources, "review")
}

// ── Generate goroutine path ────────────────────────────────────────────────────

func TestGenerate_AllThreeModesAccepted(t *testing.T) {
	modes := []string{"desc", "blog", "social"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			h, db := setupContentTest(t)
			shopID := uuid.New()
			seedProduct(t, db, shopID.String(), "p-"+mode, "Test", "", "", "", "")

			body := strings.NewReader(`{"shop_id":"` + shopID.String() + `","job_type":"` + mode + `","product_ids":["p-` + mode + `"]}`)
			req := httptest.NewRequest(http.MethodPost, "/api/content/generate", body)
			rec := httptest.NewRecorder()

			h.Generate(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code)

			var resp map[string]interface{}
			json.NewDecoder(rec.Body).Decode(&resp)
			assert.Equal(t, "processing", resp["status"])
		})
	}
}

func TestGenerate_AllOKTrackedCorrectly(t *testing.T) {
	h, db := setupContentTest(t)
	shopID := uuid.New()
	seedProduct(t, db, shopID.String(), "ok1", "OK Product", "", "", "", "")

	body := strings.NewReader(`{"shop_id":"` + shopID.String() + `","job_type":"desc","product_ids":["ok1"],"tone":"luxury","language":"fr"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/content/generate", body)
	rec := httptest.NewRecorder()

	h.Generate(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	assert.True(t, resp["success"].(bool))
	assert.NotEmpty(t, resp["job_id"])
}
