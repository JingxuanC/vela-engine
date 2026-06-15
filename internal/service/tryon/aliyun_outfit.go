// Package tryon provides AI virtual try-on services using Alibaba Cloud OutfitAnyone API.
package tryon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"log/slog"
)

const (
	dashscopeBaseURL = "https://dashscope.aliyuncs.com"

	// Models
	ModelTryonPlus = "aitryon-plus"       // High quality: 0.50¥, 40-60s
	ModelTryon     = "aitryon"            // Fast: 0.20¥, ~7s
	ModelParsing   = "aitryon-parsing-v1" // Garment segmentation: 0.004¥
	ModelRefiner   = "aitryon-refiner"    // Quality enhancement: 0.30¥
	ModelWan21     = "wan2.1-t2i-plus"    // Text-to-image & img2img enhancement: ~0.16¥

	// Limits
	MaxPollSeconds = 120
	MaxRetries     = 3
)

// AliyunOutfitClient wraps the DashScope OutfitAnyone API for virtual try-on.
type AliyunOutfitClient struct {
	apiKey string
	http   *http.Client
}

// ParseResult holds garment parsing output.
type ParseResult struct {
	CropImgURL    string  `json:"crop_img_url"`
	ParsingImgURL string  `json:"parsing_img_url"`
	BBox          []int   `json:"bbox"`
	CostCNY       float64 `json:"cost_cny"`
}

// TryonResult holds the virtual try-on output.
type TryonResult struct {
	ResultImageURL string  `json:"result_image_url"`
	TaskID         string  `json:"task_id"`
	CostCNY        float64 `json:"cost_cny"`
}

// dashscopeTaskResponse is the raw response from DashScope async task API.
type dashscopeTaskResponse struct {
	Output struct {
		TaskID     string `json:"task_id"`
		TaskStatus string `json:"task_status"`
		ImageURL   string `json:"image_url"`
		Message    string `json:"message"`
	} `json:"output"`
}

// dashscopeAsyncResponse is the initial response when submitting an async task.
type dashscopeAsyncResponse struct {
	Output struct {
		TaskID string `json:"task_id"`
	} `json:"output"`
}

// NewAliyunOutfitClient creates a new OutfitAnyone API client.
func NewAliyunOutfitClient(apiKey string) *AliyunOutfitClient {
	return &AliyunOutfitClient{
		apiKey: apiKey,
		http: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:    50,
				IdleConnTimeout: 60 * time.Second,
			},
		},
	}
}

// ParseGarment segments garment from a model shot.
// Cost: 0.004¥/image
func (c *AliyunOutfitClient) ParseGarment(ctx context.Context, imageURL string, clothesType string) (*ParseResult, error) {
	payload := map[string]interface{}{
		"model": ModelParsing,
		"input": map[string]string{
			"image_url": imageURL,
		},
		"parameters": map[string]interface{}{
			"clothes_type": []string{clothesType},
		},
	}

	resp, err := c.post(ctx, "/api/v1/services/vision/image-process/process", payload)
	if err != nil {
		return nil, fmt.Errorf("parse_garment: %w", err)
	}

	var data struct {
		Output struct {
			CropImgURL    string `json:"crop_img_url"`
			ParsingImgURL string `json:"parsing_img_url"`
			BBox          []int  `json:"bbox"`
		} `json:"output"`
	}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("parse_garment: unmarshal: %w", err)
	}

	return &ParseResult{
		CropImgURL:    data.Output.CropImgURL,
		ParsingImgURL: data.Output.ParsingImgURL,
		BBox:          data.Output.BBox,
		CostCNY:       0.004,
	}, nil
}

// TryonPlus generates a virtual try-on using aitryon-plus.
// Cost: 0.50¥/image
func (c *AliyunOutfitClient) TryonPlus(ctx context.Context, req *TryonPlusRequest) (*TryonResult, error) {
	input := map[string]interface{}{
		"person_image_url": req.PersonImageURL,
		"top_garment_url":  req.TopGarmentURL,
	}
	if req.BottomGarmentURL != "" {
		input["bottom_garment_url"] = req.BottomGarmentURL
	}

	payload := map[string]interface{}{
		"model": req.Model,
		"input": input,
		"parameters": map[string]interface{}{
			"resolution":   req.Resolution,
			"restore_face": req.RestoreFace,
		},
	}
	if req.Model == "" {
		payload["model"] = ModelTryonPlus
	}

	taskID, err := c.submitAsync(ctx, "/api/v1/services/aigc/image2image/image-synthesis", payload)
	if err != nil {
		return nil, fmt.Errorf("tryon_plus: submit: %w", err)
	}

	result, err := c.pollTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("tryon_plus: poll: %w", err)
	}

	return &TryonResult{
		ResultImageURL: result.Output.ImageURL,
		TaskID:         taskID,
		CostCNY:        0.50,
	}, nil
}

// Tryon generates a virtual try-on using the faster aitryon model.
// Cost: 0.20¥/image
func (c *AliyunOutfitClient) Tryon(ctx context.Context, req *TryonPlusRequest) (*TryonResult, error) {
	req.Model = ModelTryon
	result, err := c.TryonPlus(ctx, req)
	if result != nil {
		result.CostCNY = 0.20
	}
	return result, err
}

