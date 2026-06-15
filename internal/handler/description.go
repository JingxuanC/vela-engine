package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// DescriptionHandler handles product description generation endpoints.
type DescriptionHandler struct {
	llmRouter *service.LLMRouter
}

// NewDescriptionHandler creates a new DescriptionHandler.
func NewDescriptionHandler(llmRouter *service.LLMRouter) *DescriptionHandler {
	return &DescriptionHandler{llmRouter: llmRouter}
}

// GenerateRequest holds the description generation request.
type GenerateRequest struct {
	ProductName    string   `json:"product_name"`
	Category       string   `json:"category"`
	Features       []string `json:"features"`
	ToneOfVoice    string   `json:"tone_of_voice"`
	TargetKeywords []string `json:"target_keywords"`
	Language       string   `json:"language"`
}

// GenerateResponse holds the generated description.
type GenerateResponse struct {
	Success      bool     `json:"success"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	BulletPoints []string `json:"bullet_points"`
	SEOKeywords  []string `json:"seo_keywords"`
	Error        string   `json:"error,omitempty"`
}

// Generate handles POST /api/description/generate.
func (h *DescriptionHandler) Generate(w http.ResponseWriter, r *http.Request) {
	var req GenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if req.ProductName == "" {
		httputil.WriteError(w, http.StatusBadRequest, "product_name is required")
		return
	}

	descProvider, _, descErr := h.llmRouter.GetProvider(r.Context(), uuid.Nil)
	if descErr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "AI service unavailable")
		return
	}
	result, err := service.GenerateDescription(r.Context(), descProvider, req.ProductName, req.Category, req.Features, req.ToneOfVoice, req.TargetKeywords, req.Language)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.WriteOK(w, result)
}
