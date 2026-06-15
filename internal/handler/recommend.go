package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"gorm.io/gorm"
)

// RecommendHandler handles personalized product recommendations.
type RecommendHandler struct {
	db *gorm.DB
}

// NewRecommendHandler creates a new RecommendHandler.
func NewRecommendHandler(db *gorm.DB) *RecommendHandler {
	return &RecommendHandler{db: db}
}

// RecommendProduct represents a recommended product.
type RecommendProduct struct {
	ProductID string  `json:"product_id"`
	Title     string  `json:"title"`
	ImageURL  string  `json:"image_url"`
	Price     float64 `json:"price"`
	Score     float64 `json:"score"`
	Reason    string  `json:"reason"`
}

// RecommendProductsRequest holds the recommendation context.
type RecommendProductsRequest struct {
	ShopID      string `json:"shop_id"`
	Context     string `json:"context"`
	ReferenceID string `json:"reference_id"`
	Limit       int    `json:"limit"`
}

// RecommendProductsResponse holds the recommendations.
type RecommendProductsResponse struct {
	Success         bool               `json:"success"`
	Recommendations []RecommendProduct `json:"recommendations"`
	Strategy        string             `json:"strategy"`
	Error           string             `json:"error,omitempty"`
}

// Get handles POST /api/recommend — returns personalized recommendations from local DB.
func (h *RecommendHandler) Get(w http.ResponseWriter, r *http.Request) {
	var req RecommendProductsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if req.Limit == 0 {
		req.Limit = 6
	}

	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	shopUID, err := database.ResolveShopID(h.db, req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id: "+err.Error())
		return
	}

	var recs []RecommendProduct
	strategy := "content_similarity"

	switch req.Context {
	case "you_may_also_like":
		strategy = "you_may_also_like"
		recs = h.youMayAlsoLike(shopUID, req.ReferenceID, req.Limit)

	case "frequently_bought_together":
		strategy = "frequently_bought_together"
		recs = h.frequentlyBoughtTogether(shopUID, req.ReferenceID, req.Limit)

	case "trending":
		strategy = "trending"
		recs = h.trendingProducts(shopUID, req.Limit)

	case "top_sellers":
		strategy = "top_sellers"
		recs = h.topSellers(shopUID, req.Limit)

	default:
		// Default: mix of you_may_also_like and trending
		recs = h.youMayAlsoLike(shopUID, req.ReferenceID, req.Limit)
		if len(recs) < req.Limit {
			trending := h.trendingProducts(shopUID, req.Limit-len(recs))
			recs = append(recs, trending...)
		}
	}

	if len(recs) == 0 {
		slog.Warn("recommend: no products found in local DB", "shop_id", req.ShopID, "context", req.Context)
	}

	httputil.WriteOK(w, RecommendProductsResponse{
		Success:         true,
		Recommendations: recs,
		Strategy:        strategy,
	})
}

// youMayAlsoLike finds products in the same category but different vendor.
func (h *RecommendHandler) youMayAlsoLike(shopID uuid.UUID, referenceID string, limit int) []RecommendProduct {
	// First, find the reference product's category
	var refProduct model.SyncedProduct
	if err := h.db.Where("shop_id = ? AND platform_id = ?", shopID, referenceID).First(&refProduct).Error; err != nil {
		return nil
	}

	// Query same product type / category, different product
	var products []model.SyncedProduct
	query := h.db.Where("shop_id = ? AND status = ? AND platform_id != ?", shopID, "active", referenceID)

	if refProduct.ProductType != "" {
		query = query.Where("product_type = ?", refProduct.ProductType)
	}

	query.Limit(limit + 5).Find(&products)

	if len(products) == 0 && refProduct.Vendor != "" {
		// Fallback: same vendor
		h.db.Where("shop_id = ? AND status = ? AND platform_id != ? AND vendor = ?",
			shopID, "active", referenceID, refProduct.Vendor).Limit(limit + 5).Find(&products)
	}

	if len(products) == 0 {
		// Broad fallback: any active product
		h.db.Where("shop_id = ? AND status = ?", shopID, "active").
			Limit(limit + 5).Find(&products)
	}

	return productsToRecommendations(products, limit, "You May Also Like")
}

