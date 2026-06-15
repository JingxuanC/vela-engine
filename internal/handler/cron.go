// Package handler provides HTTP handlers for the Vela AI API.
package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/externaldata"
	"github.com/JingxuanC/vela-engine/internal/platform/insight"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/internal/platform/sync"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/internal/service/judgeme"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// CronHandler handles cron/scheduled task endpoints for background sync operations.
type CronHandler struct {
	productSyncer   *sync.ProductSyncer
	orderSyncer     *sync.OrderSyncer
	customerSyncer  *sync.CustomerSyncer
	db              *gorm.DB
	cache           *service.CacheService
	judgemeSync     *judgeme.SyncService
	llmRouter       *service.LLMRouter
	webhookHandler  *WebhookHandler
	eventBus        eventbus.EventBus
	insightEngine   *insight.InsightEngine
	trendsClient    *externaldata.GoogleTrendsClient
	eccompassClient *externaldata.ECCompassClient
}

func NewCronHandler(db *gorm.DB, cache *service.CacheService, llmRouter *service.LLMRouter, webhookHandler *WebhookHandler, productSyncer *sync.ProductSyncer, orderSyncer *sync.OrderSyncer, customerSyncer *sync.CustomerSyncer, bus eventbus.EventBus, insightEngine *insight.InsightEngine, trendsClient *externaldata.GoogleTrendsClient, eccompassClient *externaldata.ECCompassClient) *CronHandler {
	return &CronHandler{
		db: db, cache: cache,
		judgemeSync:     judgeme.NewSyncService(db, bus),
		llmRouter:       llmRouter,
		webhookHandler:  webhookHandler,
		productSyncer:   productSyncer,
		orderSyncer:     orderSyncer,
		customerSyncer:  customerSyncer,
		eventBus:        bus,
		insightEngine:   insightEngine,
		trendsClient:    trendsClient,
		eccompassClient: eccompassClient,
	}
}

// --- Response DTO ---

type cronResponse struct {
	Success  bool        `json:"success"`
	Message  string      `json:"message"`
	TaskName string      `json:"task_name"`
	Data     interface{} `json:"data,omitempty"`
}

func writeCronOK(w http.ResponseWriter, taskName, message string, data interface{}) {
	httputil.WriteOK(w, cronResponse{
		Success:  true,
		Message:  message,
		TaskName: taskName,
		Data:     data,
	})
}

func writeCronError(w http.ResponseWriter, status int, taskName, message string) {
	httputil.WriteError(w, status, fmt.Sprintf("[%s] %s", taskName, message))
}

// --- lastSync helpers ---

func (h *CronHandler) getLastSync(shopID uuid.UUID, syncType string) time.Time {
	if h.cache == nil {
		return time.Now().UTC().Add(-15 * time.Minute)
	}
	key := fmt.Sprintf("last_sync:%s:%s", syncType, shopID.String())
	val, err := h.cache.Client().Get(bgTimeout(10*time.Second), key).Result()
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, val)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (h *CronHandler) setLastSync(shopID uuid.UUID, syncType string) {
	if h.cache == nil {
		return
	}
	key := fmt.Sprintf("last_sync:%s:%s", syncType, shopID.String())
	h.cache.Client().Set(bgTimeout(10*time.Second), key, time.Now().UTC().Format(time.RFC3339), 24*time.Hour)
}

// --- 2. SyncProducts: POST /api/cron/sync-products ---
// Incremental product sync — stub for now.
func (h *CronHandler) SyncProducts(w http.ResponseWriter, r *http.Request) {
	taskName := "sync-products"
	start := time.Now()

	if h.db == nil {
		writeCronError(w, http.StatusServiceUnavailable, taskName, "Database not available")
		return
	}

	shopID, err := resolveShopIDFromRequest(h.db, r)
	if err != nil {
		writeCronError(w, http.StatusBadRequest, taskName, err.Error())
		return
	}

	lastSync := h.getLastSync(shopID, "products")
	now := time.Now().UTC()

	// Skip if synced recently (within last 10 min)
	if !lastSync.IsZero() && now.Sub(lastSync) < 10*time.Minute {
		writeCronOK(w, taskName, fmt.Sprintf("Skipped — last sync was at %s (less than 10 min ago)", lastSync.Format(time.RFC3339)), map[string]interface{}{
			"shop_id":      shopID.String(),
			"last_sync_at": lastSync.Format(time.RFC3339),
			"skipped":      true,
		})
		return
	}

	slog.Info("cron", "job", "sync_products", "shop_id", shopID, "action", "start", "last_sync", lastSync.Format(time.RFC3339))

	result, err := h.productSyncer.Sync(r.Context(), sync.SyncOptions{
		ShopID: shopID, Since: lastSync, Limit: 250,
	})
	durationMs := time.Since(start).Milliseconds()
	if err != nil {
		slog.Error("cron", "job", "sync_products", "shop_id", shopID, "status", "error", "duration_ms", durationMs, "error", err)
		writeCronError(w, http.StatusInternalServerError, taskName, err.Error())
		return
	}
	h.setLastSync(shopID, "products")
	slog.Info("cron", "job", "sync_products", "shop_id", shopID, "status", "success", "count", result.TotalRecords, "new", result.NewRecords, "updated", result.UpdatedRecords, "duration_ms", durationMs)
	// EventBus events are published inside productSyncer.Sync() — no duplicate here
	writeCronOK(w, taskName, fmt.Sprintf("Synced %d products (new=%d updated=%d)", result.TotalRecords, result.NewRecords, result.UpdatedRecords), map[string]interface{}{
		"shop_id": shopID.String(), "synced": result.TotalRecords,
		"new": result.NewRecords, "updated": result.UpdatedRecords,
		"duration_ms": result.DurationMs,
	})
}

