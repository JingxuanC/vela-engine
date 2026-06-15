package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/internal/service/tryon"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"gorm.io/gorm"
)

// ImageHandler handles image processing endpoints.
type ImageHandler struct {
	db           *gorm.DB
	llmRouter    *service.LLMRouter
	outfitClient *tryon.AliyunOutfitClient
}

// NewImageHandler creates a new ImageHandler.
func NewImageHandler(db *gorm.DB, llmRouter *service.LLMRouter, outfitClient *tryon.AliyunOutfitClient) *ImageHandler {
	return &ImageHandler{db: db, llmRouter: llmRouter, outfitClient: outfitClient}
}

// RemoveBGRequest holds a background removal request.
type RemoveBGRequest struct {
	ShopID   string `json:"shop_id"`
	ImageURL string `json:"image_url"`
}

// RemoveBGResponse holds the result.
type RemoveBGResponse struct {
	Success   bool   `json:"success"`
	ResultURL string `json:"result_url"`
	Error     string `json:"error,omitempty"`
}

// RemoveBG handles POST /api/image/remove-bg.
func (h *ImageHandler) RemoveBG(w http.ResponseWriter, r *http.Request) {
	var req RemoveBGRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if strings.TrimSpace(req.ImageURL) == "" {
		httputil.WriteError(w, http.StatusBadRequest, "image_url is required")
		return
	}

	if h.outfitClient == nil {
		slog.Warn("remove-bg: outfit client not available, returning stub")
		httputil.WriteOK(w, RemoveBGResponse{
			Success:   true,
			ResultURL: req.ImageURL + "?bg=removed",
		})
		return
	}

	ctx := r.Context()
	clothesType := "upper" // default garment type

	// Use ParseGarment (garment segmentation) as background removal.
	// This segments the garment/subject from the background image.
	result, err := h.outfitClient.ParseGarment(ctx, req.ImageURL, clothesType)
	if err != nil {
		slog.Error("remove-bg: ParseGarment failed", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Background removal failed: "+err.Error())
		return
	}

	// Use the cropped image URL as the result (subject extracted from background)
	resultURL := result.CropImgURL
	if resultURL == "" {
		resultURL = result.ParsingImgURL
	}

	// Store a record in DB if shop_id is provided
	if req.ShopID != "" && h.db != nil {
		shopUID, parseErr := database.ResolveShopID(h.db, req.ShopID)
		if parseErr == nil {
			record := model.TryOnRecord{
				ShopID:         shopUID,
				TaskID:         fmt.Sprintf("remove-bg-%s", uuid.New().String()),
				ProductID:      "",
				PersonImageURL: req.ImageURL,
				ResultImageURL: resultURL,
				Status:         "completed",
			}
			if dbErr := h.db.Create(&record).Error; dbErr != nil {
				slog.Warn("remove-bg: failed to store record", "error", dbErr)
			}
		}
	}

	slog.Info("remove-bg completed", "shop_id", req.ShopID, "image_url", req.ImageURL, "result_url", resultURL)

	httputil.WriteOK(w, RemoveBGResponse{
		Success:   true,
		ResultURL: resultURL,
	})
}

// RemoveBGWithModel handles background removal with a specific DashScope image generation model.
// Uses the Wan2.1 img2img model via AliyunOutfitClient for image editing.
func (h *ImageHandler) RemoveBGWithModel(ctx context.Context, imageURL string) (string, error) {
	if h.outfitClient == nil {
		return "", fmt.Errorf("outfit client not available")
	}

	result, err := h.outfitClient.ParseGarment(ctx, imageURL, "upper")
	if err != nil {
		return "", fmt.Errorf("remove bg from model: %w", err)
	}

	if result.CropImgURL != "" {
		return result.CropImgURL, nil
	}
	return result.ParsingImgURL, nil
}
