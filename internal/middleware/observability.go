package middleware

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/JingxuanC/vela-engine/internal/platform/observability"
)

// Observability returns a middleware that records HTTP metrics and injects trace IDs.
func Observability(collector *observability.Collector) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			collector.IncActiveRequests()

			// Inject trace_id into context (reuses chi RequestID)
			traceID := middleware.GetReqID(r.Context())
			if traceID == "" {
				traceID = fmt.Sprintf("req_%d", time.Now().UnixNano())
			}
			ctx := observability.WithTraceID(r.Context(), traceID)

			// Set DB source for GORM metrics
			ctx = observability.WithDBSource(ctx, fmt.Sprintf("%s:%s", r.Method, cleanPath(r.URL.Path)))

			// Proceed with enriched context
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r.WithContext(ctx))

			durationMs := float64(time.Since(start).Microseconds()) / 1000.0
			collector.RecordRequest(r.Method, r.URL.Path, rec.status, durationMs)
		})
	}
}

// statusRecorder wraps http.ResponseWriter to capture HTTP status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher for SSE streaming support.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// cleanPath normalizes a URL path for metric labels (removes dynamic segments).
func cleanPath(path string) string {
	// Replace UUIDs and numeric IDs with placeholders
	cleaned := path
	for i := 0; i < len(cleaned)-8; i++ {
		if isHex(cleaned[i]) && cleaned[i+8] == '-' && isHex(cleaned[i+9]) {
			cleaned = cleaned[:i] + ":id" + cleaned[i+36:]
			break
		}
	}
	// Truncate to 50 chars for label cardinality
	if len(cleaned) > 50 {
		cleaned = cleaned[:50]
	}
	return cleaned
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
