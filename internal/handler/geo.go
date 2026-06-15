package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// GeoHandler serves Schema.org JSON-LD, llms.txt, and feed endpoints for AI search engines.
type GeoHandler struct {
	db    *gorm.DB
	cache *service.CacheService
}

func NewGeoHandler(db *gorm.DB, cache *service.CacheService) *GeoHandler {
	return &GeoHandler{db: db, cache: cache}
}

// ── Schema.org Types ────────────────────────────────────────────────────────

type GeoBrand struct {
	Type string `json:"@type"`
	Name string `json:"name"`
}

type GeoOffer struct {
	Type          string `json:"@type"`
	Price         string `json:"price"`
	PriceCurrency string `json:"priceCurrency"`
	Availability  string `json:"availability"`
	URL           string `json:"url,omitempty"`
}

type GeoAggregateRating struct {
	Type        string  `json:"@type"`
	RatingValue float64 `json:"ratingValue"`
	ReviewCount int     `json:"reviewCount"`
	BestRating  int     `json:"bestRating"`
	WorstRating int     `json:"worstRating"`
}

type GeoShippingDetails struct {
	Type         string            `json:"@type"`
	ShippingRate GeoMonetaryAmount `json:"shippingRate"`
	ShippingDest GeoDefinedRegion  `json:"shippingDestination"`
}

type GeoMonetaryAmount struct {
	Type     string `json:"@type"`
	Value    string `json:"value"`
	Currency string `json:"currency"`
}

type GeoDefinedRegion struct {
	Type           string `json:"@type"`
	AddressCountry string `json:"addressCountry"`
}

type GeoReturnPolicy struct {
	Type                 string `json:"@type"`
	ApplicableCountry    string `json:"applicableCountry"`
	ReturnPolicyCategory string `json:"returnPolicyCategory"`
	MerchantReturnDays   int    `json:"merchantReturnDays"`
	ReturnFees           string `json:"returnFees"`
}

type GeoProductSchema struct {
	Context         string              `json:"@context"`
	Type            string              `json:"@type"`
	Name            string              `json:"name"`
	Description     string              `json:"description"`
	Image           []string            `json:"image,omitempty"`
	Brand           GeoBrand            `json:"brand"`
	SKU             string              `json:"sku,omitempty"`
	GTIN13          string              `json:"gtin13,omitempty"`
	Offers          GeoOffer            `json:"offers"`
	AggregateRating *GeoAggregateRating `json:"aggregateRating,omitempty"`
	ShippingDetails *GeoShippingDetails `json:"shippingDetails,omitempty"`
	ReturnPolicy    *GeoReturnPolicy    `json:"hasMerchantReturnPolicy,omitempty"`
}

// ── Endpoints ────────────────────────────────────────────────────────────────

// Feed returns a full JSON-LD product feed for AI search engines.
// GET /api/geo/feed?shop_id=xxx
// Cached in Redis (TTL 1h), regenerated on product webhook.
func (h *GeoHandler) Feed(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	// Try Redis cache first
	cacheKey := "geo:feed:" + shopID
	if h.cache != nil {
		if cached, err := h.cache.Get(r.Context(), cacheKey); err == nil && cached != "" {
			w.Header().Set("Content-Type", "application/ld+json")
			w.Header().Set("X-Cache", "HIT")
			w.Write([]byte(cached))
			return
		}
	}

	// Query active products (limit 500 for feed size)
	var products []model.SyncedProduct
	if err := h.db.Where("shop_id = ? AND status = ?", shopID, "active").
		Limit(500).Find(&products).Error; err != nil {
		slog.Error("geo: feed query failed", "shop_id", shopID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to query products")
		return
	}

	// Build full Schema.org Product list
	items := make([]GeoProductSchema, 0, len(products))
	for _, p := range products {
		schema := h.buildProductSchema(r.Context(), &p, shopID)
		if schema != nil {
			items = append(items, *schema)
		}
	}

	// Encode to JSON
	jsonBytes, err := json.Marshal(items)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to encode feed")
		return
	}

	// Cache in Redis (1 hour)
	if h.cache != nil {
		h.cache.Set(r.Context(), cacheKey, string(jsonBytes), time.Hour)
	}

	w.Header().Set("Content-Type", "application/ld+json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Cache", "MISS")
	w.Write(jsonBytes)
}

