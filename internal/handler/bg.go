package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/JingxuanC/vela-engine/internal/middleware"
)

// bgTimeout returns a background context with a deadline for fire-and-forget goroutines.
// The caller must not cancel the returned context — it self-expires after the deadline.
// Use this instead of raw context.Background() to prevent goroutine leaks under load.
func bgTimeout(d time.Duration) context.Context {
	ctx, _ := context.WithTimeout(context.Background(), d)
	return ctx
}

// shopIDFromRequest extracts the shop ID from the request, using the gateway-injected
// context value first (which has been validated against X-Shop-ID header), falling back
// to the query parameter for backward compatibility with non-/api/ paths.
func shopIDFromRequest(r *http.Request) string {
	if sid := middleware.ShopIDFromContext(r.Context()); sid != "" {
		return sid
	}
	return r.URL.Query().Get("shop_id")
}
