package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/internal/service/visibility"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// VisibilityHandler serves the AI Visibility Suite API.
type VisibilityHandler struct {
	db        *gorm.DB
	cache     *service.CacheService
	ucp       *visibility.UCPEngine
	geo       *visibility.GEOEngine
	seo       *visibility.SEOEngine
	llms      *visibility.LLMsTxtGenerator
	optimizer *visibility.Optimizer
}

// NewVisibilityHandler creates a new VisibilityHandler.
func NewVisibilityHandler(db *gorm.DB, cache *service.CacheService, llmRouter *service.LLMRouter) *VisibilityHandler {
	return &VisibilityHandler{
		db:        db,
		cache:     cache,
		ucp:       visibility.NewUCPEngine(db),
		geo:       visibility.NewGEOEngine(db),
		seo:       visibility.NewSEOEngine(db),
		llms:      visibility.NewLLMsTxtGenerator(db),
		optimizer: visibility.NewOptimizer(llmRouter),
	}
}

// ── GET /api/visibility/score ────────────────────────────────────────────────

// Score returns the AI discovery score for a shop.
// Query params: shop_id (required), product_ids (optional, comma-separated)
func (h *VisibilityHandler) Score(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	// Check Redis cache
	cacheKey := fmt.Sprintf("visibility:score:%s", shopID)
	if h.cache != nil {
		if cached, err := h.cache.Get(r.Context(), cacheKey); err == nil && cached != "" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cache", "HIT")
			w.Write([]byte(cached))
			return
		}
	}

	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	ctx := r.Context()

	// Resolve ShopID
	uid, err := uuid.Parse(shopID)
	if err != nil {
		// Not a UUID — try to resolve by domain
		var shop model.Shop
		if h.db.WithContext(ctx).Where("shop_domain = ?", shopID).First(&shop).Error != nil {
			httputil.WriteError(w, http.StatusBadRequest, "shop not found: "+shopID)
			return
		}
		uid = shop.ID
	}
	shopIDStr := uid.String()

	// Run three-dimensional checks in parallel
	var ucpSubScore, geoSubScore, seoSubScore visibility.SubScore
	done := make(chan struct{}, 3)
	go func() { ucpSubScore = h.runUCPChecks(ctx, shopIDStr); done <- struct{}{} }()
	go func() { geoSubScore = h.runGEOChecks(ctx, shopIDStr); done <- struct{}{} }()
	go func() { seoSubScore = h.runSEOChecks(ctx, shopIDStr); done <- struct{}{} }()
	for i := 0; i < 3; i++ {
		<-done
	}

	score := visibility.BuildScore(ucpSubScore, geoSubScore, seoSubScore, "")

	// Cache result (TTL 1h)
	jsonBytes, _ := json.Marshal(score)
	if h.cache != nil {
		h.cache.Set(ctx, cacheKey, string(jsonBytes), visibility.CacheTTL)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "MISS")
	w.Write(jsonBytes)
}

// ── GET /api/visibility/product ──────────────────────────────────────────────

// ProductScore returns per-product visibility check results.
// Query params: shop_id (required), product_id (required)
func (h *VisibilityHandler) ProductScore(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	productID := r.URL.Query().Get("product_id")
	if shopID == "" || productID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id and product_id are required")
		return
	}

	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	ctx := r.Context()

	// Fetch the product
	var p model.SyncedProduct
	if err := h.db.WithContext(ctx).
		Where("shop_id = ? AND (platform_id = ? OR id = ?)", shopID, productID, productID).
		First(&p).Error; err != nil {
		httputil.WriteError(w, http.StatusNotFound, "Product not found")
		return
	}

	// Run checks on this single product
	ucpSubScore := h.ucp.CheckProduct(ctx, &p)

	// GEO and SEO are shop-level; for per-product, just run UCP with neutral others
	geoNeutral := visibility.SubScore{Score: 100, Weight: visibility.WeightGEO}
	seoNeutral := visibility.SubScore{Score: 100, Weight: visibility.WeightSEO}
	score := visibility.BuildScore(ucpSubScore, geoNeutral, seoNeutral, p.PlatformID)

	report := visibility.ProductReport{
		ProductID: p.PlatformID,
		Title:     p.Title,
		Overall:   score.Overall,
		UCP:       ucpSubScore,
		GEO:       geoNeutral,
		SEO:       seoNeutral,
		Issues:    score.Issues,
	}

	httputil.WriteOK(w, report)
}

