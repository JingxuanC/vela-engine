package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"github.com/google/uuid"
	"strconv"
	"strings"
	"time"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/vertical"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// UsageHandler handles shop usage/analytics endpoints.
type UsageHandler struct {
	cache *service.CacheService
	db    *gorm.DB
}

// NewUsageHandler creates a new UsageHandler.
func NewUsageHandler(cache *service.CacheService, db *gorm.DB) *UsageHandler {
	return &UsageHandler{cache: cache, db: db}
}

// Feature display names mapped by feature ID.
// Includes both URL-path-based keys (from gateway middleware) and
// frontend feature IDs (from @aitools-shared/features.ts FEATURES).
var featureDisplayNames = map[string]string{
	"tryon":       "Virtual Try-On",
	"seo":         "SEO Optimizer",
	"description": "Product Description",
	"desc":        "Product Description",
	"size":        "Size Recommendation",
	"image":       "Background Removal",
	"bgremove":    "Background Removal",
	"review":      "Review Analysis",
	"chat":        "AI Chat",
	"insights":    "Business Insights",
}

// UsageResponse is the JSON response for GET /api/shop/usage.
type UsageResponse struct {
	MonthlyCreditsUsed  int            `json:"monthly_credits_used"`
	DailyUsage          []int          `json:"daily_usage"`
	FeatureBreakdown    []FeatureUsage `json:"feature_breakdown"`
	Plan                string         `json:"plan"`
	MonthlyCreditsTotal int            `json:"monthly_credits_total"`
	TotalTryOns         int            `json:"total_tryons"`
	TotalAddToCart      int            `json:"total_add_to_cart"`
}

// FeatureUsage represents per-feature usage for the response.
type FeatureUsage struct {
	FeatureID string `json:"feature_id"`
	Name      string `json:"name"`
	Count     int    `json:"count"`
}

// GetUsage handles GET /api/shop/usage?shop_id=xxx.
// It queries Redis for quota data and returns aggregated monthly/daily/feature usage
// plus plan info for the dashboard.
// Key format used by gateway middleware: quota:{feature}:{date}
func (h *UsageHandler) GetUsage(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	ctx := r.Context()
	today := time.Now()
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())

	features := []string{"tryon", "seo", "description", "size", "image", "review", "chat", "insights"}

	featureTotals := make(map[string]int)
	monthlyCredits := 0
	dailyUsage := make([]int, 15)

	// Iterate over every date in the current month up to today.
	// Gateway middleware writes quota keys as: quota:{feature}:{date}
	// See: middleware/gateway.go CheckQuota → INCR quota:<feature>:<date>
	for d := monthStart; !d.After(today); d = d.AddDate(0, 0, 1) {
		date := d.Format("20060102")
		dayTotal := 0

		for _, feature := range features {
			key := fmt.Sprintf("quota:%s:%s", feature, date)
			countStr, err := h.cache.Get(ctx, key)
			if err != nil || countStr == "" {
				continue
			}
			count := parseCount(countStr)
			if count > 0 {
				featureTotals[feature] += count
				dayTotal += count
			}
		}

		monthlyCredits += dayTotal

		// Populate last-15-days slot if this date falls in the window.
		daysAgo := int(today.Sub(d).Hours() / 24)
		if daysAgo >= 0 && daysAgo < 15 {
			dailyUsage[14-daysAgo] = dayTotal
		}
	}

	// Build feature breakdown from aggregated totals.
	featureBreakdown := make([]FeatureUsage, 0, len(featureTotals))
	for feature, count := range featureTotals {
		name := featureDisplayNames[feature]
		if name == "" {
			name = feature
		}
		featureBreakdown = append(featureBreakdown, FeatureUsage{
			FeatureID: feature,
			Name:      name,
			Count:     count,
		})
	}

	// Determine plan and monthly credits total.
	// Default to 5000 (growth plan) — in production this would come from DB/Shopify.
	plan := "growth"
	monthlyCreditsTotal := 5000

	// Calculate conversion rate from usage data if we have both tryon and add-to-cart.
	// tryon returns stored in Redis; add-to-cart tracks via a separate event.
	tryonTotal := featureTotals["tryon"]
	totalAddToCart := 0
	// Read add-to-cart events from monthly data
	for d := monthStart; !d.After(today); d = d.AddDate(0, 0, 1) {
		date := d.Format("20060102")
		key := fmt.Sprintf("event:add_to_cart:%s:%s", shopID, date)
		countStr, err := h.cache.Get(ctx, key)
		if err != nil || countStr == "" {
			continue
		}
		totalAddToCart += parseCount(countStr)
	}

	httputil.WriteOK(w, UsageResponse{
		MonthlyCreditsUsed:  monthlyCredits,
		DailyUsage:          dailyUsage,
		FeatureBreakdown:    featureBreakdown,
		Plan:                plan,
		MonthlyCreditsTotal: monthlyCreditsTotal,
		TotalTryOns:         tryonTotal,
		TotalAddToCart:      totalAddToCart,
	})
}

