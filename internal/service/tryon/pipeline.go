package tryon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"time"

	"log/slog"

	"github.com/JingxuanC/vela-engine/internal/service"
)

// TaskStatus represents the state of an async try-on task.
type TaskStatus string

const (
	StatusPending    TaskStatus = "pending"
	StatusValidating TaskStatus = "validating"
	StatusParsing    TaskStatus = "parsing"
	StatusGenerating TaskStatus = "generating"
	StatusRefining   TaskStatus = "refining"
	StatusUploading  TaskStatus = "uploading"
	StatusCached     TaskStatus = "cached"
	StatusCompleted  TaskStatus = "completed"
	StatusFailed     TaskStatus = "failed"
)

// TaskData holds all the state for a try-on task, stored in Redis.
type TaskData struct {
	TaskID          string     `json:"task_id"`
	ShopID          string     `json:"shop_id"`
	ProductID       string     `json:"product_id"`
	PersonImageURL  string     `json:"person_image_url"`
	GarmentImageURL string     `json:"garment_image_url"`
	GarmentType     string     `json:"garment_type"`
	RestoreFace     bool       `json:"restore_face"`
	Status          TaskStatus `json:"status"`
	Stage           string     `json:"stage"`
	ProgressPct     int        `json:"progress_pct"`
	ResultImageURL  string     `json:"result_image_url,omitempty"`
	ErrorMessage    string     `json:"error_message,omitempty"`
	Retries         int        `json:"retries"`
	ProcessingMs    int        `json:"processing_ms,omitempty"`
	CostCNY         float64    `json:"cost_cny,omitempty"`
	// Style enhancement
	StylePreset string `json:"style_preset,omitempty"`
	StylePrompt string `json:"style_prompt,omitempty"` // raw merchant-written prompt
	Enhance     bool   `json:"enhance,omitempty"`
	EnhancedURL string `json:"enhanced_url,omitempty"`
}

// PipelineOrchestrator runs the 10-step try-on pipeline with image validation,
// preprocessing, phash caching, quality checking, and Aliyun API orchestration.
// Each step publishes progress updates via Redis pub/sub for SSE streaming.
type PipelineOrchestrator struct {
	cache   *service.CacheService
	client  *AliyunOutfitClient
	nc      *NegativeCache
	imgProc *ImageProcessor
	storage *service.StorageService
}

// NewPipelineOrchestrator creates a new pipeline orchestrator with all dependencies.
func NewPipelineOrchestrator(
	cache *service.CacheService,
	client *AliyunOutfitClient,
	imgProc *ImageProcessor,
	storage *service.StorageService,
) *PipelineOrchestrator {
	return &PipelineOrchestrator{
		cache:   cache,
		client:  client,
		nc:      NewNegativeCache(5 * time.Minute),
		imgProc: imgProc,
		storage: storage,
	}
}

