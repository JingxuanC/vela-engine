package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// NotificationsHandler handles notification preferences and sending.
type NotificationsHandler struct {
	db *gorm.DB
}

// NewNotificationsHandler creates a new NotificationsHandler with DB integration.
func NewNotificationsHandler(db *gorm.DB) *NotificationsHandler {
	return &NotificationsHandler{db: db}
}

// NotificationPrefs holds a shop's notification preferences.
type NotificationPrefs struct {
	ShopID             string `json:"shop_id"`
	EmailReturnUpdates bool   `json:"email_return_updates"`
	EmailUsageAlerts   bool   `json:"email_usage_alerts"`
	EmailMarketing     bool   `json:"email_marketing"`
	EmailReviewInvites bool   `json:"email_review_invites"`
	InAppNotifications bool   `json:"in_app_notifications"`
}

// GetPrefs handles GET /api/notifications/preferences.
func (h *NotificationsHandler) GetPrefs(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteOK(w, defaultNotifPrefs(""))
		return
	}

	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteOK(w, defaultNotifPrefs(""))
		return
	}

	id, err := database.ResolveShopID(h.db, shopID)
	if err != nil {
		httputil.WriteOK(w, defaultNotifPrefs(shopID))
		return
	}

	var prefs model.NotificationPrefs
	if err := h.db.WithContext(r.Context()).Where("shop_id = ?", id).First(&prefs).Error; err != nil {
		httputil.WriteOK(w, defaultNotifPrefs(shopID))
		return
	}

	httputil.WriteOK(w, NotificationPrefs{
		ShopID:             prefs.ShopID.String(),
		EmailReturnUpdates: prefs.EmailReturnUpdates,
		EmailUsageAlerts:   prefs.EmailUsageAlerts,
		EmailMarketing:     prefs.EmailMarketing,
		EmailReviewInvites: prefs.EmailReviewInvites,
		InAppNotifications: prefs.InAppNotifications,
	})
}

// UpdatePrefs handles PUT /api/notifications/preferences.
func (h *NotificationsHandler) UpdatePrefs(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	var req NotificationPrefs
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	shopID, err := database.ResolveShopID(h.db, req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop ID: "+err.Error())
		return
	}

	// Atomic upsert — FirstOrCreate + Assign is race-free
	prefs := model.NotificationPrefs{
		ShopID: shopID,
	}
	if err := h.db.WithContext(r.Context()).
		Where("shop_id = ?", shopID).
		Assign(model.NotificationPrefs{
			EmailReturnUpdates: req.EmailReturnUpdates,
			EmailUsageAlerts:   req.EmailUsageAlerts,
			EmailMarketing:     req.EmailMarketing,
			EmailReviewInvites: req.EmailReviewInvites,
			InAppNotifications: req.InAppNotifications,
		}).
		FirstOrCreate(&prefs).Error; err != nil {
		slog.Error("failed to upsert notification prefs", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to save preferences")
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"message": "Notification preferences updated",
	})
}

// SetPrefs handles POST /api/notifications/preferences.
func (h *NotificationsHandler) SetPrefs(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	var req NotificationPrefs
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	shopID, err := database.ResolveShopID(h.db, req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop ID: "+err.Error())
		return
	}

	// Atomic upsert
	prefs := model.NotificationPrefs{
		ShopID: shopID,
	}
	if err := h.db.WithContext(r.Context()).
		Where("shop_id = ?", shopID).
		Assign(model.NotificationPrefs{
			EmailReturnUpdates: req.EmailReturnUpdates,
			EmailUsageAlerts:   req.EmailUsageAlerts,
			EmailMarketing:     req.EmailMarketing,
			EmailReviewInvites: req.EmailReviewInvites,
			InAppNotifications: req.InAppNotifications,
		}).
		FirstOrCreate(&prefs).Error; err != nil {
		slog.Error("failed to upsert notification prefs", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to save preferences")
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"message": "Notification preferences updated",
	})
}

// ListInApp handles GET /api/notifications/in-app?shop_id=xxx&unread_only=true
func (h *NotificationsHandler) ListInApp(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteOK(w, map[string]interface{}{"notifications": []interface{}{}, "unread": 0})
		return
	}

	shopIDStr := r.URL.Query().Get("shop_id")
	if shopIDStr == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	shopID, err := database.ResolveShopID(h.db, shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid shop_id")
		return
	}

	unreadOnly := r.URL.Query().Get("unread_only") == "true"
	q := h.db.WithContext(r.Context()).
		Where("shop_id = ?", shopID).
		Order("created_at DESC").
		Limit(50)

	if unreadOnly {
		q = q.Where("is_read = ?", false)
	}

	var items []model.InAppNotification
	q.Find(&items)

	var unreadCount int64
	h.db.WithContext(r.Context()).Model(&model.InAppNotification{}).
		Where("shop_id = ? AND is_read = ?", shopID, false).
		Count(&unreadCount)

	notifs := make([]map[string]interface{}, len(items))
	for i, n := range items {
		notifs[i] = map[string]interface{}{
			"id":         n.ID,
			"type":       n.Type,
			"title":      n.Title,
			"body":       n.Body,
			"action_url": n.ActionURL,
			"priority":   n.Priority,
			"is_read":    n.IsRead,
			"created_at": n.CreatedAt,
		}
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success":        true,
		"notifications":  notifs,
		"unread_count":   unreadCount,
	})
}

// MarkRead handles PUT /api/notifications/in-app/{id}/read
func (h *NotificationsHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	idStr := r.URL.Query().Get("id")
	if idStr == "" {
		httputil.WriteError(w, http.StatusBadRequest, "notification id is required")
		return
	}

	if err := h.db.WithContext(r.Context()).
		Model(&model.InAppNotification{}).
		Where("id = ?", idStr).
		Update("is_read", true).Error; err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to mark as read")
		return
	}

	httputil.WriteOK(w, map[string]interface{}{"success": true})
}

func defaultNotifPrefs(shopID string) NotificationPrefs {
	return NotificationPrefs{
		ShopID:             shopID,
		EmailReturnUpdates: true,
		EmailUsageAlerts:   true,
		EmailMarketing:     false,
		EmailReviewInvites: true,
		InAppNotifications: true,
	}
}
