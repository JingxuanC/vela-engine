package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
		"sort"
	"strings"
	
	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"net/http"
	"gorm.io/gorm"
)

// ExchangeHandler generates AI-powered exchange recommendations.
// Transaction operations (Create/Approve/Ship/Complete) are handled by Shopify.
type ExchangeHandler struct {
	db        *gorm.DB
	llmRouter *service.LLMRouter
}

// NewExchangeHandler creates a new ExchangeHandler.
func NewExchangeHandler(db *gorm.DB, llmRouter *service.LLMRouter) *ExchangeHandler {
	return &ExchangeHandler{db: db, llmRouter: llmRouter}
}

// ExchangeRecommendation is a suggested product for exchange.
type ExchangeRecommendation struct {
	ProductID   string  `json:"product_id"`
	VariantID   string  `json:"variant_id,omitempty"`
	Title       string  `json:"title"`
	ImageURL    string  `json:"image_url"`
	Price       float64 `json:"price"`
	PriceDiff   float64 `json:"price_diff"`
	MatchScore  float64 `json:"match_score"`
	MatchReason string  `json:"match_reason"`
}

// RecommendRequest holds the return context for exchange recommendations.
type RecommendRequest struct {
	ReturnID     string  `json:"return_id"`
	ProductID    string  `json:"product_id"`
	ProductTitle string  `json:"product_title"`
	Category     string  `json:"category"`
	Price        float64 `json:"price"`
	Size         string  `json:"size"`
	ShopID       string  `json:"shop_id"`
}

// RecommendResponse holds exchange recommendations.
type RecommendResponse struct {
	Success         bool                     `json:"success"`
	Recommendations []ExchangeRecommendation `json:"recommendations"`
	Error           string                   `json:"error,omitempty"`
}

// scoredCandidate holds a candidate product with its computed score.
type scoredCandidate struct {
	product   model.SyncedProduct
	score     float64
	reason    string
	price     float64
	priceDiff float64
}

