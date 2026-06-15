package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"gorm.io/gorm"
)

// RecommendationsHandler handles product recommendation API endpoints.
type RecommendationsHandler struct {
	db     *gorm.DB
	engine *service.RecommendationEngine
}

// NewRecommendationsHandler creates a new RecommendationsHandler.
func NewRecommendationsHandler(db *gorm.DB, engine *service.RecommendationEngine) *RecommendationsHandler {
	return &RecommendationsHandler{db: db, engine: engine}
}

// RecommendationsResponse is the API response for recommendations.
type RecommendationsResponse struct {
	Success         bool                            `json:"success"`
	Strategy        string                          `json:"strategy"`
	Recommendations []service.RecommendationResult  `json:"recommendations"`
}

// FBT handles GET /api/recommendations/fbt?product_id=123&limit=4
func (h *RecommendationsHandler) FBT(w http.ResponseWriter, r *http.Request) {
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

	productIDStr := r.URL.Query().Get("product_id")
	if productIDStr == "" {
		httputil.WriteError(w, http.StatusBadRequest, "product_id is required")
		return
	}
	productID, err := strconv.ParseInt(productIDStr, 10, 64)
	if err != nil || productID <= 0 {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid product_id")
		return
	}

	limit := parseIntParam(r, "limit", 4, 20)

	if h.engine == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Recommendation engine not available")
		return
	}

	recs, err := h.engine.GetRecommendations(r.Context(), shopID, productID, "fbt", limit)
	if err != nil {
		slog.Error("recommendations: FBT query failed", "shop_id", shopID, "product_id", productID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to get recommendations")
		return
	}

	if recs == nil {
		recs = []service.RecommendationResult{}
	}

	httputil.WriteOK(w, RecommendationsResponse{
		Success:         true,
		Strategy:        "fbt",
		Recommendations: recs,
	})
}

// Trending handles GET /api/recommendations/trending?limit=8
func (h *RecommendationsHandler) Trending(w http.ResponseWriter, r *http.Request) {
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

	limit := parseIntParam(r, "limit", 8, 20)

	if h.engine == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Recommendation engine not available")
		return
	}

	// For trending, query product_affinities where product_id_b = 0 (trending strategy)
	recs, err := h.engine.GetTrending(r.Context(), shopID, limit)
	if err != nil {
		slog.Error("recommendations: Trending query failed", "shop_id", shopID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to get trending products")
		return
	}

	if recs == nil {
		recs = []service.RecommendationResult{}
	}

	httputil.WriteOK(w, RecommendationsResponse{
		Success:         true,
		Strategy:        "trending",
		Recommendations: recs,
	})
}

// parseIntParam parses a query parameter with a default and max value.
func parseIntParam(r *http.Request, name string, defaultVal, maxVal int) int {
	val := defaultVal
	if s := r.URL.Query().Get(name); s != "" {
		if parsed, err := strconv.Atoi(s); err == nil && parsed > 0 {
			val = parsed
		}
	}
	if val > maxVal {
		val = maxVal
	}
	return val
}

// Stats handles GET /api/recommendations/stats
func (h *RecommendationsHandler) Stats(w http.ResponseWriter, r *http.Request) {
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

	// Count FBT pairs
	var fbtCount int64
	h.db.WithContext(r.Context()).Model(&model.ProductAffinity{}).
		Where("shop_id = ? AND strategy = ?", shopID, "fbt").
		Count(&fbtCount)

	// Count trending products
	var trendingCount int64
	h.db.WithContext(r.Context()).Model(&model.ProductAffinity{}).
		Where("shop_id = ? AND strategy = ? AND product_id_b = 0", shopID, "trending").
		Count(&trendingCount)

	// Get top trending products for display
	var topProducts []model.ProductAffinity
	h.db.WithContext(r.Context()).
		Where("shop_id = ? AND strategy = ? AND product_id_b = 0", shopID, "trending").
		Order("score DESC").
		Limit(10).
		Find(&topProducts)

	type topProductItem struct {
		ProductID string `json:"product_id"`
		Title     string `json:"title"`
		RecRevenue float64 `json:"rec_revenue"`
	}
	topItems := make([]topProductItem, 0, len(topProducts))
	for _, p := range topProducts {
		topItems = append(topItems, topProductItem{
			ProductID: fmt.Sprintf("%d", p.ProductID_A),
			Title:     p.ProductTitle,
			RecRevenue: 0, // TODO: track revenue attribution in Phase 3b
		})
	}
	if topItems == nil {
		topItems = []topProductItem{}
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success":            true,
		"fbt_impressions":    0,    // TODO: track impressions in Phase 3b
		"fbt_clicks":         0,
		"fbt_ctr":            0,
		"trending_impressions": 0,
		"trending_clicks":      0,
		"trending_ctr":          0,
		"fbt_pair_count":      fbtCount,
		"trending_count":      trendingCount,
		"top_products":        topItems,
	})
}
