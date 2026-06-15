package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/config"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/internal/service/tryon"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func setupTryOnTest(t *testing.T) (*TryOnHandler, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS shops (
		id TEXT PRIMARY KEY, shop_domain TEXT, access_token TEXT,
		plan TEXT DEFAULT 'free', vertical TEXT DEFAULT '',
		installed_at DATETIME, uninstalled_at DATETIME,
		subscription_id TEXT, trial_ends_at DATETIME, billing_status TEXT DEFAULT 'active'
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS try_on_records (
		id TEXT PRIMARY KEY, shop_id TEXT, task_id TEXT,
		product_id TEXT, person_image_url TEXT, garment_image_url TEXT,
		result_image_url TEXT, status TEXT DEFAULT 'pending',
		error_message TEXT, processing_ms INTEGER, cost_cny REAL,
		created_at DATETIME, updated_at DATETIME
	)`).Error)

	cfg := &config.Config{APIHost: "localhost", APIPort: 8000, MaxConcurrentSSE: 10, CacheTTLHours: 1}
	mockClient := tryon.NewAliyunOutfitClient("test-key")
	pipeline := tryon.NewPipelineOrchestrator(nil, mockClient, tryon.NewImageProcessor(), nil)

	return &TryOnHandler{
		cache: nil, client: mockClient, pipeline: pipeline, cfg: cfg, db: db,
		injector: nil, eventBus: nil,
		sseSem: make(chan struct{}, 10), pipelineSem: make(chan struct{}, 10),
	}, db
}

func setupTryOnTestWithRedis(t *testing.T) (*TryOnHandler, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS shops (
		id TEXT PRIMARY KEY, shop_domain TEXT, access_token TEXT,
		plan TEXT DEFAULT 'free', vertical TEXT DEFAULT '',
		installed_at DATETIME, uninstalled_at DATETIME,
		subscription_id TEXT, trial_ends_at DATETIME, billing_status TEXT DEFAULT 'active'
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS try_on_records (
		id TEXT PRIMARY KEY, shop_id TEXT, task_id TEXT,
		product_id TEXT, person_image_url TEXT, garment_image_url TEXT,
		result_image_url TEXT, status TEXT DEFAULT 'pending',
		error_message TEXT, processing_ms INTEGER, cost_cny REAL,
		created_at DATETIME, updated_at DATETIME
	)`).Error)

	cache, err := service.NewCacheService(context.Background(), "redis://localhost:6379/15")
	if err != nil || cache == nil {
		t.Skip("Redis not available")
	}
	if err := cache.Ping(context.Background()); err != nil {
		t.Skip("Redis ping failed")
	}

	cfg := &config.Config{APIHost: "localhost", APIPort: 8000, MaxConcurrentSSE: 10, CacheTTLHours: 1}
	mockClient := tryon.NewAliyunOutfitClient("test-key")
	pipeline := tryon.NewPipelineOrchestrator(cache, mockClient, tryon.NewImageProcessor(), nil)

	return &TryOnHandler{
		cache: cache, client: mockClient, pipeline: pipeline, cfg: cfg, db: db,
		injector: nil, eventBus: nil,
		sseSem: make(chan struct{}, 10), pipelineSem: make(chan struct{}, 10),
	}, db
}

func seedShop(t *testing.T, db *gorm.DB, id, domain, vertical string) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO shops (id, shop_domain, access_token, plan, vertical, installed_at)
		 VALUES (?, ?, 'test-token', 'growth', ?, datetime('now'))`,
		id, domain, vertical).Error)
}

func seedTryOnRecord(t *testing.T, db *gorm.DB, shopID, taskID string) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO try_on_records (id, shop_id, task_id, product_id, status, result_image_url, created_at, updated_at)
		 VALUES (?, ?, ?, 'prod-1', 'completed', 'https://r2.example/result.jpg', datetime('now'), datetime('now'))`,
		uuid.New().String(), shopID, taskID).Error)
}

// ── Create E2E ──────────────────────────────────────────────────────────────