// Recommend handles POST /api/exchange/recommend — recommends products for exchange.
func (h *ExchangeHandler) Recommend(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	var req RecommendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	if req.ProductID == "" || req.ShopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "product_id and shop_id are required")
		return
	}

	shopUID, err := database.ResolveShopID(h.db, req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}

	// 1. Look up the original product to get its category/product type
	var origProduct model.SyncedProduct
	if err := h.db.Where("shop_id = ? AND (platform_id = ? OR title = ?)", shopUID, req.ProductID, req.ProductTitle).
		First(&origProduct).Error; err != nil {
		slog.Warn("original product not found in local DB, using category-based fallback",
			"product_id", req.ProductID, "error", err)
	}

	category := req.Category
	if category == "" && origProduct.ProductType != "" {
		category = origProduct.ProductType
	}

	// 2. Check if the reason indicates a size issue
	isSizeIssue := strings.Contains(strings.ToLower(req.Size), "size") ||
		strings.Contains(strings.ToLower(req.Size), "fit") ||
		strings.Contains(strings.ToLower(req.Size), "large") ||
		strings.Contains(strings.ToLower(req.Size), "small") ||
		strings.Contains(strings.ToLower(req.Size), "tight")

	// 3. Query candidates from SyncedProduct table
	var candidates []model.SyncedProduct
	query := h.db.Where("shop_id = ? AND status = ?", shopUID, "active")

	if isSizeIssue && category != "" && req.ProductTitle != "" {
		// Size issue: first try same product title, different variant (size)
		query = query.Where("title = ? OR product_type = ?", req.ProductTitle, category)
	} else if category != "" {
		query = query.Where("product_type = ?", category)
	}

	query.Limit(50).Find(&candidates)

	if len(candidates) == 0 && category != "" {
		h.db.Where("shop_id = ? AND status = ?", shopUID, "active").
			Limit(50).Find(&candidates)
	}

	if len(candidates) == 0 {
		httputil.WriteOK(w, RecommendResponse{
			Success:         true,
			Recommendations: []ExchangeRecommendation{},
		})
		return
	}

	// 4. Filter by stock availability and build candidate list
	var scoredList []scoredCandidate

	for _, p := range candidates {
		if p.PlatformID == req.ProductID {
			continue // skip the original product itself
		}

		// Parse variants to check stock and get price
		var variants []map[string]interface{}
		if err := json.Unmarshal(p.Variants, &variants); err != nil {
			slog.Warn("failed to parse variants", "product_id", p.PlatformID, "error", err)
			continue
		}

		// Check stock availability and pick first in-stock variant
		var bestVariant map[string]interface{}
		hasStock := false
		for _, v := range variants {
			var invQty int
			switch iv := v["inventory_quantity"].(type) {
			case float64:
				invQty = int(iv)
			case int:
				invQty = iv
			}
			if invQty > 0 {
				hasStock = true
				bestVariant = v
				break
			}
		}
		if !hasStock {
			continue
		}

		// Get price from variant
		price := req.Price
		if bestVariant != nil {
			switch pv := bestVariant["price"].(type) {
			case float64:
				price = pv
			case string:
				fmt.Sscanf(pv, "%f", &price)
			}
		}

		priceDiff := price - req.Price

		// Heuristic scoring
		score := 0.5
		reason := "Similar product in same category"

		if isSizeIssue && p.Title == req.ProductTitle {
			score = 0.85
			reason = "Same product, different size — recommended for size exchange"
		} else if p.Vendor != "" && p.Vendor == origProduct.Vendor {
			score = 0.75
			reason = "Same brand, similar style"
		} else if p.ProductType == category {
			score = 0.65
			if p.Vendor != "" {
				reason = fmt.Sprintf("Same category from %s", p.Vendor)
			}
		}

		// Price proximity bonus
		priceRatio := math.Abs(price-req.Price) / math.Max(req.Price, 0.01)
		if priceRatio < 0.1 {
			score = math.Min(1.0, score+0.1)
			reason += ", similar price range"
		} else if priceRatio > 0.5 {
			score = math.Max(0.3, score-0.15)
			reason += ", different price range"
		}

		score = math.Min(1.0, score)

		scoredList = append(scoredList, scoredCandidate{
			product:   p,
			price:     price,
			priceDiff: priceDiff,
			score:     score,
			reason:    reason,
		})
	}

	if len(scoredList) == 0 {
		httputil.WriteOK(w, RecommendResponse{
			Success:         true,
			Recommendations: []ExchangeRecommendation{},
		})
		return
	}

	// 5. LLM scoring for match quality (if DashScope client is available)
	if h.llmRouter != nil {
		h.enhanceWithLLM(r.Context(), req, scoredList)
	}

	// 6. Sort by score descending, take top 5
	sort.Slice(scoredList, func(i, j int) bool {
		return scoredList[i].score > scoredList[j].score
	})

	if len(scoredList) > 5 {
		scoredList = scoredList[:5]
	}

	// 7. Build response
	recs := make([]ExchangeRecommendation, 0, len(scoredList))
	for _, s := range scoredList {
		imgURL := extractFirstImage(s.product.Images)
		recs = append(recs, ExchangeRecommendation{
			ProductID:   s.product.PlatformID,
			Title:       s.product.Title,
			ImageURL:    imgURL,
			Price:       s.price,
			PriceDiff:   math.Round(s.priceDiff*100) / 100,
			MatchScore:  math.Round(s.score*100) / 100,
			MatchReason: s.reason,
		})
	}

	slog.Info("exchange recommend",
		"product_id", req.ProductID,
		"category", category,
		"candidates_found", len(candidates),
		"recommendations", len(recs),
	)

	httputil.WriteOK(w, RecommendResponse{
		Success:         true,
		Recommendations: recs,
	})
}

// enhanceWithLLM calls LLM to score products for match quality.
func (h *ExchangeHandler) enhanceWithLLM(ctx context.Context, req RecommendRequest, candidates []scoredCandidate) {
	type productBrief struct {
		ID          string  `json:"id"`
		Title       string  `json:"title"`
		Vendor      string  `json:"vendor"`
		Price       float64 `json:"price"`
		ProductType string  `json:"product_type"`
	}

	var briefs []productBrief
	for _, c := range candidates {
		briefs = append(briefs, productBrief{
			ID:          c.product.PlatformID,
			Title:       c.product.Title,
			Vendor:      c.product.Vendor,
			Price:       c.price,
			ProductType: c.product.ProductType,
		})
	}

	briefJSON, _ := json.Marshal(briefs)

	prompt := fmt.Sprintf(`You are a product matching expert for an e-commerce exchange system.
Original product: "%s" (category: %s, price: $%.2f)
Return reason / size context: %s

Below are candidate products for exchange. For each, return a JSON object mapping product_id -> {"score": <0-1 float>, "reason": "<brief explanation>"}.
Score 1.0 = perfect match, 0.0 = not suitable.
Consider: same category relevance, brand affinity, price proximity, style similarity.

Candidates: %s

Return ONLY valid JSON.`,
		req.ProductTitle, req.Category, req.Price, req.Size, string(briefJSON))

	chatReq := &service.ChatCompletionRequest{
		Messages: []service.ChatMessage{
			{Role: "system", Content: "You are a product matching expert. Return only valid JSON."},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.3,
		MaxTokens:   1024,
	}

	exProvider, exCfg, exErr := h.llmRouter.GetProvider(ctx, uuid.Nil)
	if exErr != nil {
		slog.Warn("exchange LLM provider unavailable", "error", exErr)
		return
	}
	chatReq.Model = exCfg.Model
	raw, err := exProvider.ChatCompletion(ctx, chatReq)
	if err != nil {
		slog.Warn("exchange LLM scoring failed, using heuristics", "error", err)
		return
	}

	var parsed map[string]map[string]interface{}
	if err := json.Unmarshal(service.ExtractJSONHelper(raw), &parsed); err != nil {
		slog.Warn("exchange LLM response parse failed", "error", err)
		return
	}

	for i := range candidates {
		if s, ok := parsed[candidates[i].product.PlatformID]; ok {
			if score, ok := s["score"].(float64); ok {
				candidates[i].score = math.Max(0, math.Min(1, score))
			}
			if reason, ok := s["reason"].(string); ok {
				candidates[i].reason = reason
			}
		}
	}
}

// List handles GET /api/exchange — returns stored exchange recommendations.
func (h *ExchangeHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}
	shopUID, err := database.ResolveShopID(h.db, shopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id")
		return
	}
	var recs []model.ExchangeAIRecommendation
	h.db.WithContext(r.Context()).Where("shop_id = ?", shopUID).
		Order("created_at DESC").Limit(50).Find(&recs)
	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"recommendations": recs,
		"total": len(recs),
	})
}

