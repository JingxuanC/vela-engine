package middleware

import (
	"net/http"
	"os"
	"strings"
)

// InternalAuth verifies that the request comes from the trusted Remix backend
// by checking the X-Internal-Secret header against the INTERNAL_SECRET env var.
// In development mode, the check is skipped.
func InternalAuth(next http.Handler) http.Handler {
	secret := os.Getenv("INTERNAL_SECRET")
	if secret == "" {
		secret = "vela-dev-secret" // default for local development
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow health-check, metrics, and public endpoints without auth
		if r.URL.Path == "/" || r.URL.Path == "/health" || r.URL.Path == "/ready" || r.URL.Path == "/metrics" ||
			r.URL.Path == "/api/contact" || strings.HasPrefix(r.URL.Path, "/share/") {
			next.ServeHTTP(w, r)
			return
		}
		if os.Getenv("GO_ENV") == "development" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-Internal-Secret") != secret {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
