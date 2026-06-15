package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/JingxuanC/vela-engine/internal/config"
	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/internal/platform/insight"
	"github.com/JingxuanC/vela-engine/internal/platform/vertical"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/internal/service/tryon"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"github.com/JingxuanC/vela-engine/pkg/sse"
	"gorm.io/gorm"
)

// TryOnHandler handles virtual try-on API endpoints.
type TryOnHandler struct {
	cache       *service.CacheService
	client      *tryon.AliyunOutfitClient
	pipeline    *tryon.PipelineOrchestrator
	cfg         *config.Config
	db          *gorm.DB
	injector    *insight.ContextInjector
	eventBus    eventbus.EventBus
	sseSem      chan struct{}
	pipelineSem chan struct{}
}

// NewTryOnHandler creates a new TryOnHandler.
func NewTryOnHandler(cache *service.CacheService, client *tryon.AliyunOutfitClient, imgProc *tryon.ImageProcessor, storage *service.StorageService, cfg *config.Config, db *gorm.DB, inj *insight.ContextInjector) *TryOnHandler {
	return &TryOnHandler{
		cache:       cache,
		client:      client,
		pipeline:    tryon.NewPipelineOrchestrator(cache, client, imgProc, storage),
		cfg:         cfg,
		db:          db,
		injector:    inj,
		sseSem:      make(chan struct{}, cfg.MaxConcurrentSSE),
		pipelineSem: make(chan struct{}, cfg.MaxConcurrentSSE),
	}
}

// SetEventBus sets the optional event bus for AI event publishing.
func (h *TryOnHandler) SetEventBus(bus eventbus.EventBus) { h.eventBus = bus }

// TryOnRequest is the request body for creating a try-on task.
type TryOnRequest struct {
	ShopID          string `json:"shop_id"`
	ProductID       string `json:"product_id"`
	PersonImageURL  string `json:"person_image_url"`
	GarmentImageURL string `json:"garment_image_url"`
	GarmentType     string `json:"garment_type"`
	RestoreFace     bool   `json:"restore_face"`
	StylePreset     string `json:"style_preset,omitempty"`
	StylePrompt     string `json:"style_prompt,omitempty"`
	Enhance         bool   `json:"enhance,omitempty"`
}

// TryOnResponse is returned when a try-on task is created.
type TryOnResponse struct {
	TaskID         string `json:"task_id"`
	Status         string `json:"status"`
	StreamURL      string `json:"stream_url"`
	TryOnHistory   string `json:"try_on_history,omitempty"`
}

// TryOnResult is returned when polling a try-on task status.
type TryOnResult struct {
	TaskID          string  `json:"task_id"`
	Status          string  `json:"status"`
	ResultImageURL  string  `json:"result_image_url,omitempty"`
	PersonImageURL  string  `json:"person_image_url,omitempty"`
	GarmentImageURL string  `json:"garment_image_url,omitempty"`
	ProcessingMs    int     `json:"processing_ms,omitempty"`
	CostCNY         float64 `json:"cost_cny,omitempty"`
	ErrorMessage    string  `json:"error_message,omitempty"`
	Retries         int     `json:"retries"`
}

