package middleware

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
)

// Audit returns a middleware that automatically writes audit logs for all /api/* requests.
// It extracts shop_id from the request context (set by Gateway middleware),
// derives the action from the HTTP method + path, and records the response status.
// In dev mode or when the DB is nil, the middleware passes through silently.
func Audit(db *gorm.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only audit /api/ paths
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				next.ServeHTTP(w, r)
				return
			}

			// Skip contact endpoint (no shop context)
			if r.URL.Path == "/api/contact" {
				next.ServeHTTP(w, r)
				return
			}

			// Capture the response status code
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			// Get shop_id from context (set by Gateway middleware)
			shopIDStr := ShopIDFromContext(r.Context())

			// Capture before time
			start := time.Now()

			// Serve the request
			next.ServeHTTP(rec, r)

			// Write audit log asynchronously (don't block the response)
			if db != nil && shopIDStr != "" {
				go writeAuditLog(db, shopIDStr, r, rec.status, start)
			}
		})
	}
}

// writeAuditLog writes a single audit log entry to the database.
func writeAuditLog(db *gorm.DB, shopIDStr string, r *http.Request, statusCode int, start time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	shopID, err := uuid.Parse(shopIDStr)
	if err != nil {
		slog.Warn("audit: invalid shop_id in context, skipping", "shop_id", shopIDStr)
		return
	}

	// Derive action from HTTP method + cleaned path
	action := deriveAction(r.Method, r.URL.Path)

	// Resource is the full path + query
	resource := r.URL.Path
	if r.URL.RawQuery != "" {
		resource += "?" + r.URL.RawQuery
	}

	// Determine status
	status := "success"
	if statusCode >= 400 {
		status = "error"
	}

	// Build detail JSON
	detail := map[string]interface{}{
		"method":      r.Method,
		"path":        r.URL.Path,
		"query":       r.URL.RawQuery,
		"status_code": statusCode,
		"ip":          extractClientIP(r),
		"user_agent":  r.UserAgent(),
		"duration_ms": time.Since(start).Milliseconds(),
	}
	detailBytes, _ := json.Marshal(detail)

	log := model.AuditLog{
		ShopID:   shopID,
		Action:   action,
		Resource: resource,
		Status:   status,
		Detail:   string(detailBytes),
	}

	if err := db.WithContext(ctx).Create(&log).Error; err != nil {
		slog.Error("audit: failed to write audit log", "shop_id", shopID, "action", action, "error", err)
	}
}

// deriveAction generates a human-readable action string from HTTP method and path.
// Examples:
//
//	POST /api/billing/subscribe    → "billing.subscribe"
//	GET  /api/shop/billing-status  → "shop.billing_status"
//	POST /api/inbox/conversations/.../reply → "inbox.reply"
func deriveAction(method, path string) string {
	// Remove /api/ prefix
	trimmed := strings.TrimPrefix(path, "/api/")

	// Split by "/" and filter out dynamic segments (UUIDs, numeric IDs)
	parts := strings.Split(trimmed, "/")
	var filtered []string
	for _, p := range parts {
		if p == "" {
			continue
		}
		// Skip UUID-like segments (36 chars with dashes)
		if len(p) == 36 && strings.Count(p, "-") == 4 {
			// Try to parse as UUID to confirm
			if _, err := uuid.Parse(p); err == nil {
				continue
			}
		}
		// Skip purely numeric segments
		isNumeric := true
		for _, c := range p {
			if c < '0' || c > '9' {
				isNumeric = false
				break
			}
		}
		if isNumeric && len(p) > 0 {
			continue
		}
		filtered = append(filtered, p)
	}

	// Build action: join with "."
	action := strings.Join(filtered, ".")

	// Prepend method for mutation operations
	switch method {
	case http.MethodPost:
		action = action + ".create"
	case http.MethodPut, http.MethodPatch:
		action = action + ".update"
	case http.MethodDelete:
		action = action + ".delete"
	}

	return action
}

// WriteAuditEntry writes a custom audit log entry (for use by handlers directly).
func WriteAuditEntry(ctx context.Context, db *gorm.DB, shopID uuid.UUID, action, resource, status, detail string) {
	if db == nil {
		return
	}
	entry := model.AuditLog{
		ShopID:   shopID,
		Action:   action,
		Resource: resource,
		Status:   status,
		Detail:   detail,
	}
	if err := db.WithContext(ctx).Create(&entry).Error; err != nil {
		slog.Error("audit: failed to write custom audit entry", "shop_id", shopID, "action", action, "error", err)
	}
}