// ── POST /api/visibility/llmstxt ─────────────────────────────────────────────

// GenerateLLMsTxt generates and returns llms.txt content.
// Body: { "shop_id": "xxx" }
func (h *VisibilityHandler) GenerateLLMsTxt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShopID string `json:"shop_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ShopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}
	if !h.checkWriteAccess(w, r, req.ShopID) {
		return
	}

	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	result, err := h.llms.Generate(r.Context(), req.ShopID)
	if err != nil {
		slog.Error("visibility: llmstxt generation failed", "shop_id", req.ShopID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to generate llms.txt")
		return
	}

	// Cache in Redis (TTL 24h — regenerated via daily cron)
	if h.cache != nil {
		cacheKey := fmt.Sprintf("visibility:llmstxt:%s", req.ShopID)
		h.cache.Set(r.Context(), cacheKey, result.Content, 24*time.Hour)
		cacheKeyFull := fmt.Sprintf("visibility:llmstxt-full:%s", req.ShopID)
		h.cache.Set(r.Context(), cacheKeyFull, result.FullContent, 24*time.Hour)
	}

	httputil.WriteOK(w, map[string]any{
		"success":       true,
		"content":       result.Content,
		"full_content":  result.FullContent,
		"product_count": result.ProductCount,
		"sections":      result.Sections,
		"generated_at":  result.GeneratedAt,
	})
}

// ── GET /api/visibility/llmstxt ──────────────────────────────────────────────

// GetLLMsTxt returns cached or freshly generated llms.txt.
func (h *VisibilityHandler) GetLLMsTxt(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	// Try cache first
	if h.cache != nil {
		cacheKey := fmt.Sprintf("visibility:llmstxt:%s", shopID)
		if cached, err := h.cache.Get(r.Context(), cacheKey); err == nil && cached != "" {
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.Write([]byte(cached))
			return
		}
	}

	// Generate on-the-fly
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	result, err := h.llms.Generate(r.Context(), shopID)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to generate llms.txt")
		return
	}

	// Cache
	if h.cache != nil {
		cacheKey := fmt.Sprintf("visibility:llmstxt:%s", shopID)
		h.cache.Set(r.Context(), cacheKey, result.Content, 24*time.Hour)
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Write([]byte(result.Content))
}

// ── GET /api/visibility/product-advice ───────────────────────────────────────

// ProductAdvice returns the current product content vs AI-suggested optimization for comparison.
// Query params: shop_id (required), product_id (required)
// Returns the original content alongside AI-optimized suggestions so the merchant
// can preview changes before applying them.
func (h *VisibilityHandler) ProductAdvice(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	productID := r.URL.Query().Get("product_id")
	if shopID == "" || productID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id and product_id are required")
		return
	}
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	ctx := r.Context()

	// Fetch the product from DB
	var p model.SyncedProduct
	if err := h.db.WithContext(ctx).
		Where("shop_id = ? AND (platform_id = ? OR id = ?)", shopID, productID, productID).
		First(&p).Error; err != nil {
		httputil.WriteError(w, http.StatusNotFound, "Product not found")
		return
	}

	// Check Redis cache for existing optimization result
	var cachedAdvice *visibility.OptimizedContent
	if h.cache != nil {
		cacheKey := fmt.Sprintf("geo:schema:ai:%s:%s", p.Title, p.PlatformID)
		if cached, err := h.cache.Get(ctx, cacheKey); err == nil && cached != "" {
			var oc visibility.OptimizedContent
			if json.Unmarshal([]byte(cached), &oc) == nil {
				cachedAdvice = &oc
			}
		}
	}

	// Generate AI advice if not cached
	var aiAdvice *visibility.OptimizedContent
	if h.optimizer != nil {
		if cachedAdvice != nil {
			aiAdvice = cachedAdvice
		} else {
			optimized, err := h.optimizer.OptimizeDescription(ctx, &p)
			if err != nil {
				slog.Warn("visibility: product-advice optimization failed", "product_id", productID, "error", err)
				// Non-fatal: return nil advice so frontend can show "not yet optimized"
			} else {
				aiAdvice = optimized
				// Cache result for future use
				if h.cache != nil {
					cacheKey := fmt.Sprintf("geo:schema:ai:%s:%s", p.Title, p.PlatformID)
					jsonBytes, _ := json.Marshal(optimized)
					h.cache.Set(ctx, cacheKey, string(jsonBytes), 24*time.Hour)
				}
			}
		}
	}

	// Build response with current content vs AI suggestion
	resp := map[string]any{
		"product_id":   p.PlatformID,
		"title":        p.Title,
		"description":  p.Description,
		"vendor":       p.Vendor,
		"product_type": p.ProductType,
		"tags":         p.Tags,
	}

	if aiAdvice != nil {
		resp["ai_advice"] = map[string]any{
			"title":            aiAdvice.Title,
			"description":      aiAdvice.Description,
			"seo_title":        aiAdvice.SEOTitle,
			"seo_description":  aiAdvice.SEODescription,
			"keywords":         aiAdvice.Keywords,
		}
		// If cached, indicate it rather than regenerated
		resp["advice_source"] = "ai_generated"
		if cachedAdvice != nil {
			resp["advice_source"] = "cached"
		}
	} else {
		resp["ai_advice"] = nil
		resp["advice_source"] = "not_available"
	}

	httputil.WriteOK(w, resp)
}

// ── POST /api/visibility/optimize ────────────────────────────────────────────

// OptimizeDescription triggers AI-powered GEO content optimization for a product.
// If product_id is provided, optimizes that product. Otherwise optimizes top 3 active products.
// Body: { "shop_id": "xxx", "product_id": "xxx" }
func (h *VisibilityHandler) OptimizeDescription(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShopID    string `json:"shop_id"`
		ProductID string `json:"product_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ShopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}
	if !h.checkWriteAccess(w, r, req.ShopID) {
		return
	}

	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	// If optimizer not available, fall back to cache invalidation
	if h.optimizer == nil {
		if h.cache != nil {
			h.cache.Delete(r.Context(), fmt.Sprintf("geo:feed:%s", req.ShopID))
			h.cache.Delete(r.Context(), fmt.Sprintf("visibility:score:%s", req.ShopID))
		}
		httputil.WriteOK(w, map[string]any{
			"success": true,
			"message": "Optimizer not available. Cache cleared — next feed refresh will include fresh data.",
		})
		return
	}

	// Fetch products to optimize
	var products []model.SyncedProduct
	query := h.db.WithContext(r.Context()).Where("shop_id = ? AND status = ?", req.ShopID, "active")
	if req.ProductID != "" {
		query = query.Where("platform_id = ?", req.ProductID)
	}
	query.Limit(5).Find(&products)

	if len(products) == 0 {
		httputil.WriteError(w, http.StatusNotFound, "No active products found to optimize")
		return
	}

	// Run AI optimization
	results, err := h.optimizer.OptimizeBatch(r.Context(), products)
	if err != nil {
		slog.Error("visibility: optimize failed", "shop_id", req.ShopID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Optimization failed: "+err.Error())
		return
	}

	// Cache optimized results in Redis for GEO module
	if h.cache != nil {
		for i, result := range results {
			if i < len(products) {
				cacheKey := fmt.Sprintf("geo:schema:ai:%s:%s", products[i].Title, products[i].PlatformID)
				jsonBytes, _ := json.Marshal(result)
				h.cache.Set(r.Context(), cacheKey, string(jsonBytes), 24*time.Hour)
			}
		}
		h.cache.Delete(r.Context(), fmt.Sprintf("geo:feed:%s", req.ShopID))
		h.cache.Delete(r.Context(), fmt.Sprintf("visibility:score:%s", req.ShopID))
	}

	slog.Info("visibility: GEO content optimized",
		"shop_id", req.ShopID,
		"products_optimized", len(results),
	)

	httputil.WriteOK(w, map[string]any{
		"success":            true,
		"message":            fmt.Sprintf("AI optimization complete — %d products optimized for GEO visibility", len(results)),
		"products_optimized": len(results),
		"results":            results,
	})
}