// Refiner enhances a try-on result image quality.
// Cost: 0.30¥/image
func (c *AliyunOutfitClient) Refiner(ctx context.Context, imageURL string) (*TryonResult, error) {
	payload := map[string]interface{}{
		"model": ModelRefiner,
		"input": map[string]string{
			"image_url": imageURL,
		},
	}

	taskID, err := c.submitAsync(ctx, "/api/v1/services/aigc/image2image/image-synthesis", payload)
	if err != nil {
		return nil, fmt.Errorf("refiner: submit: %w", err)
	}

	result, err := c.pollTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("refiner: poll: %w", err)
	}

	return &TryonResult{
		ResultImageURL: result.Output.ImageURL,
		TaskID:         taskID,
		CostCNY:        0.30,
	}, nil
}

// submitAsync submits an async task and returns the task ID.
func (c *AliyunOutfitClient) submitAsync(ctx context.Context, path string, payload interface{}) (string, error) {
	resp, err := c.postAsync(ctx, path, payload)
	if err != nil {
		return "", err
	}

	var data dashscopeAsyncResponse
	if err := json.Unmarshal(resp, &data); err != nil {
		return "", fmt.Errorf("submit: unmarshal: %w", err)
	}
	if data.Output.TaskID == "" {
		return "", fmt.Errorf("submit: no task_id in response")
	}

	return data.Output.TaskID, nil
}

// pollTask polls an async task until completion with exponential backoff.
func (c *AliyunOutfitClient) pollTask(ctx context.Context, taskID string) (*dashscopeTaskResponse, error) {
	interval := 2 * time.Second
	elapsed := 0 * time.Second

	for elapsed < MaxPollSeconds*time.Second {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		resp, err := c.get(ctx, "/api/v1/tasks/"+taskID)
		if err != nil {
			return nil, fmt.Errorf("poll: %w", err)
		}

		var data dashscopeTaskResponse
		if err := json.Unmarshal(resp, &data); err != nil {
			return nil, fmt.Errorf("poll: unmarshal: %w", err)
		}

		switch data.Output.TaskStatus {
		case "SUCCEEDED":
			return &data, nil
		case "FAILED":
			return nil, fmt.Errorf("task %s failed: %s", taskID, data.Output.Message)
		case "PENDING", "RUNNING":
			// Continue polling
		default:
			slog.Warn("unknown task status", "task_id", taskID, "status", data.Output.TaskStatus)
		}

		sleepDuration := interval
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sleepDuration):
		}

		elapsed += interval
		interval = time.Duration(math.Min(float64(interval*2), float64(10*time.Second)))
	}

	return nil, fmt.Errorf("task %s timed out after %ds", taskID, MaxPollSeconds)
}

// post sends a synchronous POST request.
func (c *AliyunOutfitClient) post(ctx context.Context, path string, payload interface{}) ([]byte, error) {
	return c.doRequest(ctx, "POST", path, payload, false)
}

// postAsync sends an async POST request (X-DashScope-Async header).
func (c *AliyunOutfitClient) postAsync(ctx context.Context, path string, payload interface{}) ([]byte, error) {
	return c.doRequest(ctx, "POST", path, payload, true)
}

// get sends a GET request.
func (c *AliyunOutfitClient) get(ctx context.Context, path string) ([]byte, error) {
	return c.doRequest(ctx, "GET", path, nil, false)
}

func (c *AliyunOutfitClient) doRequest(ctx context.Context, method, path string, payload interface{}, async bool) ([]byte, error) {
	var bodyBytes []byte
	if payload != nil {
		var err error
		bodyBytes, err = json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
	}

	url := dashscopeBaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if async {
		req.Header.Set("X-DashScope-Async", "enable")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody[:minInt(len(respBody), 500)]))
	}

	return respBody, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TryonPlusRequest holds parameters for a try-on request.
type TryonPlusRequest struct {
	PersonImageURL   string `json:"person_image_url"`
	TopGarmentURL    string `json:"top_garment_url"`
	BottomGarmentURL string `json:"bottom_garment_url,omitempty"`
	Model            string `json:"model,omitempty"`
	RestoreFace      bool   `json:"restore_face"`
	Resolution       int    `json:"resolution"`
}

// StyleEnhanceResult holds the output of the style enhancement step.
type StyleEnhanceResult struct {
	EnhancedURL string  `json:"enhanced_url"`
	CostCNY     float64 `json:"cost_cny"`
}

// StyleEnhance applies aesthetic style enhancement to a try-on result image.
// Uses Wan2.1 img2img to transform the basic try-on output into a
// professional fashion photograph with controlled lighting, composition, and mood.
// Cost: ~0.16¥/image
func (c *AliyunOutfitClient) StyleEnhance(ctx context.Context, imageURL string, stylePreset StylePreset) (*StyleEnhanceResult, error) {
	payload := map[string]any{
		"model": ModelWan21,
		"input": map[string]string{
			"image_url": imageURL,
			"prompt":    stylePreset.PositivePrompt,
		},
		"parameters": map[string]any{
			"negative_prompt": stylePreset.NegativePrompt,
			"strength":        stylePreset.Strength,
			"guidance_scale":  stylePreset.GuidanceScale,
		},
	}

	taskID, err := c.submitAsync(ctx, "/api/v1/services/aigc/image2image/image-synthesis", payload)
	if err != nil {
		return nil, fmt.Errorf("style_enhance: submit: %w", err)
	}

	result, err := c.pollTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("style_enhance: poll: %w", err)
	}

	return &StyleEnhanceResult{
		EnhancedURL: result.Output.ImageURL,
		CostCNY:     0.16,
	}, nil
}

// Close releases the HTTP client's idle connections.
func (c *AliyunOutfitClient) Close() {
	c.http.CloseIdleConnections()
}