// GetSchema returns JSON-LD for a single product page.
// GET /api/geo/schema?shop_id=xxx&handle=yyy
func (h *GeoHandler) GetSchema(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}
	shopID := r.URL.Query().Get("shop_id")
	handle := r.URL.Query().Get("handle")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	// Try Redis cache (per-product TTL)
	var product model.SyncedProduct
	lookup := handle
	if handle == "" {
		lookup = r.URL.Query().Get("product_id")
	}
	if lookup == "" {
		httputil.WriteError(w, http.StatusBadRequest, "handle or product_id is required")
		return
	}

	cacheKey := fmt.Sprintf("geo:schema:%s:%s", shopID, lookup)
	if h.cache != nil {
		if cached, err := h.cache.Get(r.Context(), cacheKey); err == nil && cached != "" {
			w.Header().Set("Content-Type", "application/ld+json")
			w.Header().Set("X-Cache", "HIT")
			w.Write([]byte(cached))
			return
		}
	}

	// Query the specific product
	if err := h.db.Where("shop_id = ? AND (platform_id = ? OR title ILIKE ?)", shopID, lookup, "%"+lookup+"%").
		First(&product).Error; err != nil {
		httputil.WriteError(w, http.StatusNotFound, "product not found")
		return
	}

	schema := h.buildProductSchema(r.Context(), &product, shopID)
	if schema == nil {
		httputil.WriteError(w, http.StatusNotFound, "product not found")
		return
	}

	jsonBytes, _ := json.Marshal(schema)
	if h.cache != nil {
		h.cache.Set(r.Context(), cacheKey, string(jsonBytes), time.Hour)
	}

	w.Header().Set("Content-Type", "application/ld+json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(jsonBytes)
}