// ── POST /api/visibility/fix ─────────────────────────────────────────────────

// Fix runs auto-fix for a specific issue.
// Body: { "shop_id": "xxx", "fix_action": "ucp:描述完整度", "product_id": "xxx" }
func (h *VisibilityHandler) Fix(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShopID    string `json:"shop_id"`
		FixAction string `json:"fix_action"`
		ProductID string `json:"product_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ShopID == "" || req.FixAction == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id and fix_action are required")
		return
	}
	if !h.checkWriteAccess(w, r, req.ShopID) {
		return
	}

	// Route fix action to appropriate handler
	switch req.FixAction {
	case "ucp:描述完整度", "geo:描述结构化":
		// AI-powered description optimization
		if req.ProductID == "" {
			httputil.WriteError(w, http.StatusBadRequest, "product_id is required for description optimization")
			return
		}
		var p model.SyncedProduct
		if err := h.db.WithContext(r.Context()).
			Where("shop_id = ? AND platform_id = ?", req.ShopID, req.ProductID).
			First(&p).Error; err != nil {
			httputil.WriteError(w, http.StatusNotFound, "Product not found")
			return
		}
		optimized, err := h.optimizer.OptimizeDescription(r.Context(), &p)
		if err != nil {
			slog.Error("visibility: fix optimize failed", "product", req.ProductID, "error", err)
			httputil.WriteError(w, http.StatusInternalServerError, fmt.Sprintf("Optimization failed: %v", err))
			return
		}
		// Cache result in Redis for GEO module
		if h.cache != nil {
			cacheKey := fmt.Sprintf("geo:schema:ai:%s:%s", p.Title, p.PlatformID)
			jsonBytes, _ := json.Marshal(optimized)
			h.cache.Set(r.Context(), cacheKey, string(jsonBytes), 24*time.Hour)
			h.cache.Delete(r.Context(), fmt.Sprintf("geo:feed:%s", req.ShopID))
			h.cache.Delete(r.Context(), fmt.Sprintf("visibility:score:%s", req.ShopID))
		}
		httputil.WriteOK(w, map[string]any{
			"success":    true,
			"message":    "Description optimized for AI visibility",
			"optimized":  optimized,
		})

	case "geo:FAQ 模块":
		// AI FAQ generation — sample top 3 products and generate FAQ
		var products []model.SyncedProduct
		h.db.WithContext(r.Context()).
			Where("shop_id = ? AND status = ?", req.ShopID, "active").
			Limit(3).Find(&products)
		if len(products) == 0 {
			httputil.WriteError(w, http.StatusNotFound, "No active products found")
			return
		}
		var faqs []visibility.FAQResult
		for _, p := range products {
			faq, err := h.optimizer.GenerateFAQ(r.Context(), &p, nil)
			if err != nil {
				slog.Warn("visibility: FAQ generation failed for product", "product_id", p.PlatformID, "error", err)
				continue
			}
			faqs = append(faqs, *faq)
		}
		httputil.WriteOK(w, map[string]any{
			"success":       true,
			"message":       fmt.Sprintf("FAQ generated for %d products", len(faqs)),
			"faqs":          faqs,
			"total_products": len(products),
		})

	case "geo:标题可搜索性", "ucp:标题具象度":
		// Title optimization for a specific product or the first product
		var p model.SyncedProduct
		query := h.db.WithContext(r.Context()).Where("shop_id = ? AND status = ?", req.ShopID, "active")
		if req.ProductID != "" {
			query = query.Where("platform_id = ?", req.ProductID)
		}
		if err := query.First(&p).Error; err != nil {
			httputil.WriteError(w, http.StatusNotFound, "No active product found")
			return
		}
		optimized, err := h.optimizer.OptimizeDescription(r.Context(), &p)
		if err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, fmt.Sprintf("Title optimization failed: %v", err))
			return
		}
		httputil.WriteOK(w, map[string]any{
			"success":       true,
			"message":       "Title optimization generated. Review before applying.",
			"current_title": p.Title,
			"suggested_title": optimized.Title,
			"seo_title":     optimized.SEOTitle,
			"keywords":      optimized.Keywords,
		})

	case "geo:LLMs.txt 部署", "geo:LLMs-full.txt 部署":
		// Trigger llms.txt generation
		result, err := h.llms.Generate(r.Context(), req.ShopID)
		if err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, "Failed to generate llms.txt")
			return
		}
		if h.cache != nil {
			h.cache.Set(r.Context(), fmt.Sprintf("visibility:llmstxt:%s", req.ShopID), result.Content, 24*time.Hour)
			h.cache.Set(r.Context(), fmt.Sprintf("visibility:llmstxt-full:%s", req.ShopID), result.FullContent, 24*time.Hour)
			h.cache.Delete(r.Context(), fmt.Sprintf("visibility:score:%s", req.ShopID))
		}
		httputil.WriteOK(w, map[string]any{
			"success":       true,
			"message":       "llms.txt generated successfully",
			"product_count": result.ProductCount,
			"content":       result.Content,
		})

	case "seo:Schema JSON-LD":
		// Refresh JSON-LD by invalidating GEO cache
		if h.cache != nil {
			h.cache.Delete(r.Context(), fmt.Sprintf("geo:feed:%s", req.ShopID))
			h.cache.Delete(r.Context(), fmt.Sprintf("geo:schema:%s:*", req.ShopID))
			h.cache.Delete(r.Context(), fmt.Sprintf("visibility:score:%s", req.ShopID))
		}
		httputil.WriteOK(w, map[string]any{
			"success": true,
			"message": "JSON-LD Schema cache cleared. Regenerate via GEO Feed for fresh data.",
		})

	default:
		httputil.WriteError(w, http.StatusBadRequest, fmt.Sprintf("Unknown fix action: %s. Supported: ucp:描述完整度, geo:描述结构化, geo:FAQ 模块, geo:标题可搜索性, geo:LLMs.txt 部署, seo:Schema JSON-LD", req.FixAction))
	}
}

// ── Cron: Daily LLMs.txt Regeneration ───────────────────────────────────────

// RefreshLLMs regenerates llms.txt for the given shop. Used by daily cron.
func (h *VisibilityHandler) RefreshLLMs(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	result, err := h.llms.Generate(r.Context(), shopID)
	if err != nil {
		slog.Error("visibility: cron llms refresh failed", "shop_id", shopID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to refresh llms.txt")
		return
	}

	// Update cache
	if h.cache != nil {
		h.cache.Set(r.Context(), fmt.Sprintf("visibility:llmstxt:%s", shopID), result.Content, 24*time.Hour)
		h.cache.Set(r.Context(), fmt.Sprintf("visibility:llmstxt-full:%s", shopID), result.FullContent, 24*time.Hour)
		h.cache.Delete(r.Context(), fmt.Sprintf("visibility:score:%s", shopID))
	}

	slog.Info("visibility: cron refreshed llms.txt", "shop_id", shopID, "product_count", result.ProductCount)
	httputil.WriteOK(w, map[string]any{
		"success":       true,
		"product_count": result.ProductCount,
		"generated_at":  result.GeneratedAt,
	})
}

// ── Billing Guard ───────────────────────────────────────────────────────────

// visibilityWritePlans defines which plans can use write operations (fix, optimize, generate).
var visibilityWritePlans = map[string]bool{
	"growth": true,
	"pro":    true,
	"enterprise": true,
}

// canWrite checks if the shop's plan allows visibility write operations.
// Results are cached in Redis for 30 minutes to avoid per-request DB queries.
func (h *VisibilityHandler) canWrite(ctx context.Context, shopID string) bool {
	if h.db == nil {
		return true // no DB — allow all in dev
	}

	// Check Redis cache first
	if h.cache != nil {
		cacheKey := fmt.Sprintf("visibility:plan:%s", shopID)
		if cached, err := h.cache.Get(ctx, cacheKey); err == nil && cached != "" {
			return visibilityWritePlans[cached]
		}
	}

	var shop model.Shop
	if err := h.db.WithContext(ctx).Where("shop_domain = ? OR id = ?", shopID, shopID).First(&shop).Error; err != nil {
		return false
	}

	// Cache plan for 30 minutes
	if h.cache != nil {
		cacheKey := fmt.Sprintf("visibility:plan:%s", shopID)
		h.cache.Set(ctx, cacheKey, shop.Plan, 30*time.Minute)
	}

	return visibilityWritePlans[shop.Plan]
}

// checkWriteAccess is middleware-style helper that returns an error response if write access is denied.
func (h *VisibilityHandler) checkWriteAccess(w http.ResponseWriter, r *http.Request, shopID string) bool {
	if !h.canWrite(r.Context(), shopID) {
		httputil.WriteError(w, http.StatusForbidden, "Visibility write operations require Growth plan or higher. Upgrade at /app/plans")
		return false
	}
	return true
}

// ── Scan triggers a full shop visibility scan. ───────────────────────────────

// Scan handles POST /api/visibility/scan
func (h *VisibilityHandler) Scan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShopID string `json:"shop_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ShopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}
	if !h.checkWriteAccess(w, r, req.ShopID) {
		return
	}

	// Trigger scan by invalidating cache — next Score() call will re-compute
	if h.cache != nil {
		h.cache.Delete(r.Context(), fmt.Sprintf("visibility:score:%s", req.ShopID))
	}

	slog.Info("visibility: scan triggered", "shop_id", req.ShopID)

	httputil.WriteOK(w, map[string]any{
		"success": true,
		"message": "Full visibility scan triggered. Call GET /visibility/score to see results.",
	})
}