// SyncOrders handles POST /api/cron/sync-orders
func (h *CronHandler) SyncOrders(w http.ResponseWriter, r *http.Request) {
	taskName := "sync-orders"
	start := time.Now()
	if h.db == nil {
		writeCronError(w, http.StatusServiceUnavailable, taskName, "Database not available")
		return
	}
	if h.orderSyncer == nil {
		writeCronError(w, http.StatusServiceUnavailable, taskName, "Order syncer not configured")
		return
	}
	shopID, err := resolveShopIDFromRequest(h.db, r)
	if err != nil {
		writeCronError(w, http.StatusBadRequest, taskName, err.Error())
		return
	}
	lastSync := h.getLastSync(shopID, "orders")
	now := time.Now().UTC()
	if !lastSync.IsZero() && now.Sub(lastSync) < 10*time.Minute {
		writeCronOK(w, taskName, fmt.Sprintf("Skipped — last sync at %s", lastSync.Format(time.RFC3339)), map[string]interface{}{"shop_id": shopID.String(), "skipped": true})
		return
	}
	h.setLastSync(shopID, "orders")
	slog.Info("cron", "job", "sync_orders", "shop_id", shopID, "action", "start")
	result, err := h.orderSyncer.Sync(r.Context(), sync.SyncOptions{ShopID: shopID, Since: lastSync, Limit: 250})
	durationMs := time.Since(start).Milliseconds()
	if err != nil {
		slog.Error("cron", "job", "sync_orders", "shop_id", shopID, "status", "error", "duration_ms", durationMs, "error", err)
		writeCronError(w, http.StatusInternalServerError, taskName, err.Error())
		return
	}
	slog.Info("cron", "job", "sync_orders", "shop_id", shopID, "status", "success", "count", result.TotalRecords, "new", result.NewRecords, "updated", result.UpdatedRecords, "duration_ms", durationMs)
	// EventBus events are published inside orderSyncer.Sync() — no duplicate here
	writeCronOK(w, taskName, fmt.Sprintf("Synced %d orders (new=%d updated=%d)", result.TotalRecords, result.NewRecords, result.UpdatedRecords), map[string]interface{}{
		"shop_id": shopID.String(), "synced": result.TotalRecords, "new": result.NewRecords, "updated": result.UpdatedRecords, "duration_ms": result.DurationMs,
	})
}

