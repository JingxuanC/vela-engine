package middleware

import "net/http"

// ConcurrencyLimit limits the number of concurrent requests.
func ConcurrencyLimit(maxConcurrent int) func(http.Handler) http.Handler {
	sem := make(chan struct{}, maxConcurrent)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				next.ServeHTTP(w, r)
			default:
				http.Error(w, `{"detail":"server too busy"}`, http.StatusServiceUnavailable)
			}
		})
	}
}