// ── Private Helpers ──────────────────────────────────────────────────────────

func (h *VisibilityHandler) runUCPChecks(ctx context.Context, shopID string) visibility.SubScore {
	// UCP: sample top 30 products and aggregate
	reports, err := h.ucp.CheckAllProducts(ctx, shopID, 30)
	if err != nil || len(reports) == 0 {
		slog.Warn("visibility: ucp check failed or empty", "shop_id", shopID, "error", err)
		return visibility.SubScore{Score: 50, Weight: visibility.WeightUCP}
	}

	// Aggregate: average UCP score across sampled products
	var total float64
	for _, r := range reports {
		total += r.UCP.Score
	}
	avgScore := total / float64(len(reports))

	// Collect checks from first product as representative
	var checks []visibility.CheckItem
	if len(reports) > 0 {
		checks = reports[0].UCP.Checks
	}

	return visibility.SubScore{
		Score:  avgScore,
		Weight: visibility.WeightUCP,
		Checks: checks,
	}
}

func (h *VisibilityHandler) runGEOChecks(ctx context.Context, shopID string) visibility.SubScore {
	// Check if llms.txt is deployed (via cache presence)
	llmsDeployed := false
	if h.cache != nil {
		cacheKey := fmt.Sprintf("visibility:llmstxt:%s", shopID)
		if cached, err := h.cache.Get(ctx, cacheKey); err == nil && cached != "" {
			llmsDeployed = true
		}
		// Also check the legacy geo:llms cache
		if !llmsDeployed {
			cacheKey2 := fmt.Sprintf("geo:llms:%s", shopID)
			if cached, err := h.cache.Get(ctx, cacheKey2); err == nil && cached != "" {
				llmsDeployed = true
			}
		}
	}

	return h.geo.CheckShop(ctx, shopID, llmsDeployed)
}

func (h *VisibilityHandler) runSEOChecks(ctx context.Context, shopID string) visibility.SubScore {
	return h.seo.CheckShop(ctx, shopID)
}