// --- 3. SyncReviews: POST /api/cron/sync-reviews ---
// Full review sync from Judge.me — loops over all active JudgeMe shops.
func (h *CronHandler) SyncReviews(w http.ResponseWriter, r *http.Request) {
	taskName := "sync-reviews"
	start := time.Now()

	if h.db == nil {
		writeCronError(w, http.StatusServiceUnavailable, taskName, "Database not available")
		return
	}

	// Find all active shops with Judge.me connected
	var settings []model.JudgeMeSetting
	if err := h.db.WithContext(r.Context()).
		Where("is_active = ?", true).
		Find(&settings).Error; err != nil {
		slog.Error("cron", "job", "sync_reviews", "status", "error", "error", err)
		writeCronError(w, http.StatusInternalServerError, taskName, "Failed to query Judge.me settings")
		return
	}

	if len(settings) == 0 {
		writeCronOK(w, taskName, "No active Judge.me shops found, nothing to sync", map[string]interface{}{
			"shops_found": 0,
		})
		return
	}

	type shopResult struct {
		ShopID     string `json:"shop_id"`
		ShopDomain string `json:"shop_domain"`
		Status     string `json:"status"`
		Count      int    `json:"reviews_synced"`
		Error      string `json:"error,omitempty"`
	}

	results := make([]shopResult, 0, len(settings))
	successCount := 0
	failCount := 0

	for _, setting := range settings {
		// Check last sync — skip if less than 1 hour ago
		lastSync := h.getLastSync(setting.ShopID, "reviews")
		if !lastSync.IsZero() && time.Since(lastSync) < 1*time.Hour {
			results = append(results, shopResult{
				ShopID:     setting.ShopID.String(),
				ShopDomain: setting.ShopDomain,
				Status:     "skipped",
				Count:      0,
				Error:      "synced within last hour",
			})
			continue
		}

		// Run full sync in foreground for this request
		slog.Info("cron", "job", "sync_reviews", "shop_id", setting.ShopID, "shop_domain", setting.ShopDomain, "action", "start")

		err := h.judgemeSync.FullSync(r.Context(), setting.ShopID)
		if err != nil {
			slog.Error("cron", "job", "sync_reviews", "shop_id", setting.ShopID, "status", "error", "error", err)
			results = append(results, shopResult{
				ShopID:     setting.ShopID.String(),
				ShopDomain: setting.ShopDomain,
				Status:     "failed",
				Count:      0,
				Error:      err.Error(),
			})
			failCount++
		} else {
			h.setLastSync(setting.ShopID, "reviews")
			results = append(results, shopResult{
				ShopID:     setting.ShopID.String(),
				ShopDomain: setting.ShopDomain,
				Status:     "success",
				Count:      0, // The actual count is logged by FullSync
			})
			successCount++
		}
	}

	durationMs := time.Since(start).Milliseconds()
	slog.Info("cron", "job", "sync_reviews", "shops_total", len(settings), "shops_success", successCount, "shops_failed", failCount, "duration_ms", durationMs)

	writeCronOK(w, taskName, fmt.Sprintf("Synced reviews for %d shops (%d success, %d failed, %d skipped)",
		len(results), successCount, failCount, len(results)-successCount-failCount),
		map[string]interface{}{
			"shops_total":   len(settings),
			"shops_synced":  successCount,
			"shops_failed":  failCount,
			"shops_skipped": len(results) - successCount - failCount,
			"results":       results,
		})
}