// Run executes the full try-on pipeline for a given task.
// This is the entry point called by the asynq task handler.
func (p *PipelineOrchestrator) Run(ctx context.Context, taskID string, rawPayload json.RawMessage) error {
	start := time.Now()

	// Load task data
	data, err := p.loadTaskData(ctx, taskID)
	if err != nil {
		return fmt.Errorf("pipeline: load task data: %w", err)
	}

	// Step 1: Validate — download + validate format + dimension checks (10%)
	p.updateProgress(ctx, taskID, StatusValidating, "validating", 10)
	_, personImg, _, garmentImg, err := p.validateImages(ctx, data)
	if err != nil {
		return p.handleFailure(ctx, taskID, data, err)
	}

	// Step 2: Phash cache check — if cached, skip all API calls (15%)
	personPhash := p.imgProc.ComputePHash(personImg)
	garmentPhash := p.imgProc.ComputePHash(garmentImg)
	cacheKey := personPhash + ":" + garmentPhash
	if cachedURL, ok := p.getPhashCache(ctx, cacheKey); ok {
		elapsed := time.Since(start)
		data.Status = StatusCached
		data.Stage = "cached"
		data.ProgressPct = 100
		data.ResultImageURL = cachedURL
		data.ProcessingMs = int(elapsed.Milliseconds())
		data.CostCNY = 0
		if err := p.saveTaskData(ctx, taskID, data); err != nil {
			slog.Error("failed to save cached task data", "task_id", taskID, "error", err)
		}
		p.publishProgress(ctx, taskID, map[string]interface{}{
			"status":           StatusCached,
			"result_image_url": cachedURL,
			"processing_ms":    data.ProcessingMs,
			"cached":           true,
		})
		slog.Info("tryon pipeline: cache hit, skipping API", "task_id", taskID, "phash", cacheKey)
		return nil
	}

	// Step 3: Preprocess — resize/crop + upload to R2 if available (25%)
	p.updateProgress(ctx, taskID, StatusParsing, "preprocessing", 25)
	personURL, garmentURL, err := p.preprocessImages(ctx, taskID, personImg, garmentImg)
	if err != nil {
		// R2 not configured — fall back to original URLs
		slog.Warn("preprocess upload failed, using original URLs", "task_id", taskID, "error", err)
		personURL = data.PersonImageURL
		garmentURL = data.GarmentImageURL
	}

	// Step 4: Parse garment (35%)
	p.updateProgress(ctx, taskID, StatusParsing, "parsing", 35)
	if _, err = p.parseGarment(ctx, garmentURL, data.GarmentType); err != nil {
		return p.handleFailure(ctx, taskID, data, err)
	}

	// Step 5: Generate try-on (55%)
	p.updateProgress(ctx, taskID, StatusGenerating, "generating", 55)
	tryonResult, err := p.generate(ctx, data, personURL, garmentURL)
	if err != nil {
		return p.handleFailure(ctx, taskID, data, err)
	}

	// Step 6: Quality check — verify output quality (70%)
	if tryonResult != nil {
		p.updateProgress(ctx, taskID, StatusGenerating, "quality_check", 70)
		if !p.checkQuality(ctx, tryonResult.ResultImageURL) {
			slog.Warn("quality check failed, continuing with degraded result", "task_id", taskID)
		}
	}

	// Step 7: Refine (85%)
	var finalURL string
	if tryonResult != nil {
		p.updateProgress(ctx, taskID, StatusRefining, "refining", 85)
		refinedResult, err := p.refine(ctx, tryonResult.ResultImageURL)
		if err != nil {
			slog.Warn("refiner failed, using unrefined result", "task_id", taskID, "error", err)
			finalURL = tryonResult.ResultImageURL
		} else {
			finalURL = refinedResult.ResultImageURL
		}

		p.updateProgress(ctx, taskID, StatusUploading, "uploading", 95)
	}

	// Step 8: Set phash cache — cache result URL for future hits
	if tryonResult != nil {
		p.setPhashCache(ctx, cacheKey, finalURL)
	}

	// Step 8.5: Style enhancement — if enabled, apply Wan2.1 img2img
	var styleEnhancedURL string
	if data.Enhance && finalURL != "" {
		p.updateProgress(ctx, taskID, StatusUploading, "style_enhance", 90)
		var preset StylePreset
		if data.StylePreset != "" {
			preset = GetStylePreset(data.StylePreset)
		} else {
			preset = GetStylePreset("studio")
		}
		enhanced, err := p.client.StyleEnhance(ctx, finalURL, preset)
		if err != nil {
			slog.Warn("style enhancement failed, using unenhanced result", "task_id", taskID, "error", err)
			styleEnhancedURL = finalURL
		} else {
			styleEnhancedURL = enhanced.EnhancedURL
			data.CostCNY += enhanced.CostCNY
			data.EnhancedURL = enhanced.EnhancedURL
		}
	} else {
		styleEnhancedURL = finalURL
	}

	// Step 9: Upload to R2 (if style-enhanced)
	uploadURL := styleEnhancedURL
	if uploadURL != finalURL && uploadURL != "" {
		// Download enhanced result and upload to R2
		enhancedRaw, _, err := downloadImageBytes(ctx, uploadURL)
		if err == nil {
			shopID := data.ShopID
			if shopID == "" {
				shopID = "unknown"
			}
			timestamp := time.Now().UTC().Format("20060102_150405")
			r2Path := fmt.Sprintf("results/%s/%s/%s_enhanced.webp", shopID, taskID, timestamp)
			r2URL, err := p.storage.Upload(ctx, r2Path, enhancedRaw, "image/webp")
			if err == nil {
				uploadURL = r2URL
			}
		}
	}

	// Step 10: Complete (100%)
	elapsed := time.Since(start)
	data.Status = StatusCompleted
	data.Stage = "completed"
	data.ProgressPct = 100
	data.ResultImageURL = finalURL
	data.ProcessingMs = int(elapsed.Milliseconds())
	if tryonResult != nil {
		data.CostCNY = tryonResult.CostCNY
	}

	if err := p.saveTaskData(ctx, taskID, data); err != nil {
		slog.Error("failed to save completed task data", "task_id", taskID, "error", err)
	}

	// Publish final status
	p.publishProgress(ctx, taskID, map[string]interface{}{
		"status":           StatusCompleted,
		"result_image_url": finalURL,
		"processing_ms":    data.ProcessingMs,
	})
	slog.Info("tryon pipeline completed", "task_id", taskID, "duration_ms", data.ProcessingMs)

	return nil
}

