package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/internal/service/review"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// ReviewHandler handles review analysis endpoints.
type ReviewHandler struct {
	llmRouter *service.LLMRouter
}

// NewReviewHandler creates a new ReviewHandler.
func NewReviewHandler(llmRouter *service.LLMRouter) *ReviewHandler {
	return &ReviewHandler{llmRouter: llmRouter}
}

// SummarizeRequest holds the request for review summarization.
type SummarizeRequest struct {
	ProductID string               `json:"product_id"`
	Reviews   []review.ReviewInput `json:"reviews"`
}

// SummarizeResponse holds the response.
type SummarizeResponse struct {
	Success           bool     `json:"success"`
	Summary           string   `json:"summary"`
	SentimentPositive float64  `json:"sentiment_positive"`
	SentimentNegative float64  `json:"sentiment_negative"`
	SentimentNeutral  float64  `json:"sentiment_neutral"`
	TopPros           []string `json:"top_pros"`
	TopCons           []string `json:"top_cons"`
	KeyThemes         []string `json:"key_themes"`
	ReviewCount       int      `json:"review_count"`
	Error             string   `json:"error,omitempty"`
}

// Summarize handles POST /api/review/summarize.
func (h *ReviewHandler) Summarize(w http.ResponseWriter, r *http.Request) {
	var req SummarizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if strings.TrimSpace(req.ProductID) == "" {
		httputil.WriteError(w, http.StatusBadRequest, "product_id is required")
		return
	}
	if len(req.Reviews) == 0 {
		httputil.WriteError(w, http.StatusBadRequest, "reviews array must not be empty")
		return
	}

	revProvider, _, revErr := h.llmRouter.GetProvider(r.Context(), uuid.Nil)
	if revErr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "AI service unavailable")
		return
	}
	result, err := review.SummarizeReviews(r.Context(), revProvider, req.ProductID, req.Reviews)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.WriteOK(w, SummarizeResponse{
		Success:           true,
		Summary:           result.Summary,
		SentimentPositive: result.SentimentPositive,
		SentimentNegative: result.SentimentNegative,
		SentimentNeutral:  result.SentimentNeutral,
		TopPros:           result.TopPros,
		TopCons:           result.TopCons,
		KeyThemes:         result.KeyThemes,
		ReviewCount:       len(req.Reviews),
	})
}

// DetectFakeRequest holds the fake review detection request.
type DetectFakeRequest struct {
	ProductID string               `json:"product_id"`
	Reviews   []review.ReviewInput `json:"reviews"`
	Threshold float64              `json:"threshold"`
}

// DetectFakeResponse holds the detection results.
type DetectFakeResponse struct {
	Success       bool     `json:"success"`
	FakeIDs       []string `json:"fake_ids"`
	SuspiciousIDs []string `json:"suspicious_ids"`
	Analysis      string   `json:"analysis"`
	Error         string   `json:"error,omitempty"`
}

// DetectFake handles POST /api/review/detect-fake.
func (h *ReviewHandler) DetectFake(w http.ResponseWriter, r *http.Request) {
	var req DetectFakeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if strings.TrimSpace(req.ProductID) == "" {
		httputil.WriteError(w, http.StatusBadRequest, "product_id is required")
		return
	}
	if len(req.Reviews) == 0 {
		httputil.WriteError(w, http.StatusBadRequest, "reviews array must not be empty")
		return
	}
	if req.Threshold == 0 {
		req.Threshold = 0.7
	}

	detectProvider, _, detectErr := h.llmRouter.GetProvider(r.Context(), uuid.Nil)
	if detectErr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "AI service unavailable")
		return
	}
	result, err := review.DetectFakeReviews(r.Context(), detectProvider, req.ProductID, req.Reviews, req.Threshold)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.WriteOK(w, DetectFakeResponse{
		Success:       true,
		FakeIDs:       result.FakeIDs,
		SuspiciousIDs: result.SuspiciousIDs,
		Analysis:      result.Analysis,
	})
}
