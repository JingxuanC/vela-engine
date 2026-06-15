package middleware

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JingxuanC/vela-engine/internal/service"
)

// RateLimitConfig defines rate limiting rules for a specific path prefix.
type RateLimitConfig struct {
	PathPrefix  string        // e.g. "/api/contact", "/api/llm"
	Window      time.Duration // window duration
	MaxRequests int           // max requests in the window
	PerShop     bool          // if true, key includes shop_id
	PerIP       bool          // if true, key includes client IP
}

// DefaultRateLimits returns the standard rate limiting configuration.
// Config is ordered: more specific prefixes match first, "/api" is catch-all.
func DefaultRateLimits() []RateLimitConfig {
	return []RateLimitConfig{
		{PathPrefix: "/api/contact", Window: 60 * time.Second, MaxRequests: 5, PerIP: true},
		{PathPrefix: "/api/llm", Window: 60 * time.Second, MaxRequests: 10, PerShop: true},
		{PathPrefix: "/api/admin/observability", Window: 60 * time.Second, MaxRequests: 10, PerShop: true},
		{PathPrefix: "/api", Window: 60 * time.Second, MaxRequests: 60, PerShop: true},
	}
}

// RateLimit returns a middleware that enforces rate limits using Redis.
// Uses a fixed-window approach (INCR + EXPIRE) which is simple and effective.
// Limits are checked in order: more specific prefixes match first.
// On Redis errors, the middleware fails open (allows the request through).
func RateLimit(cache *service.CacheService, configs []RateLimitConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only limit /api/ paths
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				next.ServeHTTP(w, r)
				return
			}

			// Skip if no cache
			if cache == nil {
				next.ServeHTTP(w, r)
				return
			}

			// Find the first matching config (most specific first)
			var matched *RateLimitConfig
			for i := range configs {
				if strings.HasPrefix(r.URL.Path, configs[i].PathPrefix) {
					matched = &configs[i]
					break
				}
			}

			if matched == nil {
				next.ServeHTTP(w, r)
				return
			}

			// Build rate limit key
			key := buildRateLimitKey(r, matched)

			// Fixed-window atomic INCR + EXPIRE
			count, err := cache.Client().Incr(r.Context(), key).Result()
			if err != nil {
				slog.Warn("rate_limit: incr failed, failing open",
					"path", r.URL.Path, "error", err)
				next.ServeHTTP(w, r)
				return
			}

			// Set TTL on first request in the window
			if count == 1 {
				cache.Client().Expire(r.Context(), key, matched.Window*2)
			}

			remaining := matched.MaxRequests - int(count)
			if remaining < 0 {
				remaining = 0
			}

			// Set rate limit headers
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(matched.MaxRequests))
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))

			if count > int64(matched.MaxRequests) {
				resetSec := int64(matched.Window.Seconds())
				w.Header().Set("Retry-After", strconv.FormatInt(resetSec, 10))
				http.Error(w, `{"detail":"Too many requests"}`, http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// buildRateLimitKey constructs a Redis key for rate limiting.
// Format: ratelimit:{prefix}:{shop_id}:{ip}:{window_bucket}
func buildRateLimitKey(r *http.Request, cfg *RateLimitConfig) string {
	// Normalize path prefix to a clean key component
	prefix := strings.TrimPrefix(cfg.PathPrefix, "/api/")
	prefix = strings.ReplaceAll(prefix, "/", ":")

	key := "ratelimit:" + prefix

	if cfg.PerShop {
		shopID := ShopIDFromContext(r.Context())
		if shopID == "" {
			shopID = r.Header.Get("X-Shop-ID")
		}
		if shopID == "" {
			shopID = r.URL.Query().Get("shop_id")
		}
		if shopID != "" {
			key += ":" + shopID
		}
	}

	if cfg.PerIP {
		ip := extractClientIP(r)
		key += ":" + ip
	}

	// Fixed-window bucket
	windowSec := int64(cfg.Window.Seconds())
	if windowSec < 1 {
		windowSec = 1
	}
	bucket := time.Now().Unix() / windowSec
	key += ":" + strconv.FormatInt(bucket, 10)

	return key
}

// IPRateLimit returns middleware that limits requests per IP using a fixed window.
// Legacy helper kept for backward compatibility.
func IPRateLimit(cache *service.CacheService, window time.Duration, maxRequests int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cache == nil {
				next.ServeHTTP(w, r)
				return
			}
			ip := extractClientIP(r)
			now := time.Now().Unix()
			windowSec := int64(window.Seconds())
			key := "ratelimit:ip:" + ip + ":" + strconv.FormatInt(now/windowSec, 10)
			count, err := cache.Client().Incr(r.Context(), key).Result()
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			if count == 1 {
				cache.Client().Expire(r.Context(), key, window)
			}
			if count > int64(maxRequests) {
				w.Header().Set("Retry-After", strconv.Itoa(int(windowSec)))
				w.Header().Set("X-RateLimit-Limit", strconv.Itoa(maxRequests))
				w.Header().Set("X-RateLimit-Remaining", "0")
				http.Error(w, `{"detail":"Too many requests"}`, http.StatusTooManyRequests)
				return
			}
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(maxRequests-int(count)))
			next.ServeHTTP(w, r)
		})
	}
}

// PublicRateLimit is a stricter limiter for public endpoints (no auth).
func PublicRateLimit(cache *service.CacheService) func(http.Handler) http.Handler {
	return IPRateLimit(cache, 60*time.Second, 30)
}

// StrictRateLimit for auth endpoints.
func StrictRateLimit(cache *service.CacheService) func(http.Handler) http.Handler {
	return IPRateLimit(cache, 60*time.Second, 300)
}

func extractClientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		for i := 0; i < len(ip); i++ {
			if ip[i] == ',' {
				return ip[:i]
			}
		}
		return ip
	}
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	host := r.RemoteAddr
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			return host[:i]
		}
	}
	return host
}