// Create handles POST /api/tryon — creates a new try-on task.
func (h *TryOnHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req TryOnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if req.PersonImageURL == "" {
		httputil.WriteError(w, http.StatusBadRequest, "person_image_url is required")
		return
	}
	if req.GarmentImageURL == "" {
		httputil.WriteError(w, http.StatusBadRequest, "garment_image_url is required")
		return
	}

	ctx := r.Context()
	shopID := req.ShopID

	// Vertical gate: try-on is a Fashion vertical feature
	if h.db != nil {
		shopUID, _ := uuid.Parse(shopID)
		if !vertical.HasFeature(h.db, shopUID, "tryon") {
			httputil.WriteError(w, http.StatusForbidden,
				"Virtual try-on is available for Fashion & Apparel stores. Select your industry in Settings.")
			return
		}
	}
		// Check shop-level feature flag
		if h.cache != nil {
			flagKey := fmt.Sprintf("shop:%s:feature:tryon", shopID)
			flagValue, err := h.cache.Get(ctx, flagKey)
			if err == nil && flagValue != "" {
				if isDisabled(flagValue) {
					httputil.WriteError(w, http.StatusForbidden,
						"Try-on feature is not enabled for this shop. Please upgrade your plan to access this feature.")
					return
				}
			}
		}

		// Check daily quota
		if h.cache != nil {
			today := time.Now().Format("20060102")
			allowed, _, err := h.cache.CheckQuota(ctx, shopID, today, 100)
			if err == nil && !allowed {
				w.Header().Set("Retry-After", "86400")
				httputil.WriteError(w, http.StatusTooManyRequests,
					"Daily quota exceeded. Please upgrade your plan or try again tomorrow.")
				return
			}
		}

	taskID := uuid.New().String()

	// Store task data in Redis
	taskData := &tryon.TaskData{
		TaskID:          taskID,
		ShopID:          shopID,
		ProductID:       req.ProductID,
		PersonImageURL:  req.PersonImageURL,
		GarmentImageURL: req.GarmentImageURL,
		GarmentType:     req.GarmentType,
		RestoreFace:     req.RestoreFace,
		Status:          tryon.StatusPending,
		Stage:           "pending",
		ProgressPct:     0,
		StylePreset:     req.StylePreset,
		StylePrompt:     req.StylePrompt,
		Enhance:         req.Enhance,
	}

	if taskData.GarmentType == "" {
		taskData.GarmentType = "upper"
	}

	raw, _ := json.Marshal(taskData)
		if h.cache == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "Cache unavailable")
			return
		}
	if err := h.cache.Set(ctx, fmt.Sprintf("tryon:%s:data", taskID), string(raw), h.cfg.CacheTTL()); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to store task data")
		return
	}
	if err := h.cache.Set(ctx, fmt.Sprintf("tryon:%s:status", taskID), string(tryon.StatusPending), h.cfg.CacheTTL()); err != nil {
		// Clean up partial state on failure
		h.cache.Delete(ctx, fmt.Sprintf("tryon:%s:data", taskID))
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to initialize task state")
		return
	}

	// Dispatch pipeline asynchronously with bounded concurrency via semaphore.
	// Uses a buffered channel as semaphore: capacity = NumCPU * 4, blocks when full.
	select {
	case h.pipelineSem <- struct{}{}:
		go func() {
			defer func() { <-h.pipelineSem }()
			bgCtx := bgTimeout(5*time.Minute)
			payload, _ := json.Marshal(req)
			if err := h.pipeline.Run(bgCtx, taskID, payload); err != nil {
				slog.Error("tryon pipeline failed", "task_id", taskID, "error", err)
			}
		}()
	default:
		// Too many concurrent tasks — clean up and return 503
		h.cache.Delete(r.Context(), fmt.Sprintf("tryon:%s:data", taskID))
		h.cache.Delete(r.Context(), fmt.Sprintf("tryon:%s:status", taskID))
		w.Header().Set("Retry-After", "30")
		httputil.WriteError(w, http.StatusServiceUnavailable, "Too many concurrent try-on tasks. Please try again later.")
		return
	}

	streamURL := fmt.Sprintf("/api/tryon/%s/stream", taskID)

	tryOnHistory := ""
	if h.injector != nil && h.db != nil && shopID != "" {
		tryOnHistory = h.injector.InjectOne(r.Context(), h.db, shopID, "try_on_history")
	}

	publishShopID, _ := uuid.Parse(shopID)
	PublishAIEvent(r.Context(), h.eventBus, publishShopID, "tryon.create", req, map[string]string{"task_id": taskID, "status": "pending"}, true)

	httputil.WriteJSON(w, http.StatusOK, TryOnResponse{
		TaskID:       taskID,
		Status:       string(tryon.StatusPending),
		StreamURL:    streamURL,
		TryOnHistory: tryOnHistory,
	})
}

// GetStatus handles GET /api/tryon/{taskID} — polls task status.
func (h *TryOnHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")
	ctx := r.Context()

	if h.cache == nil {
		httputil.WriteError(w, http.StatusNotFound, "Task not found")
		return
	}
	raw, err := h.cache.Get(ctx, fmt.Sprintf("tryon:%s:data", taskID))
	if err != nil || raw == "" {
		httputil.WriteError(w, http.StatusNotFound, "Task not found")
		return
	}

	var data tryon.TaskData
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to parse task data")
		return
	}

	httputil.WriteOK(w, TryOnResult{
		TaskID:          data.TaskID,
		Status:          string(data.Status),
		ResultImageURL:  data.ResultImageURL,
		PersonImageURL:  data.PersonImageURL,
		GarmentImageURL: data.GarmentImageURL,
		ProcessingMs:    data.ProcessingMs,
		CostCNY:         data.CostCNY,
		ErrorMessage:    data.ErrorMessage,
		Retries:         data.Retries,
	})
}