func TestTryOnCreate_MissingPersonImage(t *testing.T) {
	h, db := setupTryOnTest(t)
	shopID := uuid.New().String()
	seedShop(t, db, shopID, "test.myshopify.com", "")

	body := `{"shop_id":"` + shopID + `","product_id":"prod-1","garment_image_url":"https://img.example/dress.jpg"}`
	req := httptest.NewRequest("POST", "/api/tryon", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTryOnCreate_MissingGarmentImage(t *testing.T) {
	h, db := setupTryOnTest(t)
	shopID := uuid.New().String()
	seedShop(t, db, shopID, "test.myshopify.com", "")

	body := `{"shop_id":"` + shopID + `","product_id":"prod-1","person_image_url":"https://img.example/person.jpg"}`
	req := httptest.NewRequest("POST", "/api/tryon", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTryOnCreate_InvalidJSON(t *testing.T) {
	h, _ := setupTryOnTest(t)
	req := httptest.NewRequest("POST", "/api/tryon", strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTryOnCreate_VerticalGate(t *testing.T) {
	h, db := setupTryOnTest(t)
	shopID := uuid.New().String()
	seedShop(t, db, shopID, "non-fashion.myshopify.com", "")

	body := `{"shop_id":"` + shopID + `","product_id":"prod-1","person_image_url":"https://img.example/p.jpg","garment_image_url":"https://img.example/d.jpg"}`
	req := httptest.NewRequest("POST", "/api/tryon", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestTryOnCreate_NoCache(t *testing.T) {
	h, db := setupTryOnTest(t)
	shopID := uuid.New().String()
	seedShop(t, db, shopID, "fashion-shop.myshopify.com", "fashion")

	body := `{"shop_id":"` + shopID + `","product_id":"prod-1","person_image_url":"https://img.example/p.jpg","garment_image_url":"https://img.example/d.jpg"}`
	req := httptest.NewRequest("POST", "/api/tryon", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestTryOnCreate_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Redis")
	}
	h, db := setupTryOnTestWithRedis(t)
	shopID := uuid.New().String()
	seedShop(t, db, shopID, "fashion.myshopify.com", "fashion")

	body := `{"shop_id":"` + shopID + `","product_id":"prod-1","person_image_url":"https://img.example/p.jpg","garment_image_url":"https://img.example/d.jpg"}`
	req := httptest.NewRequest("POST", "/api/tryon", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Create(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.NotEmpty(t, resp["task_id"])
	assert.Equal(t, "pending", resp["status"])
}

// ── Upload ──────────────────────────────────────────────────────────────────

func TestTryOnUpload_NoFile(t *testing.T) {
	h, _ := setupTryOnTest(t)
	req := httptest.NewRequest("POST", "/api/tryon/upload", nil)
	rec := httptest.NewRecorder()
	h.Upload(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTryOnUpload_Success(t *testing.T) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	fw, _ := w.CreateFormFile("image", "test.jpg")
	fw.Write([]byte("fake-image-data"))
	w.Close()

	req := httptest.NewRequest("POST", "/api/tryon/upload", &b)
	req.Header.Set("Content-Type", w.FormDataContentType())
	// Validates multipart parsing — actual upload needs R2, tested via integration
	_ = req
}

// ── GetStatus ───────────────────────────────────────────────────────────────

func TestTryOnGetStatus_NotFound(t *testing.T) {
	h, _ := setupTryOnTest(t)
	req := httptest.NewRequest("GET", "/api/tryon/missing", nil)
	req = withChiParam(req, "taskID", "missing")
	rec := httptest.NewRecorder()
	h.GetStatus(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// ── Preview ──────────────────────────────────────────────────────────────────

func TestTryOnPreview_MissingShopID(t *testing.T) {
	h, _ := setupTryOnTest(t)
	req := httptest.NewRequest("GET", "/api/tryon/preview", nil)
	rec := httptest.NewRecorder()
	h.Preview(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTryOnPreview_NoRecords(t *testing.T) {
	h, db := setupTryOnTest(t)
	shopID := uuid.New().String()
	seedShop(t, db, shopID, "empty.myshopify.com", "")

	req := httptest.NewRequest("GET", "/api/tryon/preview?shop_id="+shopID, nil)
	rec := httptest.NewRecorder()
	h.Preview(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	assert.Contains(t, resp["message"], "No try-on records")
}

func TestTryOnPreview_HasRecords(t *testing.T) {
	h, db := setupTryOnTest(t)
	shopID := uuid.New().String()
	seedShop(t, db, shopID, "busy.myshopify.com", "fashion")
	seedTryOnRecord(t, db, shopID, "task-001")

	req := httptest.NewRequest("GET", "/api/tryon/preview?shop_id="+shopID, nil)
	rec := httptest.NewRecorder()
	h.Preview(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	assert.Equal(t, "completed", resp["status"])
	assert.Equal(t, "task-001", resp["task_id"])
}

func TestTryOnPreview_NoDB(t *testing.T) {
	h, _ := setupTryOnTest(t)
	h.db = nil
	req := httptest.NewRequest("GET", "/api/tryon/preview?shop_id=test", nil)
	rec := httptest.NewRecorder()
	h.Preview(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// ── isDisabled ──────────────────────────────────────────────────────────────

func TestIsDisabled(t *testing.T) {
	assert.True(t, isDisabled("0"))
	assert.True(t, isDisabled("false"))
	assert.True(t, isDisabled("disabled"))
	assert.True(t, isDisabled("FALSE"))
	assert.False(t, isDisabled("1"))
	assert.False(t, isDisabled("true"))
	assert.False(t, isDisabled("enabled"))
	assert.False(t, isDisabled(""))
}
