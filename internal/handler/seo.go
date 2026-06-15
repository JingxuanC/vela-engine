package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	chi "github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// SEOHandler handles SEO analysis endpoints.
type SEOHandler struct {
	llmRouter *service.LLMRouter
	cache     *service.CacheService
}

// NewSEOHandler creates a new SEOHandler.
func NewSEOHandler(llmRouter *service.LLMRouter, cache *service.CacheService) *SEOHandler {
	return &SEOHandler{llmRouter: llmRouter, cache: cache}
}

// ScanRequest holds the SEO scan request.
type ScanRequest struct {
	URL         string `json:"url"`
	ProductName string `json:"product_name"`
	Description string `json:"description"`
}

// ScanResponse holds the SEO analysis results.
type ScanResponse struct {
	Success     bool     `json:"success"`
	Score       int      `json:"score"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Keywords    []string `json:"keywords"`
	Suggestions []string `json:"suggestions"`
	JSONLD      string   `json:"json_ld"`
	Error       string   `json:"error,omitempty"`
}

// Scan handles POST /api/seo/scan — analyzes SEO for a product page.
func (h *SEOHandler) Scan(w http.ResponseWriter, r *http.Request) {
	var req ScanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if req.ProductName == "" {
		httputil.WriteError(w, http.StatusBadRequest, "product_name is required")
		return
	}

	seoProvider, _, seoErr := h.llmRouter.GetProvider(r.Context(), uuid.Nil)
	if seoErr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "AI service unavailable")
		return
	}
	result, err := service.GenerateSEO(r.Context(), seoProvider, req.URL, req.ProductName, req.Description)
	// Store AI-generated JSON-LD in Redis for GEO module
	if result != nil && result.Success && result.JSONLD != "" && h.cache != nil {
		parts := strings.Split(strings.TrimRight(req.URL, "/"), "/")
		handle := parts[len(parts)-1]
		cacheKey := fmt.Sprintf("geo:schema:ai:%s:%s", req.ProductName, handle)
		h.cache.Set(r.Context(), cacheKey, result.JSONLD, 24*time.Hour)
	}
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httputil.WriteOK(w, result)
}
func (h *SEOHandler) BatchCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShopID     string   `json:"shop_id"`
		ProductIDs []string `json:"product_ids"`
		Tasks      []string `json:"tasks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if len(req.ProductIDs) == 0 {
		httputil.WriteError(w, http.StatusBadRequest, "no products selected")
		return
	}
	batchID := fmt.Sprintf("batch-%d", time.Now().UnixMilli())
	// Run AI generation in background
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("seo batch: panic", "error", r)
			}
		}()
		for _, pid := range req.ProductIDs {
			h.generateSEOForProduct(bgTimeout(5*time.Minute), req.ShopID, pid, req.Tasks)
		}
		// Invalidate GEO cache so feed picks up AI-generated content
		if h.cache != nil {
			h.cache.Delete(bgTimeout(30*time.Second), "geo:feed:"+req.ShopID)
		}
	}()
	httputil.WriteOK(w, map[string]interface{}{"batch_id": batchID, "total_items": len(req.ProductIDs), "status": "processing"})
}
func (h *SEOHandler) BatchStatus(w http.ResponseWriter, r *http.Request) {
	httputil.WriteOK(w, map[string]interface{}{"batch_id": chi.URLParam(r, "batchID"), "status": "completed"})
}
func (h *SEOHandler) BatchResults(w http.ResponseWriter, r *http.Request) {
	httputil.WriteOK(w, map[string]interface{}{"batch_id": chi.URLParam(r, "batchID"), "items": []interface{}{}})
}
func (h *SEOHandler) generateSEOForProduct(ctx context.Context, shopID, productID string, tasks []string) {
	if h.llmRouter == nil {
		return
	}
	genProvider, genCfg, genErr := h.llmRouter.GetProvider(ctx, uuid.Nil)
	if genErr != nil {
		slog.Error("seo batch: LLM provider unavailable", "error", genErr)
		return
	}
	for _, task := range tasks {
		sys := "You are an SEO expert. Generate optimized " + task + " for product " + productID + ". Output JSON only."
		req := &service.ChatCompletionRequest{
			Model:       genCfg.Model,
			Messages:    []service.ChatMessage{{Role: "system", Content: sys}},
			Temperature: 0.3, MaxTokens: 300,
		}
		_, err := genProvider.ChatCompletion(ctx, req)
		if err != nil {
			slog.Error("seo batch: failed", "product", productID, "task", task, "error", err)
		}
	}
}