// StreamProgress handles GET /api/tryon/{taskID}/stream — SSE progress stream.
func (h *TryOnHandler) StreamProgress(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")
	ctx := r.Context()

	// Acquire semaphore
	select {
	case h.sseSem <- struct{}{}:
		defer func() { <-h.sseSem }()
	default:
		w.Header().Set("Retry-After", "10")
		httputil.WriteError(w, http.StatusServiceUnavailable,
			"Too many concurrent SSE connections. Please try again later.")
		return
	}

	flusher, ok := w.(sse.FlushWriter)
	if !ok {
		httputil.WriteError(w, http.StatusInternalServerError, "Streaming not supported")
		return
	}

	sse.SetSSEHeaders(w)

	pollInterval := 500 * time.Millisecond
	var lastStage string

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Check if client disconnected
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Get latest progress from Redis
		progressRaw, err := h.cache.Get(ctx, fmt.Sprintf("tryon:%s:progress", taskID))
		if err == nil && progressRaw != "" {
			sse.WriteAndFlush(flusher, progressRaw)
		}

		status, err := h.cache.Get(ctx, fmt.Sprintf("tryon:%s:status", taskID))
		if err == nil && status != "" {
			if status == "completed" || status == "cached" || status == "failed" {
				// Send final result
				raw, _ := h.cache.Get(ctx, fmt.Sprintf("tryon:%s:data", taskID))
				if raw != "" {
					var data tryon.TaskData
					if json.Unmarshal([]byte(raw), &data) == nil {
						final := map[string]interface{}{
							"status":           data.Status,
							"result_image_url": data.ResultImageURL,
							"error_message":    data.ErrorMessage,
							"processing_ms":    data.ProcessingMs,
						}
						finalJSON, _ := json.Marshal(final)
						sse.WriteAndFlush(flusher, string(finalJSON))
					}
				}
				return
			}

			if status != lastStage {
				pollInterval = 500 * time.Millisecond
				lastStage = status
			}
		}

		// Polling delay with exponential backoff
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}

		pollInterval = time.Duration(math.Min(float64(pollInterval*2), float64(5*time.Second)))
	}
}

func isDisabled(value string) bool {
	lower := ""
	for _, c := range value {
		if c >= 'A' && c <= 'Z' {
			lower += string(c + 32)
		} else {
			lower += string(c)
		}
	}
	return lower == "0" || lower == "false" || lower == "disabled"
}

// Preview handles GET /api/tryon/preview?shop_id= — admin quick preview.
// Returns the most recent try-on result for the given shop_id.
func (h *TryOnHandler) Preview(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "missing shop_id")
		return
	}

	if h.db == nil {
		slog.Warn("tryon preview: database not available, returning stub")
		httputil.WriteOK(w, map[string]string{
			"status":  "ok",
			"shop_id": shopID,
			"message": "Preview not yet implemented — will return cached try-on result for this shop",
		})
		return
	}

	shopUID, err := database.ResolveShopID(h.db, shopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid shop_id: "+err.Error())
		return
	}

	var record model.TryOnRecord
	if err := h.db.Where("shop_id = ?", shopUID).
		Order("created_at DESC").
		Limit(1).
		First(&record).Error; err != nil {
		// No records found — return a simple status
		httputil.WriteOK(w, map[string]interface{}{
			"status":  "ok",
			"shop_id": shopID,
			"message": "No try-on records found for this shop",
		})
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"status":           record.Status,
		"shop_id":          shopID,
		"task_id":          record.TaskID,
		"result_image_url": record.ResultImageURL,
		"person_image_url": record.PersonImageURL,
		"product_id":       record.ProductID,
		"created_at":       record.CreatedAt.Format(time.RFC3339),
	})
}
// Upload handles POST /api/tryon/upload — multipart image upload for customer photos.
func (h *TryOnHandler) Upload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "image too large (max 10MB)")
		return
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "image file required")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to read image")
		return
	}

	key := fmt.Sprintf("tryon/upload/%s_%d.webp", uuid.New().String(), time.Now().Unix())
	url, err := h.pipeline.Upload(bgTimeout(30*time.Second), key, data, "image/webp")
	if err != nil {
		slog.Error("tryon: upload failed", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "upload failed")
		return
	}

	httputil.WriteOK(w, map[string]string{"url": url})
}