// --- Internal: pipeline steps ---

// validateImages downloads and validates both person and garment images.
func (p *PipelineOrchestrator) validateImages(ctx context.Context, data *TaskData) (
	personRaw []byte, personImg image.Image,
	garmentRaw []byte, garmentImg image.Image,
	err error,
) {
	personRaw, _, personImg, err = p.imgProc.ValidateImage(ctx, data.PersonImageURL)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("validate person image: %w", err)
	}

	garmentRaw, _, garmentImg, err = p.imgProc.ValidateImage(ctx, data.GarmentImageURL)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("validate garment image: %w", err)
	}

	slog.Info("images validated",
		"task_id", data.TaskID,
		"person_size", len(personRaw),
		"garment_size", len(garmentRaw),
	)
	return personRaw, personImg, garmentRaw, garmentImg, nil
}

// preprocessImages preprocesses person and garment images and uploads them to R2.
// Returns the public URLs of the preprocessed images.
func (p *PipelineOrchestrator) preprocessImages(ctx context.Context, taskID string, personImg, garmentImg image.Image) (string, string, error) {
	// Preprocess person image
	processedPerson := p.imgProc.PreprocessPerson(personImg)
	personBytes, err := p.imgProc.CompressForWeb(processedPerson)
	if err != nil {
		return "", "", fmt.Errorf("compress person: %w", err)
	}
	personURL, err := p.storage.Upload(ctx, "tryon/"+taskID+"/person.png", personBytes, detectContentType(personBytes))
	if err != nil {
		return "", "", fmt.Errorf("upload person: %w", err)
	}

	// Preprocess garment image
	processedGarment := p.imgProc.PreprocessGarment(garmentImg)
	garmentBytes, err := p.imgProc.CompressForWeb(processedGarment)
	if err != nil {
		return "", "", fmt.Errorf("compress garment: %w", err)
	}
	garmentURL, err := p.storage.Upload(ctx, "tryon/"+taskID+"/garment.png", garmentBytes, detectContentType(garmentBytes))
	if err != nil {
		return "", "", fmt.Errorf("upload garment: %w", err)
	}

	slog.Info("images preprocessed and uploaded", "task_id", taskID,
		"person_url", personURL, "garment_url", garmentURL)
	return personURL, garmentURL, nil
}

// parseGarment calls the Aliyun API to segment/parse the garment image.
func (p *PipelineOrchestrator) parseGarment(ctx context.Context, garmentURL, garmentType string) (*ParseResult, error) {
	return p.client.ParseGarment(ctx, garmentURL, garmentType)
}

// generate calls the Aliyun try-on API with preprocessed image URLs.
func (p *PipelineOrchestrator) generate(ctx context.Context, data *TaskData, personURL, garmentURL string) (*TryonResult, error) {
	params := BuildTryonPrompt(data.GarmentType, "casual", "unisex", 0)
	return p.client.TryonPlus(ctx, &TryonPlusRequest{
		PersonImageURL: personURL,
		TopGarmentURL:  garmentURL,
		Model:          params.Model,
		RestoreFace:    data.RestoreFace,
		Resolution:     params.Resolution,
	})
}

