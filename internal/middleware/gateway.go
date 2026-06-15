// Package middleware provides HTTP middleware for the Vela AI API.
package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/config"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// ShopIDResolver resolves a shop_id string to a UUID.
// Used by the gateway to extract shop context from API requests.
type ShopIDResolver func(ctx context.Context, shopIDStr string) uuid.UUID

// Gateway returns middleware that enforces API key authentication,
// feature flag checks, and daily quota limits on /api/ routes.
//
// Auth: X-API-Key header (simple token, not Shopify OAuth).
// Public endpoints (/api/contact, /health) pass through unauthenticated.
//
// shopIDResolver is optional — pass nil if shop context is not needed.
func Gateway(cfg *config.Config, cache *service.CacheService, shopIDResolver ShopIDResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Public paths pass through
			if !strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/contact" {
				next.ServeHTTP(w, r)
				return
			}
			if r.URL.Path == "/api/contact" {
				next.ServeHTTP(w, r)
				return
			}

			ctx := r.Context()

			// ── Auth: X-API-Key header ────────────────────────────
			apiKey := r.Header.Get("X-API-Key")
			if apiKey == "" {
				apiKey = extractBearerToken(r)
			}
			if cfg.APIToken != "" && apiKey != cfg.APIToken {
				httputil.WriteError(w, http.StatusUnauthorized, "Unauthorized")
				return
			}

			// ── Feature flag check ────────────────────────────────
			feature := extractFeatureFromPath(r.URL.Path)
			if feature != "" {
				flagKey := "feature:" + feature
				flagValue, err := cache.Get(ctx, flagKey)
				if err != nil {
					slog.Warn("feature flag check failed, failing open",
						"path", r.URL.Path, "feature", feature, "error", err)
				} else if flagValue != "" && isDisabled(flagValue) {
					httputil.WriteOK(w, map[string]interface{}{
						"success":  false,
						"disabled": true,
						"feature":  feature,
						"message":  "Feature '" + feature + "' is currently disabled.",
					})
					return
				}
			}

			// ── Cross-tenant check ────────────────────────────────
			shopFromHeader := r.Header.Get("X-Shop-ID")
			shopFromQuery := r.URL.Query().Get("shop_id")
			if shopFromHeader != "" && shopFromQuery != "" && shopFromHeader != shopFromQuery {
				slog.Warn("gateway: cross-tenant access denied",
					"header_shop", shopFromHeader,
					"query_shop", shopFromQuery,
					"path", r.URL.Path)
				httputil.WriteError(w, http.StatusForbidden, "Cross-tenant access denied")
				return
			}

			effectiveShopID := shopFromHeader
			if effectiveShopID == "" {
				effectiveShopID = shopFromQuery
			}
			if effectiveShopID != "" {
				if _, err := uuid.Parse(effectiveShopID); err != nil {
					effectiveShopID = "00000000-0000-0000-0000-000000000001" // default
				}
				ctx = context.WithValue(ctx, ctxKeyShopID, effectiveShopID)
			}

			// ── Quota check ───────────────────────────────────────
			if !cfg.IsDevelopment() && feature != "" {
				limit := service.DefaultQuotaLimits[feature]
				if limit == 0 {
					limit = 100
				}
				today := time.Now().Format("20060102")
				quotaKey := feature
				if effectiveShopID != "" {
					quotaKey = effectiveShopID + ":" + feature
				}
				allowed, count, err := cache.CheckQuota(ctx, quotaKey, today, limit)
				if err != nil {
					slog.Warn("quota check failed, failing open", "path", r.URL.Path, "feature", feature, "error", err)
				} else if !allowed {
					slog.Info("quota exceeded", "feature", feature, "count", count, "limit", limit)
					w.Header().Set("Retry-After", "86400")
					httputil.WriteError(w, http.StatusTooManyRequests,
						"Daily quota exceeded for '"+feature+"'. Limit: "+itoa(limit)+". Please upgrade your plan.")
					return
				}
			}

			ctx = context.WithValue(ctx, ctxKeyFeature, feature)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return auth[7:]
	}
	return ""
}

func extractFeatureFromPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "api" {
		return parts[1]
	}
	return ""
}

func isDisabled(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return lower == "0" || lower == "false" || lower == "disabled"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

// ── Context keys ──

type ctxKey string

const ctxKeyFeature ctxKey = "feature"
const ctxKeyShopID ctxKey = "shop_id"

func FeatureFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyFeature).(string); ok {
		return v
	}
	return ""
}

func ShopIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyShopID).(string); ok {
		return v
	}
	return ""
}
