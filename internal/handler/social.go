package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
	socialsvc "github.com/JingxuanC/vela-engine/internal/service/social"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// SocialHandler handles unified social platform OAuth, publishing, and post management.
// Platforms (pinterest, instagram, youtube) are registered via the PlatformPublisher interface.
type SocialHandler struct {
	db         *gorm.DB
	cache      *service.CacheService
	crypto     *service.CryptoService
	publishers map[string]socialsvc.PlatformPublisher
}

// NewSocialHandler creates a SocialHandler with registered platform publishers.
func NewSocialHandler(db *gorm.DB, cache *service.CacheService, crypto *service.CryptoService, publishers ...socialsvc.PlatformPublisher) *SocialHandler {
	m := make(map[string]socialsvc.PlatformPublisher, len(publishers))
	for _, p := range publishers {
		m[p.Platform()] = p
	}
	return &SocialHandler{db: db, cache: cache, crypto: crypto, publishers: m}
}

// ── helpers ────────────────────────────────────────────────────────────────

func (h *SocialHandler) requireDB(w http.ResponseWriter) bool {
	if h.db == nil {
		httputil.WriteError(w, 503, "DB unavailable")
		return false
	}
	return true
}

func (h *SocialHandler) requireCrypto(w http.ResponseWriter) bool {
	if h.crypto == nil {
		httputil.WriteError(w, 500, "crypto service not configured")
		return false
	}
	return true
}

func (h *SocialHandler) getPlatform(w http.ResponseWriter, r *http.Request) (string, socialsvc.PlatformPublisher, bool) {
	name := chi.URLParam(r, "platform")
	if name == "" {
		httputil.WriteError(w, 400, "platform path parameter required")
		return "", nil, false
	}
	pub, ok := h.publishers[name]
	if !ok {
		httputil.WriteError(w, 400, fmt.Sprintf("unsupported platform: %s", name))
		return "", nil, false
	}
	return name, pub, true
}

func (h *SocialHandler) resolveShopID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	// Try query param
	sid := r.URL.Query().Get("shop_id")
	// Fallback to JSON body
	if sid == "" && r.Body != nil {
		var body struct{ ShopID string `json:"shop_id"` }
		if json.NewDecoder(r.Body).Decode(&body) == nil && body.ShopID != "" {
			sid = body.ShopID
		}
	}
	if sid == "" {
		httputil.WriteError(w, 400, "shop_id required")
		return uuid.Nil, false
	}
	shopID, err := database.ResolveShopID(h.db, sid)
	if err != nil {
		httputil.WriteError(w, 400, "invalid shop_id: "+err.Error())
		return uuid.Nil, false
	}
	return shopID, true
}

func oauthKey(platform, state string) string {
	return "social:oauth:" + platform + ":" + state
}

// ── endpoints ──────────────────────────────────────────────────────────────

// Platforms returns all registered platforms and their connection status.
// GET /api/social/platforms?shop_id=xxx
func (h *SocialHandler) Platforms(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	shopID, ok := h.resolveShopID(w, r)
	if !ok {
		return
	}
	// Sorted platform names for deterministic output
	names := make([]string, 0, len(h.publishers))
	for n := range h.publishers {
		names = append(names, n)
	}
	sort.Strings(names)

	platforms := make([]map[string]interface{}, 0, len(names))
	for _, name := range names {
		var conn model.SocialConnection
		active := h.db.Where("shop_id = ? AND platform = ? AND status = ?",
			shopID, name, "active").First(&conn).Error == nil
		info := map[string]interface{}{
			"platform":  name,
			"connected": active,
		}
		if active {
			info["connected_at"] = conn.CreatedAt
			info["expires_at"] = conn.ExpiresAt
		}
		platforms = append(platforms, info)
	}
	httputil.WriteOK(w, map[string]interface{}{"success": true, "platforms": platforms})
}