// checkQuality downloads the generated result and runs quality checks.
// Returns true if quality passes, false if degraded (but pipeline continues).
func (p *PipelineOrchestrator) checkQuality(ctx context.Context, resultURL string) bool {
	img, err := downloadResultImage(ctx, resultURL)
	if err != nil {
		slog.Warn("quality check: failed to download result image", "url", resultURL, "error", err)
		return false
	}

	result := CheckQuality(img)
	slog.Info("quality check complete",
		"passes", result.Passes,
		"blur_score", fmt.Sprintf("%.1f", result.BlurScore),
		"artifact_pct", fmt.Sprintf("%.3f", result.ArtifactPct),
	)
	return result.Passes
}

func (p *PipelineOrchestrator) refine(ctx context.Context, imageURL string) (*TryonResult, error) {
	return p.client.Refiner(ctx, imageURL)
}

// --- Internal: data helpers ---

func (p *PipelineOrchestrator) loadTaskData(ctx context.Context, taskID string) (*TaskData, error) {
	raw, err := p.cache.Get(ctx, fmt.Sprintf("tryon:%s:data", taskID))
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, fmt.Errorf("task %s not found", taskID)
	}

	var data TaskData
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, fmt.Errorf("unmarshal task data: %w", err)
	}
	return &data, nil
}

func (p *PipelineOrchestrator) saveTaskData(ctx context.Context, taskID string, data *TaskData) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return p.cache.Set(ctx, fmt.Sprintf("tryon:%s:data", taskID), string(raw), 24*time.Hour)
}

func (p *PipelineOrchestrator) updateProgress(ctx context.Context, taskID string, status TaskStatus, stage string, pct int) {
	data, _ := p.loadTaskData(ctx, taskID)
	if data != nil {
		data.Status = status
		data.Stage = stage
		data.ProgressPct = pct
		p.saveTaskData(ctx, taskID, data)
	}

	p.publishProgress(ctx, taskID, map[string]interface{}{
		"status":       status,
		"stage":        stage,
		"progress_pct": pct,
	})
}

func (p *PipelineOrchestrator) publishProgress(ctx context.Context, taskID string, payload map[string]interface{}) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}

	// Store progress
	p.cache.Set(ctx, fmt.Sprintf("tryon:%s:progress", taskID), string(raw), 5*time.Minute)

	// Store status
	if status, ok := payload["status"]; ok {
		p.cache.Set(ctx, fmt.Sprintf("tryon:%s:status", taskID), fmt.Sprint(status), 5*time.Minute)
	}

	// Publish to Redis pub/sub channel
	p.cache.Publish(ctx, fmt.Sprintf("tryon:%s:progress", taskID), string(raw))
}

func (p *PipelineOrchestrator) handleFailure(ctx context.Context, taskID string, data *TaskData, err error) error {
	slog.Error("tryon pipeline failed", "task_id", taskID, "stage", data.Stage, "error", err)

	data.Status = StatusFailed
	data.ErrorMessage = err.Error()
	if saveErr := p.saveTaskData(ctx, taskID, data); saveErr != nil {
		slog.Error("failed to save failed task data", "task_id", taskID, "error", saveErr)
	}

	p.publishProgress(ctx, taskID, map[string]interface{}{
		"status":        StatusFailed,
		"error_message": err.Error(),
	})

	return err
}

// --- Internal: utility functions ---

// downloadResultImage downloads and decodes an image from a URL (for quality checking result images).
func downloadResultImage(ctx context.Context, url string) (image.Image, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("download: create request: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("download: read: %w", err)
	}

	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("download: decode: %w", err)
	}

	return img, nil
}

// detectContentType returns the MIME type based on magic bytes.
func detectContentType(data []byte) string {
	if len(data) >= 2 && data[0] == 0xff && data[1] == 0xd8 {
		return "image/jpeg"
	}
	// Default to PNG for all other cases (CompressForWeb prefers PNG)
	return "image/png"
}

// downloadImageBytes downloads raw image bytes from a URL.
// Returns (raw_bytes, content_type, error).
func downloadImageBytes(ctx context.Context, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("download: create request: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("download: read: %w", err)
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// Upload stores data to R2 and returns the public URL.
func (p *PipelineOrchestrator) Upload(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	return p.storage.Upload(ctx, key, data, contentType)
}