// FeaturesResponse is the JSON response for GET /api/shop/features.
type FeaturesResponse struct {
	Enabled []string `json:"enabled"`
}

// GetFeatures handles GET /api/shop/features?shop_id=xxx.
// Returns the list of enabled feature IDs for the storefront to conditionally render widgets.
func (h *UsageHandler) GetFeatures(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	ctx := r.Context()
	key := fmt.Sprintf("aitools:features:shop_%s", shopID)
	cacheTTL := 5 * time.Minute

	// 1. Check Redis for cached features (includes custom overrides)
	raw, err := h.cache.Get(ctx, key)
	if err == nil && raw != "" {
		var data struct{ Enabled []string `json:"enabled"` }
		if json.Unmarshal([]byte(raw), &data) == nil {
			httputil.WriteOK(w, FeaturesResponse{Enabled: data.Enabled})
			return
		}
	}

	// 2. Compute defaults, gated by vertical
	enabled := []string{"seo", "desc", "bgremove", "review", "chat", "insights"}
	if h.db != nil {
		shopUID, parseErr := uuid.Parse(shopID)
		if parseErr == nil && vertical.HasFeature(h.db, shopUID, "tryon") {
			enabled = append(enabled, "tryon")
		}
	}

	// 3. Cache the result in Redis for subsequent requests
	encoded, _ := json.Marshal(map[string][]string{"enabled": enabled})
	h.cache.Set(ctx, key, string(encoded), cacheTTL)

	httputil.WriteOK(w, FeaturesResponse{Enabled: enabled})
}

// parseCount converts a Redis string value to int.
func parseCount(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0
	}
	return n
}

// GetFunnel handles GET /api/shop/funnel
func (h *UsageHandler) GetFunnel(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id required")
		return
	}
	// Read real counters from Redis (with fallback to 0)
	getCount := func(key string) int {
		v, err := h.cache.Get(r.Context(), key)
		if err != nil || v == "" {
			return 0
		}
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	}
	httputil.WriteOK(w, map[string]interface{}{
		"product_views":   getCount("event:product_viewed:" + shopID),
		"tryon_starts":    getCount("event:tryon_started:" + shopID),
		"tryon_completes": getCount("event:tryon_completed:" + shopID),
		"add_to_cart":     getCount("event:add_to_cart:" + shopID),
		"checkouts":       getCount("event:checkout:" + shopID),
		"purchases":       getCount("event:purchase:" + shopID),
	})
}

// GetProducts returns paginated product list for the admin UI.
func (h *UsageHandler) GetProducts(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" { httputil.WriteError(w, http.StatusBadRequest, "shop_id is required"); return }
	if h.db == nil { httputil.WriteOK(w, map[string]interface{}{"products": []interface{}{}, "total": 0}); return }
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 100 { limit = 50 }
	if offset < 0 { offset = 0 }
	var total int64; var products []model.SyncedProduct
	h.db.Model(&model.SyncedProduct{}).Where("shop_id = ?", shopID).Count(&total)
	h.db.Where("shop_id = ?", shopID).Limit(limit).Offset(offset).Find(&products)
	items := make([]map[string]interface{}, len(products))
	for i, p := range products {
		img := firstImageURL(p.Images)
		items[i] = map[string]interface{}{"id": p.PlatformID, "title": p.Title, "status": p.Status, "image": img}
	}
	httputil.WriteOK(w, map[string]interface{}{"products": items, "total": total, "limit": limit, "offset": offset})
}