// AuthURL starts the OAuth flow for a platform.
// GET /api/social/{platform}/auth-url?shop_id=xxx
func (h *SocialHandler) AuthURL(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	name, pub, ok := h.getPlatform(w, r)
	if !ok {
		return
	}
	sid := r.URL.Query().Get("shop_id")
	if sid == "" {
		httputil.WriteError(w, 400, "shop_id required")
		return
	}
	state := uuid.New().String()
	if h.cache != nil {
		h.cache.Set(r.Context(), oauthKey(name, state), sid, 5*time.Minute)
	}
	httputil.WriteOK(w, map[string]interface{}{
		"success":  true,
		"auth_url": pub.AuthURL(state),
		"state":    state,
	})
}

// Callback handles the OAuth redirect from the platform.
// GET /api/social/{platform}/callback?code=xxx&state=xxx
func (h *SocialHandler) Callback(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) || !h.requireCrypto(w) {
		return
	}
	name, pub, ok := h.getPlatform(w, r)
	if !ok {
		return
	}
	code := r.URL.Query().Get("code")
	stateParam := r.URL.Query().Get("state")
	if code == "" || stateParam == "" {
		httputil.WriteError(w, 400, "code and state required")
		return
	}
	sid := ""
	if h.cache != nil {
		sid, _ = h.cache.Get(r.Context(), oauthKey(name, stateParam))
		h.cache.Delete(r.Context(), oauthKey(name, stateParam))
	}
	if sid == "" {
		httputil.WriteError(w, 400, "invalid or expired state")
		return
	}

	shopID, err := database.ResolveShopID(h.db, sid)
	if err != nil {
		httputil.WriteError(w, 400, "invalid shop_id: "+err.Error())
		return
	}

	tok, err := pub.ExchangeCode(r.Context(), code)
	if err != nil {
		slog.Error("social: exchange code failed", "platform", name, "shop_id", shopID, "error", err)
		httputil.WriteError(w, 502, "OAuth token exchange failed")
		return
	}

	encTok, encErr := h.crypto.Encrypt([]byte(tok.AccessToken))
	if encErr != nil {
		slog.Error("social: encrypt token failed", "platform", name, "error", encErr)
		httputil.WriteError(w, 500, "token encryption failed")
		return
	}
	encRef := ""
	if tok.RefreshToken != "" {
		encRef, encErr = h.crypto.Encrypt([]byte(tok.RefreshToken))
		if encErr != nil {
			slog.Error("social: encrypt refresh token failed", "platform", name, "error", encErr)
			httputil.WriteError(w, 500, "token encryption failed")
			return
		}
	}

	exp := time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	if tok.ExpiresIn <= 0 {
		exp = time.Now().Add(24 * time.Hour)
	}

	tt := tok.TokenType
	if tt == "" {
		tt = "Bearer"
	}
	conn := model.SocialConnection{
		ShopID: shopID, Platform: name,
		EncryptedToken: encTok, RefreshToken: encRef,
		TokenType: tt, ExpiresAt: &exp, Status: "active",
	}
	h.db.Where("shop_id = ? AND platform = ?", shopID, name).
		Assign(conn).FirstOrCreate(&conn)

	slog.Info("social: connected", "platform", name, "shop_id", shopID)
	httputil.WriteOK(w, map[string]interface{}{
		"success":   true,
		"connected": true,
		"platform":  name,
	})
}

// Status checks connection status for a platform.
// GET /api/social/{platform}/status?shop_id=xxx
func (h *SocialHandler) Status(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	name, _, ok := h.getPlatform(w, r)
	if !ok {
		return
	}
	sid := r.URL.Query().Get("shop_id")
	shopID, err := database.ResolveShopID(h.db, sid)
	if err != nil {
		httputil.WriteOK(w, map[string]interface{}{"success": true, "connected": false})
		return
	}
	var conn model.SocialConnection
	if h.db.Where("shop_id = ? AND platform = ? AND status = ?",
		shopID, name, "active").First(&conn).Error != nil {
		httputil.WriteOK(w, map[string]interface{}{"success": true, "connected": false})
		return
	}
	httputil.WriteOK(w, map[string]interface{}{
		"success":      true,
		"connected":    true,
		"platform":     name,
		"connected_at": conn.CreatedAt,
		"expires_at":   conn.ExpiresAt,
	})
}