// RecommendForReturn generates exchange recommendations directly (not via HTTP).
// Called by EventBus consumer when a return webhook arrives.
func (h *ExchangeHandler) RecommendForReturn(ctx context.Context, shopID, productID, productTitle, category string, price float64, returnReason string) []ExchangeRecommendation {
	if h.db == nil { return nil }
	shopUID, err := database.ResolveShopID(h.db, shopID)
	if err != nil { return nil }

	// Look up original product
	var origProduct model.SyncedProduct
	h.db.WithContext(ctx).Where("shop_id = ? AND platform_id = ?", shopUID, productID).First(&origProduct)
	if category == "" && origProduct.ProductType != "" {
		category = origProduct.ProductType
	}

	// Query candidates from same category
	var candidates []model.SyncedProduct
	query := h.db.WithContext(ctx).Where("shop_id = ? AND status = ?", shopUID, "active")
	if category != "" {
		query = query.Where("product_type = ?", category)
	}
	query.Limit(30).Find(&candidates)
	if len(candidates) == 0 { return nil }

	// Score and filter
	var scored []scoredCandidate
	for _, p := range candidates {
		if p.PlatformID == productID { continue }
		var variants []map[string]interface{}
		json.Unmarshal(p.Variants, &variants)
		hasStock := false
		var bestPrice float64
		for _, v := range variants {
			var qty int
			switch iv := v["inventory_quantity"].(type) {
			case float64: qty = int(iv)
			case int: qty = iv
			}
			if qty > 0 {
				hasStock = true
				switch pv := v["price"].(type) {
				case float64: bestPrice = pv
				case string: fmt.Sscanf(pv, "%f", &bestPrice)
				}
				break
			}
		}
		if !hasStock { continue }
		score := 0.5
		reason := "同品类替代产品"
		if p.Vendor != "" && p.Vendor == origProduct.Vendor { score = 0.7; reason = "同品牌替代产品" }
		if p.Title == origProduct.Title { score = 0.85; reason = "同款不同规格" }
		scored = append(scored, scoredCandidate{p, score, reason, bestPrice, bestPrice - price})
	}

	// Sort by score desc
	for i := 1; i < len(scored); i++ {
		for j := i; j > 0 && scored[j].score > scored[j-1].score; j-- {
			scored[j], scored[j-1] = scored[j-1], scored[j]
		}
	}
	if len(scored) > 5 { scored = scored[:5] }

	results := make([]ExchangeRecommendation, len(scored))
	for i, sc := range scored {
		results[i] = ExchangeRecommendation{
			ProductID:   sc.product.PlatformID,
			Title:       sc.product.Title,
			Price:       sc.price,
			PriceDiff:   sc.priceDiff,
			MatchScore:  sc.score,
			MatchReason: sc.reason,
		}
	}
	return results
}

// GetStatus handles GET /api/exchange/{exchangeID}.

// --- Helpers ---

// extractFirstImage extracts the first image URL from the Images JSON field.
func extractFirstImage(imagesJSON []byte) string {
	if len(imagesJSON) == 0 {
		return ""
	}
	var images []map[string]interface{}
	if err := json.Unmarshal(imagesJSON, &images); err != nil {
		return ""
	}
	if len(images) > 0 {
		if src, ok := images[0]["src"].(string); ok {
			return src
		}
	}
	return ""
}

// formatPriceDiff returns a human-readable price difference string.
func formatPriceDiff(diff float64) string {
	if diff > 0 {
		return fmt.Sprintf("You need to pay $%.2f more", diff)
	} else if diff < 0 {
		return fmt.Sprintf("You will get $%.2f refunded", -diff)
	}
	return "No price difference"
}