// DebugToken handles GET /api/shop/internal/debug-token?shop=xxx — returns the real
// Shopify access token for the given shop domain. Only used in local dev with AUTH_BYPASS.
// Protected by the global API_TOKEN, not per-shop token.
func (h *UsageHandler) DebugToken(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("shop")
	if domain == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop param required")
		return
	}
	var shop model.Shop
	if err := h.db.WithContext(r.Context()).Where("shop_domain = ?", domain).First(&shop).Error; err != nil {
		httputil.WriteError(w, http.StatusNotFound, "shop not found")
		return
	}
	httputil.WriteOK(w, map[string]string{"access_token": "", "shop_domain": shop.Domain})
}

// ProxyProducts handles GET /api/shop/proxy-products?shop=xxx — fetches products from
// Shopify Admin REST API on behalf of the shop. Used in dev mode when admin.rest is
// unavailable (AUTH_BYPASS). Protected by the global API_TOKEN.
func (h *UsageHandler) ProxyProducts(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("shop")
	limitStr := r.URL.Query().Get("limit")
	cursor := r.URL.Query().Get("cursor")
	if domain == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop param required")
		return
	}
	limit := 50
	if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 250 {
		limit = l
	}

	var shop model.Shop
	if err := h.db.WithContext(r.Context()).Where("shop_domain = ?", domain).First(&shop).Error; err != nil {
		httputil.WriteError(w, http.StatusNotFound, "shop not found")
		return
	}
	if "" == "" {
		httputil.WriteError(w, http.StatusServiceUnavailable, "shop has no access token")
		return
	}

	// Call Shopify REST API
	apiPath := fmt.Sprintf("https://%s/admin/api/2025-01/products.json?limit=%d", domain, limit)
	if cursor != "" {
		apiPath += "&since_id=" + cursor
	}

	req, err := http.NewRequestWithContext(r.Context(), "GET", apiPath, nil)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("X-Shopify-Access-Token", "")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		httputil.WriteError(w, http.StatusBadGateway, "failed to reach Shopify: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		httputil.WriteError(w, resp.StatusCode, fmt.Sprintf("Shopify API error: %s", string(body)))
		return
	}

	// Stream response directly to client
	w.Header().Set("Content-Type", "application/json")
	io.Copy(w, resp.Body)
}

// firstImageURL extracts the first image src from a Shopify product images JSON array.
func firstImageURL(images datatypes.JSON) string {
	var imgs []struct {
		Src string `json:"src"`
	}
	if err := json.Unmarshal(images, &imgs); err != nil || len(imgs) == 0 {
		return ""
	}
	return imgs[0].Src
}
// GetVertical returns the shop current vertical and all available verticals.
// GET /api/shop/vertical?shop_id=xxx
func (h *UsageHandler) GetVertical(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("shop_id")
	if sid == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id required")
		return
	}
	if h.db == nil {
		httputil.WriteOK(w, map[string]interface{}{"vertical": "", "available": verticals()})
		return
	}
	shopID, err := uuid.Parse(sid)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid shop_id")
		return
	}

	var shop model.Shop
	if err := h.db.Where("id = ?", shopID).First(&shop).Error; err != nil {
		httputil.WriteOK(w, map[string]interface{}{"vertical": "", "available": verticals()})
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"vertical":  shop.Vertical,
		"available": verticals(),
	})
}

// SetVertical updates the shop vertical selection.
// PUT /api/shop/vertical
func (h *UsageHandler) SetVertical(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "DB unavailable")
		return
	}
	var req struct {
		ShopID   string `json:"shop_id"`
		Vertical string `json:"vertical"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	shopID, err := uuid.Parse(req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid shop_id")
		return
	}

	if err := vertical.SetVertical(h.db, shopID, req.Vertical); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to update vertical")
		return
	}

	// Pre-warm features cache so storefront embed gets instant response
	enabled := []string{"seo", "desc", "bgremove", "review", "chat", "insights"}
	if req.Vertical == "fashion" {
		enabled = append(enabled, "tryon")
	}
	if h.cache != nil {
		featuresKey := fmt.Sprintf("aitools:features:shop_%s", req.ShopID)
		encoded, _ := json.Marshal(map[string][]string{"enabled": enabled})
		h.cache.Set(r.Context(), featuresKey, string(encoded), 24*time.Hour)
	}

	httputil.WriteOK(w, map[string]string{"success": "true", "vertical": req.Vertical})
}

func verticals() []map[string]string {
	return []map[string]string{
		{"name": "", "label": "General (all features)"},
		{"name": "fashion", "label": "Fashion & Apparel"},
	}
}