// Disconnect removes a platform connection and its associated posts.
// POST /api/social/{platform}/disconnect
func (h *SocialHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	name, _, ok := h.getPlatform(w, r)
	if !ok {
		return
	}
	shopID, ok := h.resolveShopID(w, r)
	if !ok {
		return
	}
	// Delete associated posts to prevent orphans
	h.db.Where("shop_id = ? AND platform = ?", shopID, name).Delete(&model.SocialPost{})
	// Delete the connection
	h.db.Where("shop_id = ? AND platform = ?", shopID, name).Delete(&model.SocialConnection{})

	slog.Info("social: disconnected", "platform", name, "shop_id", shopID)
	httputil.WriteOK(w, map[string]string{
		"success": "true",
		"message": "Disconnected from " + name,
	})
}

// Publish publishes content to a connected platform.
// POST /api/social/{platform}/publish
func (h *SocialHandler) Publish(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) || !h.requireCrypto(w) {
		return
	}
	name, pub, ok := h.getPlatform(w, r)
	if !ok {
		return
	}

	var req struct {
		ShopID         string `json:"shop_id"`
		Title          string `json:"title"`
		Description    string `json:"description"`
		ImageURL       string `json:"image_url"`
		LinkURL        string `json:"link_url"`
		BoardID        string `json:"board_id"`
		ContentPieceID string `json:"content_piece_id"` // 🆕 Phase 2: optional content piece for UTM tracking
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, 400, "invalid JSON body")
		return
	}
	if req.ShopID == "" {
		httputil.WriteError(w, 400, "shop_id required")
		return
	}
	shopID, err := database.ResolveShopID(h.db, req.ShopID)
	if err != nil {
		httputil.WriteError(w, 400, "invalid shop_id: "+err.Error())
		return
	}

	// 🆕 Phase 2: Resolve content piece and UTM URL
	var contentPieceID *uuid.UUID
	var utmURL string
	if req.ContentPieceID != "" {
		if pid, err := uuid.Parse(req.ContentPieceID); err == nil {
			var piece model.ContentPiece
			if h.db.Where("id = ? AND shop_id = ?", pid, shopID).First(&piece).Error == nil {
				contentPieceID = &pid
				utmURL = piece.UTMURL
				// If no explicit link_url provided, use the UTM URL from content piece
				if req.LinkURL == "" {
					req.LinkURL = utmURL
				}
			}
		}
	}

	var conn model.SocialConnection
	if err := h.db.Where("shop_id = ? AND platform = ? AND status = ?",
		shopID, name, "active").First(&conn).Error; err != nil {
		httputil.WriteError(w, 400, fmt.Sprintf("no active %s connection", name))
		return
	}

	tok, decErr := h.crypto.Decrypt(conn.EncryptedToken)
	if decErr != nil {
		slog.Error("social: decrypt token failed", "platform", name, "error", decErr)
		httputil.WriteError(w, 500, "token decrypt failed")
		return
	}

	result, err := pub.Publish(r.Context(), string(tok), &socialsvc.PublishRequest{
		Title:       req.Title,
		Description: req.Description,
		ImageURL:    req.ImageURL,
		LinkURL:     req.LinkURL,
		BoardID:     req.BoardID,
	})
	if err != nil {
		slog.Error("social: publish failed", "platform", name, "shop_id", shopID, "error", err)
		httputil.WriteError(w, 502, "publish failed; check platform connection status")
		return
	}

	now := time.Now()
	boardID := result.BoardID
	if boardID == "" {
		boardID = req.BoardID // fallback to request value if platform doesn't return one
	}
	post := model.SocialPost{
		ShopID: shopID, ConnectionID: conn.ID, Platform: name,
		PinID: result.PlatformPostID, BoardID: boardID,
		MediaURL: req.ImageURL, Title: req.Title,
		Description: req.Description, LinkURL: req.LinkURL,
		Status: "published", PublishedAt: &now,
	}
	// 🆕 Phase 2: Associate with content piece
	if contentPieceID != nil {
		post.ContentID = contentPieceID.String()
		post.ContentPieceID = contentPieceID
		post.UTMURL = utmURL
	}
	if err := h.db.Create(&post).Error; err != nil {
		slog.Error("social: save post failed", "platform", name, "error", err)
	}

	// 🆕 Phase 2: Update ContentPiece status to published
	if contentPieceID != nil {
		h.db.Model(&model.ContentPiece{}).
			Where("id = ? AND shop_id = ?", *contentPieceID, shopID).
			Update("status", "published")
	}

	resp := map[string]interface{}{
		"success":       true,
		"platform":      name,
		"platform_id":   result.PlatformPostID,
		"permalink_url": result.PermalinkURL,
		"post_id":       post.ID,
	}
	// 🆕 Phase 2: Include content piece and UTM info in response
	if contentPieceID != nil {
		resp["content_piece_id"] = contentPieceID.String()
		resp["utm_url"] = utmURL
	}
	httputil.WriteCreated(w, resp)
}

