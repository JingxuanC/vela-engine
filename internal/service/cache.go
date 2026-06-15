// Package service provides business logic for the Vela AI API.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Lua script for atomic quota check and increment.
const checkQuotaScript = `
local key = KEYS[1]
local limit = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
local count = redis.call('INCR', key)
if count == 1 then
    redis.call('EXPIRE', key, ttl)
end
return {count, limit}
`

// Default daily quota limits per feature. Overridable via Redis.
var DefaultQuotaLimits = map[string]int{
	"tryon":       100,
	"seo":         200,
	"description": 300,
	"size":        200,
	"image":       100,
	"review":      300,
	"chat":        500,
	"insights":    100,
	"shop":        10000,
	"youtube":     100,
	"meta":        100,
	"analytics":    30,
	"enterprise":  300,
	"multi-store": 100,
	"returns":     10000,
	"exchange":    10000,
	"recommend":   10000,
	"notifications": 10000,
	"cart-recovery": 10000,
	"style":       10000,
	"billing":     10000,
	"aitools":     10000,
}

// CacheService provides Redis-backed caching, pub/sub, and atomic quota operations.
type CacheService struct {
	client      *redis.Client
	quotaScript *redis.Script
}

// NewCacheService creates a new CacheService connected to the given Redis URL.
func NewCacheService(ctx context.Context, redisURL string) (*CacheService, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("cache: parse redis url: %w", err)
	}
	opts.PoolSize = 50
	opts.MinIdleConns = 5

	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("cache: connect redis: %w", err)
	}

	return &CacheService{
		client:      client,
		quotaScript: redis.NewScript(checkQuotaScript),
	}, nil
}

// Close releases the Redis connection pool.
func (s *CacheService) Close() error {
	return s.client.Close()
}

// Ping checks Redis connectivity.
func (s *CacheService) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

// Get retrieves a string value by key. Returns empty string if key does not exist.
func (s *CacheService) Get(ctx context.Context, key string) (string, error) {
	val, err := s.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	return val, err
}

// Set stores a string value with an optional TTL.
func (s *CacheService) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return s.client.Set(ctx, key, value, ttl).Err()
}

// Incr atomically increments a counter and returns the new value.
func (s *CacheService) Incr(ctx context.Context, key string) (int64, error) {
	return s.client.Incr(ctx, key).Result()
}

// Publish sends a message to a Redis channel (used for SSE progress updates).
func (s *CacheService) Publish(ctx context.Context, channel, message string) error {
	return s.client.Publish(ctx, channel, message).Err()
}

// GetDict retrieves and deserializes a JSON value.
func (s *CacheService) GetDict(ctx context.Context, key string) (map[string]interface{}, error) {
	raw, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, fmt.Errorf("cache: unmarshal dict: %w", err)
	}
	return data, nil
}

// SetDict serializes and stores a dict value with TTL.
func (s *CacheService) SetDict(ctx context.Context, key string, data map[string]interface{}, ttl time.Duration) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("cache: marshal dict: %w", err)
	}
	return s.Set(ctx, key, string(raw), ttl)
}

// Exists checks whether a key exists in Redis.
func (s *CacheService) Exists(ctx context.Context, key string) (bool, error) {
	n, err := s.client.Exists(ctx, key).Result()
	return n > 0, err
}

// CheckQuota atomically increments the daily quota counter and returns (allowed, count).
// Uses a Lua script to guarantee atomicity of INCR + EXPIRE + check.
func (s *CacheService) CheckQuota(ctx context.Context, key, date string, limit int) (bool, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fullKey := fmt.Sprintf("quota:%s:%s", key, date)
	ttl := int64(86400 * 2)

	results, err := s.quotaScript.Run(ctx, s.client, []string{fullKey}, limit, ttl).Slice()
	if err != nil {
		return false, 0, fmt.Errorf("cache: check quota: %w", err)
	}

	if len(results) < 2 {
		return false, 0, fmt.Errorf("cache: unexpected quota result length %d", len(results))
	}

	count, ok1 := results[0].(int64)
	limitVal, ok2 := results[1].(int64)
	if !ok1 || !ok2 {
		slog.Warn("cache: CheckQuota type assertion failed, failing open",
			"count_type", fmt.Sprintf("%T", results[0]),
			"limit_type", fmt.Sprintf("%T", results[1]),
		)
		return true, 0, nil
	}

	return count <= limitVal, count, nil
}

// Delete removes a key from Redis.
func (s *CacheService) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, key).Err()
}

// Expire sets a TTL on an existing key.
func (s *CacheService) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return s.client.Expire(ctx, key, ttl).Err()
}

// TTL returns the remaining TTL of a key in seconds.
func (s *CacheService) TTL(ctx context.Context, key string) (time.Duration, error) {
	return s.client.TTL(ctx, key).Result()
}

// Client returns the underlying Redis client for advanced operations.
func (s *CacheService) Client() *redis.Client {
	return s.client
}
