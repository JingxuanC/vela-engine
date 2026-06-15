package service

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestCache(t *testing.T) (*CacheService, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	svc, err := NewCacheService(context.Background(), "redis://"+mr.Addr())
	require.NoError(t, err)
	return svc, mr
}

func TestCacheService_SetGet(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	err := cache.Set(ctx, "test-key", "hello", 10*time.Minute)
	require.NoError(t, err)

	val, err := cache.Get(ctx, "test-key")
	require.NoError(t, err)
	assert.Equal(t, "hello", val)
}

func TestCacheService_GetMiss(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	val, err := cache.Get(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Empty(t, val)
}

func TestCacheService_SetGetDict(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	data := map[string]interface{}{
		"task_id": "abc-123",
		"status":  "pending",
		"count":   float64(42),
	}
	err := cache.SetDict(ctx, "test-dict", data, 10*time.Minute)
	require.NoError(t, err)

	result, err := cache.GetDict(ctx, "test-dict")
	require.NoError(t, err)
	assert.Equal(t, "abc-123", result["task_id"])
	assert.Equal(t, "pending", result["status"])
}

func TestCacheService_TTL(t *testing.T) {
	cache, mr := setupTestCache(t)
	ctx := context.Background()

	err := cache.Set(ctx, "ttl-key", "value", 1*time.Second)
	require.NoError(t, err)

	// Fast-forward miniredis internal clock past TTL
	mr.FastForward(2 * time.Second)

	val, err := cache.Get(ctx, "ttl-key")
	require.NoError(t, err)
	assert.Empty(t, val, "key should have expired")
}

func TestCacheService_Exists(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	exists, err := cache.Exists(ctx, "missing")
	require.NoError(t, err)
	assert.False(t, exists)

	err = cache.Set(ctx, "exists", "yes", time.Minute)
	require.NoError(t, err)

	exists, err = cache.Exists(ctx, "exists")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestCacheService_Incr(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	n, err := cache.Incr(ctx, "counter")
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	n, err = cache.Incr(ctx, "counter")
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
}

func TestCacheService_Delete(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	err := cache.Set(ctx, "del-key", "bye", time.Minute)
	require.NoError(t, err)

	err = cache.Delete(ctx, "del-key")
	require.NoError(t, err)

	val, err := cache.Get(ctx, "del-key")
	require.NoError(t, err)
	assert.Empty(t, val)
}

func TestCheckQuota_FirstRequest(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	allowed, count, err := cache.CheckQuota(ctx, "test-shop", "20260101", 100)
	require.NoError(t, err)
	assert.True(t, allowed, "first request should be allowed")
	assert.Equal(t, int64(1), count)
}

func TestCheckQuota_UnderLimit(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		allowed, count, err := cache.CheckQuota(ctx, "test-shop", "20260102", 100)
		require.NoError(t, err)
		assert.True(t, allowed)
		assert.Equal(t, int64(i+1), count)
	}
}

func TestCheckQuota_ExceedsLimit(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	limit := 5
	for i := 0; i < limit; i++ {
		allowed, _, err := cache.CheckQuota(ctx, "test-shop", "20260103", limit)
		require.NoError(t, err)
		assert.True(t, allowed, "request %d should be allowed", i+1)
	}

	// Next request should be rejected
	allowed, count, err := cache.CheckQuota(ctx, "test-shop", "20260103", limit)
	require.NoError(t, err)
	assert.False(t, allowed, "request over limit should be rejected")
	assert.Equal(t, int64(limit+1), count)
}

func TestCheckQuota_Atomicity(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	done := make(chan bool)

	// Run 10 concurrent goroutines each making 10 requests = 100 total
	for g := 0; g < 10; g++ {
		go func() {
			for i := 0; i < 10; i++ {
				_, _, err := cache.CheckQuota(ctx, "atomic-test", "20260104", 100)
				assert.NoError(t, err)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for g := 0; g < 10; g++ {
		<-done
	}

	// Verify accurate count (should be exactly 100)
	val, err := cache.Get(ctx, "quota:atomic-test:20260104")
	require.NoError(t, err)
	assert.Equal(t, "100", val, "atomic quota count should be exactly 100")
}

func TestCheckQuota_TTLSet(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	_, _, err := cache.CheckQuota(ctx, "ttl-test", "20260105", 100)
	require.NoError(t, err)

	ttl, err := cache.TTL(ctx, "quota:ttl-test:20260105")
	require.NoError(t, err)
	assert.True(t, ttl > 0, "TTL should be set on first request")
}