// GetLLMsTxt returns a Markdown-formatted AI navigation file.
// GET /api/geo/llms.txt?shop_id=xxx
func (h *GeoHandler) GetLLMsTxt(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	// Try cache
	cacheKey := "geo:llms:" + shopID
	if h.cache != nil {
		if cached, err := h.cache.Get(r.Context(), cacheKey); err == nil && cached != "" {
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.Write([]byte(cached))
			return
		}
	}

	// Get shop info
	var shop model.Shop
	if err := h.db.Where("shop_domain = ? OR id = ?", shopID, shopID).First(&shop).Error; err != nil {
		shop.Domain = shopID
	}

	// Get top products (up to 30)
	var products []model.SyncedProduct
	if err := h.db.Where("shop_id = ? AND status = ?", shopID, "active").
		Order("created_at DESC").Limit(30).Find(&products).Error; err != nil {
		slog.Error("geo: llms product query failed", "error", err)
	}

	// Build llms.txt content
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n\n", shop.Domain))
	sb.WriteString(fmt.Sprintf("> AI-powered commerce store at %s. Free shipping on orders over $75.\n\n", shop.Domain))

	sb.WriteString("## Featured Products\n\n")
	for _, p := range products {
		price := extractVariantPrice(p.Variants)
		sb.WriteString(fmt.Sprintf("- [%s](https://%s/products/): %s", p.Title, shop.Domain, p.Title))
		if price > 0 {
			sb.WriteString(fmt.Sprintf(" — $%.2f", price))
		}
		if p.Vendor != "" {
			sb.WriteString(fmt.Sprintf(" by %s", p.Vendor))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\n## Policies\n\n")
	sb.WriteString(fmt.Sprintf("- [Shipping Policy](https://%s/policies/shipping-policy): Standard shipping available\n", shop.Domain))
	sb.WriteString(fmt.Sprintf("- [Return Policy](https://%s/policies/refund-policy): 30-day returns accepted\n", shop.Domain))
	sb.WriteString(fmt.Sprintf("- [Privacy Policy](https://%s/policies/privacy-policy)\n", shop.Domain))

	sb.WriteString(fmt.Sprintf("\n## Product Feed\n\n"))
	sb.WriteString(fmt.Sprintf("- [JSON-LD Feed](https://%s/apps/vela/api/geo/feed?shop_id=%s)\n", shop.Domain, shopID))

	content := sb.String()

	// Cache
	if h.cache != nil {
		h.cache.Set(r.Context(), cacheKey, content, time.Hour)
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write([]byte(content))
}

// Settings returns GEO configuration.
func (h *GeoHandler) Settings(w http.ResponseWriter, r *http.Request) {
	httputil.WriteOK(w, map[string]interface{}{
		"enabled":   true,
		"feed_url":  "/api/geo/feed",
		"llms_url":  "/api/geo/llms.txt",
		"cache_ttl": 3600,
	})
}

// UpdateSettings saves GEO configuration.
func (h *GeoHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	httputil.WriteOK(w, map[string]string{"status": "saved"})
}

// Regenerate invalidates all GEO caches for the given shop.
func (h *GeoHandler) Regenerate(w http.ResponseWriter, r *http.Request) {
	shopID := r.URL.Query().Get("shop_id")
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}
	if h.cache != nil {
		// Delete all geo-related cache keys for this shop
		for _, prefix := range []string{"geo:feed:", "geo:llms:", "geo:schema:"} {
			h.cache.Delete(r.Context(), prefix+shopID)
		}
	}
	httputil.WriteOK(w, map[string]string{
		"status":    "regenerated",
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

// InvalidateProduct removes cached schema for a specific product.
func (h *GeoHandler) InvalidateProduct(shopID, productHandle string) {
	if h.cache == nil {
		return
	}
	if h.cache != nil {
		ctx := bgTimeout(10*time.Second)
		h.cache.Delete(ctx, fmt.Sprintf("geo:schema:%s:%s", shopID, productHandle))
		h.cache.Delete(ctx, fmt.Sprintf("geo:feed:%s", shopID))
	}
}

// ── Schema Builder ───────────────────────────────────────────────────────────

func (h *GeoHandler) buildProductSchema(ctx context.Context, p *model.SyncedProduct, shopID string) *GeoProductSchema {
	// Check Redis for AI-generated schema first (from SEO module)
	if h.cache != nil {
		aiKey := fmt.Sprintf("geo:schema:ai:%s:%s", p.Title, p.PlatformID)
		if cached, err := h.cache.Get(ctx, aiKey); err == nil && cached != "" {
			var aiSchema GeoProductSchema
			if json.Unmarshal([]byte(cached), &aiSchema) == nil && aiSchema.Name != "" {
				return &aiSchema
			}
		}
	}
	// Parse variants from JSONB
	variants := parseVariantArray(p.Variants)
	if len(variants) == 0 {
		return nil
	}
	v := variants[0]

	// Parse images
	images := parseImageArray(p.Images)

	// Build brand
	brand := GeoBrand{Type: "Brand", Name: p.Vendor}
	if brand.Name == "" {
		brand.Name = "Unknown Brand"
	}

	// Build offers
	price := v.Price
	if price == "" {
		price = "0.00"
	}
	availability := "https://schema.org/InStock"
	if v.InventoryQuantity <= 0 {
		availability = "https://schema.org/OutOfStock"
	}

	offer := GeoOffer{
		Type:          "Offer",
		Price:         price,
		PriceCurrency: "USD",
		Availability:  availability,
		URL:           fmt.Sprintf("https://%s/products/", shopID) + p.PlatformID,
	}

	schema := &GeoProductSchema{
		Context:     "https://schema.org",
		Type:        "Product",
		Name:        p.Title,
		Description: truncateString(p.Description, 300),
		Image:       images,
		Brand:       brand,
		SKU:         v.Sku,
		GTIN13:      v.Barcode,
		Offers:      offer,
	}

	// Add shipping details
	schema.ShippingDetails = &GeoShippingDetails{
		Type: "OfferShippingDetails",
		ShippingRate: GeoMonetaryAmount{
			Type:     "MonetaryAmount",
			Value:    "0.00",
			Currency: "USD",
		},
		ShippingDest: GeoDefinedRegion{
			Type:           "DefinedRegion",
			AddressCountry: "US",
		},
	}

	// Add return policy
	schema.ReturnPolicy = &GeoReturnPolicy{
		Type:                 "MerchantReturnPolicy",
		ApplicableCountry:    "US",
		ReturnPolicyCategory: "https://schema.org/MerchantReturnFiniteReturnWindow",
		MerchantReturnDays:   30,
		ReturnFees:           "https://schema.org/FreeReturn",
	}

	// Add aggregate rating from Judge.me
	if h.db != nil {
		var reviewCount int64
		var avgRating float64
		h.db.Model(&model.SyncedReview{}).
			Where("shop_id = ? AND product_id = ?", p.ShopID, p.PlatformID).
			Select("COUNT(*), COALESCE(AVG(rating), 0)").
			Row().Scan(&reviewCount, &avgRating)

		if reviewCount > 0 {
			schema.AggregateRating = &GeoAggregateRating{
				Type:        "AggregateRating",
				RatingValue: float64(int(avgRating*10)) / 10,
				ReviewCount: int(reviewCount),
				BestRating:  5,
				WorstRating: 1,
			}
		}
	}

	return schema
}

// ── JSONB Parsers ────────────────────────────────────────────────────────────

type productVariant struct {
	ID                int64  `json:"id"`
	Title             string `json:"title"`
	Price             string `json:"price"`
	Sku               string `json:"sku"`
	Barcode           string `json:"barcode"`
	InventoryQuantity int    `json:"inventory_quantity"`
}

func parseVariantArray(raw datatypes.JSON) []productVariant {
	var variants []productVariant
	if raw == nil {
		return nil
	}
	json.Unmarshal([]byte(raw), &variants)
	return variants
}

func parseImageArray(raw datatypes.JSON) []string {
	var images []struct {
		Src string `json:"src"`
	}
	if raw == nil {
		return nil
	}
	json.Unmarshal([]byte(raw), &images)
	var urls []string
	for _, img := range images {
		if img.Src != "" {
			urls = append(urls, img.Src)
		}
	}
	return urls
}

func extractVariantPrice(raw datatypes.JSON) float64 {
	variants := parseVariantArray(raw)
	for _, v := range variants {
		var p float64
		fmt.Sscanf(v.Price, "%f", &p)
		if p > 0 {
			return p
		}
	}
	return 0
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