// --- 3.5 SyncCustomers: POST /api/cron/sync-customers ---
func (h *CronHandler) SyncCustomers(w http.ResponseWriter, r *http.Request) {
	taskName := "sync-customers"
	start := time.Now()
	if h.db == nil {
		writeCronError(w, http.StatusServiceUnavailable, taskName, "Database not available")
		return
	}
	shopID, err := resolveShopIDFromRequest(h.db, r)
	if err != nil {
		writeCronError(w, http.StatusBadRequest, taskName, err.Error())
		return
	}
	lastSync := h.getLastSync(shopID, "customers")
	now := time.Now().UTC()
	// Customers change less frequently — skip if synced within 60 min
	if !lastSync.IsZero() && now.Sub(lastSync) < 60*time.Minute {
		writeCronOK(w, taskName, fmt.Sprintf("Skipped — last sync at %s", lastSync.Format(time.RFC3339)),
			map[string]interface{}{"shop_id": shopID.String(), "last_sync_at": lastSync.Format(time.RFC3339), "skipped": true})
		return
	}
	if h.customerSyncer == nil {
		writeCronError(w, http.StatusServiceUnavailable, taskName, "Customer Syncer not available")
		return
	}
	slog.Info("cron", "job", "sync_customers", "shop_id", shopID, "action", "start")

	result, err := h.customerSyncer.Sync(r.Context(), sync.SyncOptions{
		ShopID: shopID, Since: lastSync, Limit: 250,
	})
	durationMs := time.Since(start).Milliseconds()
	if err != nil {
		slog.Error("cron", "job", "sync_customers", "shop_id", shopID, "status", "error", "duration_ms", durationMs, "error", err)
		writeCronError(w, http.StatusInternalServerError, taskName, err.Error())
		return
	}
	h.setLastSync(shopID, "customers")
	slog.Info("cron", "job", "sync_customers", "shop_id", shopID, "status", "success", "count", result.TotalRecords, "new", result.NewRecords, "updated", result.UpdatedRecords, "duration_ms", durationMs)
	// EventBus events are published inside customerSyncer.Sync() — no duplicate here
	writeCronOK(w, taskName, fmt.Sprintf("Synced %d customers (new=%d updated=%d)", result.TotalRecords, result.NewRecords, result.UpdatedRecords),
		map[string]interface{}{"shop_id": shopID.String(), "synced": result.TotalRecords, "new": result.NewRecords, "updated": result.UpdatedRecords, "duration_ms": result.DurationMs})
}
// --- 4. ProcessAutoReply: POST /api/cron/process-auto-reply ---
// Processes pending auto-reply records that are ready to be sent automatically.
func (h *CronHandler) ProcessAutoReply(w http.ResponseWriter, r *http.Request) {
	taskName := "process-auto-reply"
	start := time.Now()

	if h.db == nil {
		writeCronError(w, http.StatusServiceUnavailable, taskName, "Database not available")
		return
	}

	// Query pending reply records where send_mode = "auto" (auto-send configured)
	var pendingRecords []model.ReplyRecord
	if err := h.db.WithContext(r.Context()).
		Where("status = ? AND send_mode = ?", "pending", "auto").
		Find(&pendingRecords).Error; err != nil {
		slog.Error("cron", "job", "process_auto_reply", "status", "error", "error", err)
		writeCronError(w, http.StatusInternalServerError, taskName, "Failed to query pending records")
		return
	}

	if len(pendingRecords) == 0 {
		writeCronOK(w, taskName, "No pending auto-send reply records found", map[string]interface{}{
			"total":         0,
			"auto_sent":     0,
			"still_waiting": 0,
		})
		return
	}

	autoSent := 0
	stillWaiting := 0

	for _, record := range pendingRecords {
		now := time.Now().UTC()
		updates := map[string]interface{}{
			"status":  "sent",
			"sent_at": &now,
		}

		// If no final reply selected, use first AI reply
		if record.FinalReply == "" {
			var replies []string
			if err := json.Unmarshal(record.AiReplies, &replies); err == nil && len(replies) > 0 {
				updates["final_reply"] = replies[0]
			}
		}

		if err := h.db.WithContext(r.Context()).Model(&record).Updates(updates).Error; err != nil {
			slog.Error("cron", "job", "process_auto_reply", "record_id", record.ID, "status", "error", "error", err)
			stillWaiting++
		} else {
			autoSent++
			slog.Info("cron", "job", "process_auto_reply", "record_id", record.ID, "shop_id", record.ShopID, "status", "sent")
		}
	}

	durationMs := time.Since(start).Milliseconds()
	slog.Info("cron", "job", "process_auto_reply", "total", len(pendingRecords), "sent", autoSent, "failed", stillWaiting, "duration_ms", durationMs)

	writeCronOK(w, taskName, fmt.Sprintf("Processed %d pending records: %d auto-sent, %d still waiting",
		len(pendingRecords), autoSent, stillWaiting),
		map[string]interface{}{
			"total":         len(pendingRecords),
			"auto_sent":     autoSent,
			"still_waiting": stillWaiting,
		})
}

// --- 5. RunInsights: POST /api/cron/run-insights ---
// Runs the InsightEngine analysis for a given shop.
func (h *CronHandler) RunInsights(w http.ResponseWriter, r *http.Request) {
	taskName := "run-insights"
	start := time.Now()

	if h.insightEngine == nil {
		writeCronError(w, http.StatusServiceUnavailable, taskName, "Insight engine not configured")
		return
	}

	shopID, err := resolveShopIDFromRequest(h.db, r)
	if err != nil {
		writeCronError(w, http.StatusBadRequest, taskName, err.Error())
		return
	}

	slog.Info("cron", "job", "run_insights", "shop_id", shopID, "action", "start")
	insights, err := h.insightEngine.Analyze(r.Context(), shopID.String())
	durationMs := time.Since(start).Milliseconds()
	if err != nil {
		slog.Error("cron", "job", "run_insights", "shop_id", shopID, "status", "error", "duration_ms", durationMs, "error", err)
		writeCronError(w, http.StatusInternalServerError, taskName, err.Error())
		return
	}

	slog.Info("cron", "job", "run_insights", "shop_id", shopID, "status", "success", "insight_count", len(insights), "duration_ms", durationMs)

	writeCronOK(w, taskName, fmt.Sprintf("Generated %d insights", len(insights)), map[string]interface{}{
		"shop_id":       shopID.String(),
		"insight_count": len(insights),
		"insights":      insights,
	})
}

// --- Helpers ---

// resolveShopIDFromRequest extracts shop_id from query param or JSON body.
func resolveShopIDFromRequest(_ *gorm.DB, r *http.Request) (uuid.UUID, error) {
	shopIDStr := r.URL.Query().Get("shop_id")
	if shopIDStr != "" {
		return uuid.Parse(shopIDStr)
	}

	// Try from body
	var body struct {
		ShopID string `json:"shop_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body.ShopID != "" {
		return uuid.Parse(body.ShopID)
	}

	return uuid.Nil, fmt.Errorf("shop_id is required (provide as query param or in JSON body)")
}