// Posts lists social posts, optionally filtered by platform.
// GET /api/social/posts?shop_id=xxx&platform=pinterest&limit=20&offset=0
func (h *SocialHandler) Posts(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	shopID, ok := h.resolveShopID(w, r)
	if !ok {
		return
	}
	platform := r.URL.Query().Get("platform")

	lim, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if lim < 1 || lim > 100 {
		lim = 20
	}
	if off < 0 {
		off = 0
	}

	query := h.db.Model(&model.SocialPost{}).Where("shop_id = ?", shopID)
	if platform != "" {
		query = query.Where("platform = ?", platform)
	}

	var total int64
	query.Count(&total)

	var posts []model.SocialPost
	query.Order("created_at DESC").Limit(lim).Offset(off).Find(&posts)

	items := make([]map[string]interface{}, len(posts))
	for i, p := range posts {
		items[i] = map[string]interface{}{
			"id":         p.ID,
			"platform":   p.Platform,
			"pin_id":     p.PinID,
			"title":      p.Title,
			"media_url":  p.MediaURL,
			"status":     p.Status,
			"created_at": p.CreatedAt,
		}
	}
	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"posts":   items,
		"total":   total,
		"limit":   lim,
		"offset":  off,
	})
}

// DeletePost deletes a social post after verifying shop ownership.
// DELETE /api/social/posts/{postID}?shop_id=xxx
func (h *SocialHandler) DeletePost(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	sid := r.URL.Query().Get("shop_id")
	if sid == "" {
		httputil.WriteError(w, 400, "shop_id required")
		return
	}
	shopID, err := database.ResolveShopID(h.db, sid)
	if err != nil {
		httputil.WriteError(w, 400, "invalid shop_id: "+err.Error())
		return
	}
	pid := chi.URLParam(r, "postID")
	if pid == "" {
		httputil.WriteError(w, 400, "postID path parameter required")
		return
	}
	result := h.db.Where("shop_id = ? AND id = ?", shopID, pid).Delete(&model.SocialPost{})
	if result.RowsAffected == 0 {
		httputil.WriteError(w, 404, "post not found")
		return
	}
	httputil.WriteOK(w, map[string]string{"success": "true"})
}

// GetPublisher returns the PlatformPublisher for the given platform name,
// or nil if no publisher is registered for that platform.
func (h *SocialHandler) GetPublisher(platform string) socialsvc.PlatformPublisher {
	return h.publishers[platform]
}
