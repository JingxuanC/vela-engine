package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/JingxuanC/vela-engine/internal/config"
	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"gorm.io/gorm"
)

// ShareHandler handles try-on result sharing endpoints.
type ShareHandler struct {
	db  *gorm.DB
	cfg *config.Config
}

// NewShareHandler creates a new ShareHandler.
func NewShareHandler(db *gorm.DB, cfg *config.Config) *ShareHandler {
	return &ShareHandler{db: db, cfg: cfg}
}

// ShareCreateRequest is the request body for POST /api/share.
type ShareCreateRequest struct {
	TryOnTaskID     string  `json:"tryon_task_id"`
	ProductID       string  `json:"product_id"`
	ProductTitle    string  `json:"product_title"`
	ProductPrice    float64 `json:"product_price"`
	ProductImageURL string  `json:"product_image_url"`
	ResultImageURL  string  `json:"result_image_url"`
	PersonImageURL  string  `json:"person_image_url"`
	ShopID          string  `json:"shop_id"`
}

// ShareCreateResponse is the response for a created share.
type ShareCreateResponse struct {
	Slug    string `json:"slug"`
	URL     string `json:"url"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// ShareViewResponse is the public share page data.
type ShareViewResponse struct {
	ProductID       string  `json:"product_id"`
	ProductTitle    string  `json:"product_title"`
	ProductPrice    float64 `json:"product_price"`
	ProductImageURL string  `json:"product_image_url"`
	ResultImageURL  string  `json:"result_image_url"`
	PersonImageURL  string  `json:"person_image_url"`
	ViewCount       int64   `json:"view_count"`
	CreatedAt       string  `json:"created_at"`
}

// ShareListItem is a single row in the shares list.
type ShareListItem struct {
	Slug           string `json:"slug"`
	ProductTitle   string `json:"product_title"`
	ResultImageURL string `json:"result_image_url"`
	ViewCount      int64  `json:"view_count"`
	CreatedAt      string `json:"created_at"`
}

// Create handles POST /api/share — creates a shareable link.
func (h *ShareHandler) Create(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	var req ShareCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.ResultImageURL == "" {
		httputil.WriteError(w, http.StatusBadRequest, "result_image_url is required")
		return
	}

	shopID, err := database.ResolveShopID(h.db, req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id: "+err.Error())
		return
	}

	slug := generateShareSlug()

	share := model.TryOnShare{
		ShopID:          shopID,
		Slug:            slug,
		TryOnTaskID:     req.TryOnTaskID,
		ProductID:       req.ProductID,
		ProductTitle:    req.ProductTitle,
		ProductPrice:    req.ProductPrice,
		ProductImageURL: req.ProductImageURL,
		ResultImageURL:  req.ResultImageURL,
		PersonImageURL:  req.PersonImageURL,
	}

	if err := h.db.WithContext(r.Context()).Create(&share).Error; err != nil {
		slog.Error("share: failed to create share", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to create share")
		return
	}

	url := "https://ai-tools-test-store.myshopify.com/apps/vela/share/" + slug

	slog.Info("share: created", "slug", slug, "shop_id", shopID)
	httputil.WriteCreated(w, ShareCreateResponse{
		Slug:    slug,
		URL:     url,
		Success: true,
	})
}

// Get handles GET /share/{slug} — public share view.
func (h *ShareHandler) Get(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	slug := r.PathValue("slug")
	if slug == "" {
		httputil.WriteError(w, http.StatusBadRequest, "slug is required")
		return
	}

	var share model.TryOnShare
	if err := h.db.WithContext(r.Context()).Where("slug = ?", slug).First(&share).Error; err != nil {
		httputil.WriteError(w, http.StatusNotFound, "Share not found")
		return
	}

	// Atomic increment view count (fire-and-forget)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic in goroutine", "error", r)
			}
		}()
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.db.WithContext(bgCtx).Model(&share).UpdateColumn("view_count", gorm.Expr("view_count + 1"))
	}()

	httputil.WriteOK(w, ShareViewResponse{
		ProductID:       share.ProductID,
		ProductTitle:    share.ProductTitle,
		ProductPrice:    share.ProductPrice,
		ProductImageURL: share.ProductImageURL,
		ResultImageURL:  share.ResultImageURL,
		PersonImageURL:  share.PersonImageURL,
		ViewCount:       share.ViewCount + 1, // show incremented count
		CreatedAt:       share.CreatedAt.Format("2006-01-02"),
	})
}

// List handles GET /api/shop/shares?shop_id=xxx.
func (h *ShareHandler) List(w http.ResponseWriter, r *http.Request) {
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
		httputil.WriteError(w, http.StatusBadRequest, "Invalid shop_id: "+err.Error())
		return
	}

	var shares []model.TryOnShare
	if err := h.db.WithContext(r.Context()).
		Where("shop_id = ?", shopID).
		Order("created_at DESC").
		Limit(50).
		Find(&shares).Error; err != nil {
		slog.Error("share: failed to list shares", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to list shares")
		return
	}

	items := make([]ShareListItem, 0, len(shares))
	for _, s := range shares {
		items = append(items, ShareListItem{
			Slug:           s.Slug,
			ProductTitle:   s.ProductTitle,
			ResultImageURL: s.ResultImageURL,
			ViewCount:      s.ViewCount,
			CreatedAt:      s.CreatedAt.Format("2006-01-02"),
		})
	}

	httputil.WriteOK(w, map[string]interface{}{
		"shares": items,
	})
}

// generateShareSlug creates a short random slug for share URLs.
func generateShareSlug() string {
	for i := 0; i < 5; i++ {
		b := make([]byte, 6)
		if n, err := rand.Read(b); err != nil || n < 6 {
			continue
		}
		return base64.RawURLEncoding.EncodeToString(b)[:8]
	}
	return fmt.Sprintf("s%d", time.Now().UnixNano()%100000000)
}
