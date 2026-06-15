package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/JingxuanC/vela-engine/internal/config"
)

// CORS returns a CORS middleware configured for the environment.
func CORS(cfg *config.Config) func(http.Handler) http.Handler {
	origins := cfg.CORSAllowedOrigins()

	opts := cors.Options{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link", "X-RateLimit-Remaining", "Retry-After"},
		AllowCredentials: !cfg.IsDevelopment(),
		MaxAge:           300,
	}

	return cors.Handler(opts)
}

// Logging returns a structured request logging middleware using slog.
// Injects shop_id (from gateway context) and module (from URL path) into logs.
func Logging() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			// Extract shop_id from context (set by Gateway middleware)
			shopID := ShopIDFromContext(r.Context())

			// Parse module from URL path: /api/analytics/... → analytics
			module := extractModuleFromPath(r.URL.Path)

			defer func() {
				status := ww.Status()
				level := slog.LevelInfo
				if status >= 500 {
					level = slog.LevelError
				} else if status >= 400 {
					level = slog.LevelWarn
				}

				slog.Log(r.Context(), level, "request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", status,
					"duration_ms", time.Since(start).Milliseconds(),
					"bytes", ww.BytesWritten(),
					"remote_addr", r.RemoteAddr,
					"shop_id", shopID,
					"module", module,
				)
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

// extractModuleFromPath extracts the module name from a URL path.
// e.g. /api/analytics/dashboard → analytics
//
//	/api/inbox/conversations → inbox
//	/api/supply/stock-prediction → supply
//	/metrics → system
func extractModuleFromPath(path string) string {
	parts := splitPath(path)
	if len(parts) >= 2 && parts[0] == "api" {
		return parts[1]
	}
	if len(parts) >= 1 && parts[0] != "" {
		return parts[0]
	}
	return "unknown"
}

// splitPath is a simple path splitter that avoids importing strings for this one use.
func splitPath(path string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			if i > start {
				parts = append(parts, path[start:i])
			}
			start = i + 1
		}
	}
	if start < len(path) {
		parts = append(parts, path[start:])
	}
	return parts
}