// frequentlyBoughtTogether finds products that appear together in orders.
func (h *RecommendHandler) frequentlyBoughtTogether(shopID uuid.UUID, referenceID string, limit int) []RecommendProduct {
	// Query SyncedOrder for orders containing the reference product's line items
	// Then find products that co-occur most frequently
	type coOccurrence struct {
		ProductID string
		Count     int
	}

	// Find all orders that contain this product
	var orders []model.SyncedOrder
	h.db.Where("shop_id = ?", shopID).Limit(100).Find(&orders)

	coOccurMap := make(map[string]int)
	for _, order := range orders {
		var lineItems []map[string]interface{}
		if err := json.Unmarshal(order.LineItems, &lineItems); err != nil {
			continue
		}
		hasReference := false
		var otherIDs []string
		for _, item := range lineItems {
			pid, _ := item["product_id"].(string)
			if pid == referenceID {
				hasReference = true
			} else {
				otherIDs = append(otherIDs, pid)
			}
		}
		if hasReference {
			for _, id := range otherIDs {
				coOccurMap[id]++
			}
		}
	}

	if len(coOccurMap) == 0 {
		// Fallback to same-category products
		return h.youMayAlsoLike(shopID, referenceID, limit)
	}

	// Sort by co-occurrence count
	type pair struct {
		id    string
		count int
	}
	var sorted []pair
	for id, count := range coOccurMap {
		sorted = append(sorted, pair{id, count})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].count > sorted[j].count
	})

	if len(sorted) > limit {
		sorted = sorted[:limit]
	}

	// Fetch product details in bulk
	var productIDs []string
	for _, p := range sorted {
		productIDs = append(productIDs, p.id)
	}

	var products []model.SyncedProduct
	if len(productIDs) > 0 {
		h.db.Where("shop_id = ? AND platform_id IN ?", shopID, productIDs).Find(&products)
	}

	productMap := make(map[string]model.SyncedProduct, len(products))
	for _, prod := range products {
		productMap[prod.PlatformID] = prod
	}

	var recs []RecommendProduct
	for _, p := range sorted {
		prod, ok := productMap[p.id]
		if !ok {
			continue
		}
		imgURL := extractFirstImage(prod.Images)
		price := extractPrice(prod.Variants)
		recs = append(recs, RecommendProduct{
			ProductID: prod.PlatformID,
			Title:     prod.Title,
			ImageURL:  imgURL,
			Price:     price,
			Score:     math.Min(1.0, float64(p.count)/10.0),
			Reason:    fmt.Sprintf("Frequently Bought Together (%dx co-purchased)", p.count),
		})
	}

	if len(recs) < limit {
		fallback := h.youMayAlsoLike(shopID, referenceID, limit-len(recs))
		recs = append(recs, fallback...)
	}

	return recs
}

// trendingProducts returns the most recently updated products.
func (h *RecommendHandler) trendingProducts(shopID uuid.UUID, limit int) []RecommendProduct {
	var products []model.SyncedProduct
	h.db.Where("shop_id = ? AND status = ?", shopID, "active").
		Order("updated_at DESC").Limit(limit).Find(&products)
	return productsToRecommendations(products, limit, "Trending Now")
}

// topSellers returns products with best seller priority (approximation: most recently updated).
func (h *RecommendHandler) topSellers(shopID uuid.UUID, limit int) []RecommendProduct {
	var products []model.SyncedProduct
	h.db.Where("shop_id = ? AND status = ?", shopID, "active").
		Order("title ASC").Limit(limit).Find(&products)
	return productsToRecommendations(products, limit, "Best Seller")
}

// --- Helpers ---

// productsToRecommendations converts SyncedProduct slice to RecommendProduct slice.
func productsToRecommendations(products []model.SyncedProduct, limit int, defaultReason string) []RecommendProduct {
	recs := make([]RecommendProduct, 0, minInt(len(products), limit))
	for i, p := range products {
		if i >= limit {
			break
		}
		imgURL := extractFirstImage(p.Images)
		price := extractPrice(p.Variants)
		score := 1.0 - float64(i)*0.05 // decreasing score
		if score < 0.5 {
			score = 0.5
		}
		reason := defaultReason
		if p.Vendor != "" {
			reason = fmt.Sprintf("%s from %s", defaultReason, p.Vendor)
		}
		recs = append(recs, RecommendProduct{
			ProductID: p.PlatformID,
			Title:     p.Title,
			ImageURL:  imgURL,
			Price:     price,
			Score:     math.Round(score*100) / 100,
			Reason:    reason,
		})
	}
	return recs
}

// extractPrice gets the price from the first variant in the JSON.
func extractPrice(variantsJSON []byte) float64 {
	if len(variantsJSON) == 0 {
		return 0
	}
	var variants []map[string]interface{}
	if err := json.Unmarshal(variantsJSON, &variants); err != nil {
		return 0
	}
	if len(variants) > 0 {
		switch pv := variants[0]["price"].(type) {
		case float64:
			return pv
		case string:
			var price float64
			fmt.Sscanf(pv, "%f", &price)
			return price
		}
	}
	return 0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
