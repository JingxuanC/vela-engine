package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
	socialsvc "github.com/JingxuanC/vela-engine/internal/service/social"
)

// withChiParam returns a request with chi URL params set (for {platform}, {postID} etc.)
func withChiParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
	return r.WithContext(ctx)
}

// ── mocks ────────────────────────────────────────────────────────────────────

type mockPublisher struct {
	platform    string
	authURL     string
	exchangeErr error
	publishErr  error
	publishID   string
}

func (m *mockPublisher) Platform() string           { return m.platform }
func (m *mockPublisher) AuthURL(state string) string { return m.authURL + "?state=" + state }
func (m *mockPublisher) ExchangeCode(_ context.Context, _ string) (*socialsvc.TokenResponse, error) {
	if m.exchangeErr != nil {
		return nil, m.exchangeErr
	}
	return &socialsvc.TokenResponse{
		AccessToken: "mock-access-token", RefreshToken: "mock-refresh-token",
		TokenType: "Bearer", ExpiresIn: 3600,
	}, nil
}
func (m *mockPublisher) Publish(_ context.Context, _ string, _ *socialsvc.PublishRequest) (*socialsvc.PublishResult, error) {
	if m.publishErr != nil {
		return nil, m.publishErr
	}
	return &socialsvc.PublishResult{
		PlatformPostID: m.publishID,
		PermalinkURL:   "https://mock.example/" + m.publishID,
	}, nil
}
func (m *mockPublisher) GetAnalytics(_ context.Context, _ string, _ string, _ time.Time) (*socialsvc.PlatformMetrics, error) {
	return &socialsvc.PlatformMetrics{}, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func setupSocialTest(t *testing.T, mockPlatform string) (*SocialHandler, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS social_connections (
			id TEXT PRIMARY KEY, shop_id TEXT NOT NULL,
			platform TEXT NOT NULL, encrypted_token TEXT,
			refresh_token TEXT, token_type TEXT DEFAULT 'Bearer',
			expires_at DATETIME, platform_user_id TEXT,
			platform_user_name TEXT, board_count INTEGER DEFAULT 0,
			status TEXT DEFAULT 'active',
			created_at DATETIME, updated_at DATETIME
		)
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS social_posts (
			id TEXT PRIMARY KEY, shop_id TEXT NOT NULL,
			connection_id TEXT NOT NULL, platform TEXT NOT NULL,
			content_id TEXT, content_piece_id TEXT, pin_id TEXT, board_id TEXT,
			media_url TEXT, title TEXT, description TEXT,
			link_url TEXT, utm_url TEXT, status TEXT DEFAULT 'draft',
			error_message TEXT,
			scheduled_at DATETIME, published_at DATETIME,
			created_at DATETIME, updated_at DATETIME
		)
	`).Error)

	crypto, err := service.NewCryptoService("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	require.NoError(t, err, "CryptoService init must succeed")
	pub := &mockPublisher{platform: mockPlatform, authURL: "https://mock.example/oauth", publishID: "post-123"}
	h := NewSocialHandler(db, nil, crypto, pub)
	return h, db
}

func seedConnection(t *testing.T, db *gorm.DB, shopID, platform, encToken string) string {
	t.Helper()
	id := uuid.New().String()
	require.NoError(t, db.Exec(
		`INSERT INTO social_connections (id, shop_id, platform, encrypted_token, token_type, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'Bearer', 'active', datetime('now'), datetime('now'))`,
		id, shopID, platform, encToken).Error)
	return id
}

func seedPost(t *testing.T, db *gorm.DB, shopID, connID, platform, title string) string {
	t.Helper()
	id := uuid.New().String()
	require.NoError(t, db.Exec(
		`INSERT INTO social_posts (id, shop_id, connection_id, platform, title, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'published', datetime('now'), datetime('now'))`,
		id, shopID, connID, platform, title).Error)
	return id
}

func parseJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&m))
	return m
}

// req creates a request and sets chi platform param if non-empty.
func req(method, path, platform, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if platform != "" {
		r = withChiParam(r, "platform", platform)
	}
	return r
}

// ── Platforms ────────────────────────────────────────────────────────────────

func TestPlatforms_Success(t *testing.T) {
	h, db := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()
	seedConnection(t, db, shopID, "pinterest", "enc-token-123")

	r := httptest.NewRequest("GET", "/api/social/platforms?shop_id="+shopID, nil)
	rec := httptest.NewRecorder()
	h.Platforms(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)
	m := parseJSON(t, rec)
	assert.True(t, m["success"].(bool))
	platforms := m["platforms"].([]interface{})
	assert.Equal(t, 1, len(platforms))
	p := platforms[0].(map[string]interface{})
	assert.Equal(t, "pinterest", p["platform"])
	assert.True(t, p["connected"].(bool))
}

func TestPlatforms_NotConnected(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()

	r := httptest.NewRequest("GET", "/api/social/platforms?shop_id="+shopID, nil)
	rec := httptest.NewRecorder()
	h.Platforms(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)
	m := parseJSON(t, rec)
	platforms := m["platforms"].([]interface{})
	p := platforms[0].(map[string]interface{})
	assert.False(t, p["connected"].(bool))
}

func TestPlatforms_MissingShopID(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	r := httptest.NewRequest("GET", "/api/social/platforms", nil)
	rec := httptest.NewRecorder()
	h.Platforms(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPlatforms_NoDB(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	h.db = nil
	r := httptest.NewRequest("GET", "/api/social/platforms?shop_id=test", nil)
	rec := httptest.NewRecorder()
	h.Platforms(rec, r)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// ── AuthURL ──────────────────────────────────────────────────────────────────

func TestAuthURL_Success(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()

	r := req("GET", "/api/social/pinterest/auth-url?shop_id="+shopID, "pinterest", "")
	rec := httptest.NewRecorder()
	h.AuthURL(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)
	m := parseJSON(t, rec)
	assert.True(t, m["success"].(bool))
	assert.Contains(t, m["auth_url"].(string), "mock.example/oauth?state=")
	assert.NotEmpty(t, m["state"])
}

func TestAuthURL_MissingShopID(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	r := req("GET", "/api/social/pinterest/auth-url", "pinterest", "")
	rec := httptest.NewRecorder()
	h.AuthURL(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAuthURL_UnsupportedPlatform(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	r := req("GET", "/api/social/tiktok/auth-url?shop_id=test", "tiktok", "")
	rec := httptest.NewRecorder()
	h.AuthURL(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ── Callback ─────────────────────────────────────────────────────────────────

func TestCallback_MissingCode(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	r := req("GET", "/api/social/pinterest/callback?state=abc", "pinterest", "")
	rec := httptest.NewRecorder()
	h.Callback(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCallback_MissingState(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	r := req("GET", "/api/social/pinterest/callback?code=abc", "pinterest", "")
	rec := httptest.NewRecorder()
	h.Callback(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCallback_NoCrypto(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	h.crypto = nil
	r := req("GET", "/api/social/pinterest/callback?code=abc&state=def", "pinterest", "")
	rec := httptest.NewRecorder()
	h.Callback(rec, r)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// ── Status ───────────────────────────────────────────────────────────────────

func TestStatus_Connected(t *testing.T) {
	h, db := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()
	seedConnection(t, db, shopID, "pinterest", "enc-token")

	r := req("GET", "/api/social/pinterest/status?shop_id="+shopID, "pinterest", "")
	rec := httptest.NewRecorder()
	h.Status(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)
	m := parseJSON(t, rec)
	assert.True(t, m["connected"].(bool))
}

func TestStatus_NotConnected(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()

	r := req("GET", "/api/social/pinterest/status?shop_id="+shopID, "pinterest", "")
	rec := httptest.NewRecorder()
	h.Status(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)
	m := parseJSON(t, rec)
	assert.False(t, m["connected"].(bool))
}

func TestStatus_UnsupportedPlatform(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	r := req("GET", "/api/social/tiktok/status?shop_id=test", "tiktok", "")
	rec := httptest.NewRecorder()
	h.Status(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ── Disconnect ───────────────────────────────────────────────────────────────

func TestDisconnect_Success(t *testing.T) {
	h, db := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()
	connID := seedConnection(t, db, shopID, "pinterest", "enc-token")
	seedPost(t, db, shopID, connID, "pinterest", "Test Post")

	body := `{"shop_id":"` + shopID + `"}`
	r := req("POST", "/api/social/pinterest/disconnect", "pinterest", body)
	rec := httptest.NewRecorder()
	h.Disconnect(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)
	m := parseJSON(t, rec)
	assert.Equal(t, "true", m["success"])

	var connCount int64
	db.Model(&model.SocialConnection{}).Where("shop_id = ? AND platform = ?", shopID, "pinterest").Count(&connCount)
	assert.Equal(t, int64(0), connCount)

	var postCount int64
	db.Model(&model.SocialPost{}).Where("shop_id = ? AND platform = ?", shopID, "pinterest").Count(&postCount)
	assert.Equal(t, int64(0), postCount)
}

func TestDisconnect_MissingShopID(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	r := req("POST", "/api/social/pinterest/disconnect", "pinterest", `{}`)
	rec := httptest.NewRecorder()
	h.Disconnect(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ── Publish ──────────────────────────────────────────────────────────────────

func TestPublish_MissingShopID(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	body := `{"title":"Test","description":"desc","image_url":"https://img.example/1.jpg"}`
	r := req("POST", "/api/social/pinterest/publish", "pinterest", body)
	rec := httptest.NewRecorder()
	h.Publish(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPublish_NoActiveConnection(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()

	body := `{"shop_id":"` + shopID + `","title":"Test"}`
	r := req("POST", "/api/social/pinterest/publish", "pinterest", body)
	rec := httptest.NewRecorder()
	h.Publish(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPublish_Success(t *testing.T) {
	h, db := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()
	crypto, err := service.NewCryptoService("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	require.NoError(t, err)
	encTok, err := crypto.Encrypt([]byte("mock-access-token"))
	require.NoError(t, err)
	seedConnection(t, db, shopID, "pinterest", encTok)

	body := `{"shop_id":"` + shopID + `","title":"Summer Dress","description":"Beautiful summer dress","image_url":"https://img.example/1.jpg","link_url":"https://shop.example/dress"}`
	r := req("POST", "/api/social/pinterest/publish", "pinterest", body)
	rec := httptest.NewRecorder()
	h.Publish(rec, r)

	assert.Equal(t, http.StatusCreated, rec.Code)
	m := parseJSON(t, rec)
	assert.True(t, m["success"].(bool))
	assert.Equal(t, "pinterest", m["platform"])
	assert.Equal(t, "post-123", m["platform_id"])

	var count int64
	db.Model(&model.SocialPost{}).Where("shop_id = ? AND platform = ?", shopID, "pinterest").Count(&count)
	assert.Equal(t, int64(1), count)
}

func TestPublish_NoCrypto(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	h.crypto = nil

	r := req("POST", "/api/social/pinterest/publish", "pinterest", `{"shop_id":"test","title":"Test"}`)
	rec := httptest.NewRecorder()
	h.Publish(rec, r)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestPublish_PublisherError(t *testing.T) {
	h, db := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()
	crypto, err := service.NewCryptoService("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	require.NoError(t, err)
	encTok, err := crypto.Encrypt([]byte("mock-access-token"))
	require.NoError(t, err)
	seedConnection(t, db, shopID, "pinterest", encTok)

	// Override mock to simulate publish failure
	pub := h.publishers["pinterest"].(*mockPublisher)
	pub.publishErr = fmt.Errorf("pinterest API: 403 Forbidden: {\"message\":\"Invalid access token\"}")

	body := `{"shop_id":"` + shopID + `","title":"Test","description":"desc","image_url":"https://img.example/1.jpg"}`
	r := req("POST", "/api/social/pinterest/publish", "pinterest", body)
	rec := httptest.NewRecorder()
	h.Publish(rec, r)

	assert.Equal(t, http.StatusBadGateway, rec.Code) // 502 — sanitized, no raw API error
}

func TestPublish_UnsupportedPlatform(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	r := req("POST", "/api/social/tiktok/publish", "tiktok", `{"shop_id":"test","title":"Test"}`)
	rec := httptest.NewRecorder()
	h.Publish(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPublish_DecryptError(t *testing.T) {
	h, db := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()
	seedConnection(t, db, shopID, "pinterest", "not-really-encrypted")

	body := `{"shop_id":"` + shopID + `","title":"Test","description":"desc"}`
	r := req("POST", "/api/social/pinterest/publish", "pinterest", body)
	rec := httptest.NewRecorder()
	h.Publish(rec, r)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// ── Posts ────────────────────────────────────────────────────────────────────

func TestPosts_Success(t *testing.T) {
	h, db := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()
	connID := seedConnection(t, db, shopID, "pinterest", "enc-token")
	seedPost(t, db, shopID, connID, "pinterest", "Post 1")
	seedPost(t, db, shopID, connID, "pinterest", "Post 2")

	r := httptest.NewRequest("GET", "/api/social/posts?shop_id="+shopID, nil)
	rec := httptest.NewRecorder()
	h.Posts(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)
	m := parseJSON(t, rec)
	assert.True(t, m["success"].(bool))
	assert.Equal(t, float64(2), m["total"])
	assert.Equal(t, 2, len(m["posts"].([]interface{})))
}

func TestPosts_FilterByPlatform(t *testing.T) {
	h, db := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()
	connID := seedConnection(t, db, shopID, "pinterest", "enc-token")
	seedPost(t, db, shopID, connID, "pinterest", "Pin 1")
	seedPost(t, db, shopID, connID, "instagram", "IG Post")

	r := httptest.NewRequest("GET", "/api/social/posts?shop_id="+shopID+"&platform=pinterest", nil)
	rec := httptest.NewRecorder()
	h.Posts(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)
	m := parseJSON(t, rec)
	assert.Equal(t, float64(1), m["total"])
}

func TestPosts_EmptyShop(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()

	r := httptest.NewRequest("GET", "/api/social/posts?shop_id="+shopID, nil)
	rec := httptest.NewRecorder()
	h.Posts(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)
	m := parseJSON(t, rec)
	assert.Equal(t, float64(0), m["total"])
}

func TestPosts_MissingShopID(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	r := httptest.NewRequest("GET", "/api/social/posts", nil)
	rec := httptest.NewRecorder()
	h.Posts(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ── DeletePost ───────────────────────────────────────────────────────────────

func TestDeletePost_Success(t *testing.T) {
	h, db := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()
	connID := seedConnection(t, db, shopID, "pinterest", "enc-token")
	postID := seedPost(t, db, shopID, connID, "pinterest", "Delete Me")

	r := httptest.NewRequest("DELETE", "/api/social/posts/"+postID+"?shop_id="+shopID, nil)
	r = withChiParam(r, "postID", postID)
	rec := httptest.NewRecorder()
	h.DeletePost(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)
	m := parseJSON(t, rec)
	assert.Equal(t, "true", m["success"])

	var count int64
	db.Model(&model.SocialPost{}).Where("id = ?", postID).Count(&count)
	assert.Equal(t, int64(0), count)
}

func TestDeletePost_MissingShopID(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	r := httptest.NewRequest("DELETE", "/api/social/posts/some-id", nil)
	r = withChiParam(r, "postID", "some-id")
	rec := httptest.NewRecorder()
	h.DeletePost(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDeletePost_NotFound(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()

	r := httptest.NewRequest("DELETE", "/api/social/posts/nonexistent?shop_id="+shopID, nil)
	r = withChiParam(r, "postID", "nonexistent")
	rec := httptest.NewRecorder()
	h.DeletePost(rec, r)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestDeletePost_EmptyPostID(t *testing.T) {
	h, _ := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()

	r := httptest.NewRequest("DELETE", "/api/social/posts/?shop_id="+shopID, nil)
	rec := httptest.NewRecorder()
	h.DeletePost(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ── Multiple publishers ──────────────────────────────────────────────────────

func TestMultiplePublishers_PlatformsList(t *testing.T) {
	h, db := setupSocialTest(t, "pinterest")
	shopID := uuid.New().String()

	ig := &mockPublisher{platform: "instagram", authURL: "https://ig.example/oauth", publishID: "ig-123"}
	h.publishers["instagram"] = ig

	seedConnection(t, db, shopID, "pinterest", "enc-token")

	r := httptest.NewRequest("GET", "/api/social/platforms?shop_id="+shopID, nil)
	rec := httptest.NewRecorder()
	h.Platforms(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)
	m := parseJSON(t, rec)
	platforms := m["platforms"].([]interface{})
	assert.Equal(t, 2, len(platforms))

	connected := make(map[string]bool)
	for _, p := range platforms {
		pm := p.(map[string]interface{})
		connected[pm["platform"].(string)] = pm["connected"].(bool)
	}
	assert.True(t, connected["pinterest"])
	assert.False(t, connected["instagram"])
}
