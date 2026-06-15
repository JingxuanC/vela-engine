package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/service"
)

func setupHealthTest(t *testing.T) (*HealthHandler, *miniredis.Miniredis, *gorm.DB) {
	t.Helper()

	// In-memory SQLite for DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		SkipDefaultTransaction: true,
		PrepareStmt:            true,
	})
	require.NoError(t, err)

	// In-memory Redis
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	cache, err := service.NewCacheService(context.Background(), "redis://"+mr.Addr())
	require.NoError(t, err)
	t.Cleanup(func() { cache.Close() })

	h := NewHealthHandler(cache, db, nil)
	return h, mr, db
}

func TestHealth_AlwaysOK(t *testing.T) {
	h, _, _ := setupHealthTest(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.Health(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]string
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp["status"])
	assert.Equal(t, "Vela AI API", resp["service"])
}

func TestReady_OK(t *testing.T) {
	h, _, _ := setupHealthTest(t)

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	h.Ready(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]string
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "ready", resp["status"])
}

func TestReady_RedisDown(t *testing.T) {
	h, mr, _ := setupHealthTest(t)

	// Close Redis so Ping fails
	mr.Close()

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	h.Ready(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var resp map[string]string
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Contains(t, resp["detail"], "redis not ready")
}

func TestReady_DBDown(t *testing.T) {
	h, _, db := setupHealthTest(t)

	// Close the DB connection
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.Close()

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	h.Ready(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var resp map[string]string
	err = json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Contains(t, resp["detail"], "db not ready")
}

func TestReady_NilDB(t *testing.T) {
	// HealthHandler with nil DB but valid cache
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	cache, err := service.NewCacheService(context.Background(), "redis://"+mr.Addr())
	require.NoError(t, err)
	t.Cleanup(func() { cache.Close() })

	h := NewHealthHandler(cache, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	h.Ready(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]string
	err = json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "ready", resp["status"])
}

func TestReady_NilCache(t *testing.T) {
	// HealthHandler with nil cache but valid DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		SkipDefaultTransaction: true,
		PrepareStmt:            true,
	})
	require.NoError(t, err)

	h := NewHealthHandler(nil, db, nil)

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	h.Ready(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]string
	err = json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "ready", resp["status"])
}
