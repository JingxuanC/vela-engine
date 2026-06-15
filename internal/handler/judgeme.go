// Package handler provides HTTP handlers for the Vela AI API.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/internal/service/judgeme"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// JudgeMeHandler handles Judge.me integration admin endpoints.
type JudgeMeHandler struct {
	db          *gorm.DB
	cache       *service.CacheService
	syncService *judgeme.SyncService
}

// NewJudgeMeHandler creates a new JudgeMeHandler.
func NewJudgeMeHandler(db *gorm.DB, cache *service.CacheService, eb eventbus.EventBus) *JudgeMeHandler {
	return &JudgeMeHandler{
		db:          db,
		cache:       cache,
		syncService: judgeme.NewSyncService(db, eb),
	}
}

// --- DTOs ---

// JudgeMeConnectRequest is the request body for POST /api/integrations/judgeme/connect.
type JudgeMeConnectRequest struct {
	ShopID     string `json:"shop_id"`
	APIToken   string `json:"api_token"`
	ShopDomain string `json:"shop_domain"`
}

// JudgeMeStatusResponse is the response for GET /api/integrations/judgeme/status.
type JudgeMeStatusResponse struct {
	Success     bool       `json:"success"`
	Connected   bool       `json:"connected"`
	ShopDomain  string     `json:"shop_domain,omitempty"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
	LastSyncAt  *time.Time `json:"last_sync_at,omitempty"`
	IsActive    bool       `json:"is_active"`
	Error       string     `json:"error,omitempty"`
}

// JudgeMeLogsResponse is the paginated sync logs response.
type JudgeMeLogsResponse struct {
	Success bool                 `json:"success"`
	Logs    []JudgeMeSyncLogItem `json:"logs"`
	Total   int64                `json:"total"`
}

// JudgeMeSyncLogItem is a single sync log entry.
type JudgeMeSyncLogItem struct {
	ID           string `json:"id"`
	Action       string `json:"action"`
	Status       string `json:"status"`
	Message      string `json:"message"`
	ReviewsCount int    `json:"reviews_count"`
	CreatedAt    string `json:"created_at"`
}

// --- Handlers ---

// Connect handles POST /api/integrations/judgeme/connect.
// Saves the API Token + Shop Domain and tests the connection.
func (h *JudgeMeHandler) Connect(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	var req JudgeMeConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.ShopID == "" || req.APIToken == "" || req.ShopDomain == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id, api_token, and shop_domain are required")
		return
	}

	shopID, err := database.ResolveShopID(h.db, req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	// Test the connection by listing reviews from Judge.me
	client := judgeme.NewJudgeMeClient(req.APIToken, req.ShopDomain)
	_, err = client.GetReviews(r.Context(), judgeme.ListReviewsParams{Page: 1, PerPage: 1})
	if err != nil {
		slog.Warn("judgeme: connect test failed", "shop_id", shopID, "shop_domain", req.ShopDomain, "error", err)
		httputil.WriteError(w, http.StatusBadRequest, "Connection test failed: "+err.Error())
		return
	}

	// Upsert the Judge.me settings
	now := time.Now().UTC()
	setting := model.JudgeMeSetting{
		ShopID:      shopID,
		APIToken:    req.APIToken,
		ShopDomain:  req.ShopDomain,
		ConnectedAt: now,
		IsActive:    true,
	}

	result := h.db.WithContext(r.Context()).Where("shop_id = ?", shopID).Assign(setting).FirstOrCreate(&model.JudgeMeSetting{})
	if result.Error != nil {
		slog.Error("judgeme: failed to save settings", "shop_id", shopID, "error", result.Error)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to save settings")
		return
	}

	// Log the connection
	h.syncService.LogSync(r.Context(), shopID, "test_connection", "success", "Connection test successful", 0)

	slog.Info("judgeme: connected", "shop_id", shopID, "shop_domain", req.ShopDomain)
	httputil.WriteOK(w, map[string]interface{}{
		"success":     true,
		"connected":   true,
		"shop_domain": req.ShopDomain,
	})
}

// Disconnect handles DELETE /api/integrations/judgeme/disconnect.
func (h *JudgeMeHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	shopIDStr := r.URL.Query().Get("shop_id")
	if shopIDStr == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	shopID, err := database.ResolveShopID(h.db, shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	result := h.db.WithContext(r.Context()).Model(&model.JudgeMeSetting{}).
		Where("shop_id = ?", shopID).
		Update("is_active", false)

	if result.Error != nil {
		slog.Error("judgeme: failed to disconnect", "shop_id", shopID, "error", result.Error)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to disconnect")
		return
	}

	slog.Info("judgeme: disconnected", "shop_id", shopID)
	httputil.WriteOK(w, map[string]interface{}{
		"success":      true,
		"disconnected": true,
	})
}

// Status handles GET /api/integrations/judgeme/status.
func (h *JudgeMeHandler) Status(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	shopIDStr := r.URL.Query().Get("shop_id")
	if shopIDStr == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	shopID, err := database.ResolveShopID(h.db, shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	var setting model.JudgeMeSetting
	if err := h.db.WithContext(r.Context()).Where("shop_id = ?", shopID).First(&setting).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.WriteOK(w, JudgeMeStatusResponse{
				Success:   true,
				Connected: false,
			})
			return
		}
		slog.Error("judgeme: failed to get status", "shop_id", shopID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to get status")
		return
	}

	httputil.WriteOK(w, JudgeMeStatusResponse{
		Success:     true,
		Connected:   setting.IsActive,
		ShopDomain:  setting.ShopDomain,
		ConnectedAt: &setting.ConnectedAt,
		LastSyncAt:  setting.LastSyncAt,
		IsActive:    setting.IsActive,
	})
}

// Sync handles POST /api/integrations/judgeme/sync — triggers a full sync.
func (h *JudgeMeHandler) Sync(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	shopIDStr := r.URL.Query().Get("shop_id")
	if shopIDStr == "" {
		// Try from body
		var body struct {
			ShopID string `json:"shop_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body.ShopID != "" {
			shopIDStr = body.ShopID
		}
	}

	if shopIDStr == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	shopID, err := database.ResolveShopID(h.db, shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	// Run full sync in a goroutine with a detached context (request context cancels after response)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := h.syncService.FullSync(ctx, shopID); err != nil {
			slog.Error("judgeme: sync failed", "shop_id", shopID, "error", err)
		}
	}()

	slog.Info("judgeme: sync triggered", "shop_id", shopID)
	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"message": "Sync started in background",
	})
}

// Logs handles GET /api/integrations/judgeme/logs — returns sync logs.
func (h *JudgeMeHandler) Logs(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	shopIDStr := r.URL.Query().Get("shop_id")
	if shopIDStr == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	shopID, err := database.ResolveShopID(h.db, shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	var total int64
	h.db.WithContext(r.Context()).Model(&model.JudgeMeSyncLog{}).
		Where("shop_id = ?", shopID).
		Count(&total)

	var logs []model.JudgeMeSyncLog
	h.db.WithContext(r.Context()).Where("shop_id = ?", shopID).
		Order("created_at DESC").
		Limit(50).
		Find(&logs)

	items := make([]JudgeMeSyncLogItem, 0, len(logs))
	for _, l := range logs {
		items = append(items, JudgeMeSyncLogItem{
			ID:           l.ID.String(),
			Action:       l.Action,
			Status:       l.Status,
			Message:      l.Message,
			ReviewsCount: l.ReviewsCount,
			CreatedAt:    l.CreatedAt.Format(time.RFC3339),
		})
	}

	httputil.WriteOK(w, JudgeMeLogsResponse{
		Success: true,
		Logs:    items,
		Total:   total,
	})
}
