package meta

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	socialsvc "github.com/JingxuanC/vela-engine/internal/service/social"
)

func TestMetaClient_Platform(t *testing.T) {
	c := NewClient("app-id", "app-secret", "https://cb.example/oauth")
	assert.Equal(t, "instagram", c.Platform())
}

func TestMetaClient_AuthURL(t *testing.T) {
	c := NewClient("my-app", "my-secret", "https://cb.example/oauth")
	u := c.AuthURL("state-xyz")
	assert.Contains(t, u, "facebook.com/v19.0/dialog/oauth?")
	assert.Contains(t, u, "client_id=my-app")
	assert.Contains(t, u, "state=state-xyz")
	assert.Contains(t, u, "instagram_content_publish")
}

func TestMetaClient_AuthURL_ContainsRequiredScopes(t *testing.T) {
	c := NewClient("cid", "csec", "https://myapp.com/callback")
	u := c.AuthURL("state123")
	require.Contains(t, u, "instagram_basic")
	require.Contains(t, u, "instagram_content_publish")
	require.Contains(t, u, "pages_show_list")
	require.Contains(t, u, "pages_read_engagement")
	require.Contains(t, u, "response_type=code")
}

// The following tests make real HTTP calls to FB Graph API.
// They validate error handling and request structure; skip in -short mode.

func TestMetaClient_Publish_NilRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real API call in short mode")
	}
	c := NewClient("app-id", "app-secret", "https://cb.example/oauth")
	_, err := c.Publish(t.Context(), "token", nil)
	assert.Error(t, err)
}

func TestMetaClient_Publish_EmptyImageURL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real API call in short mode")
	}
	c := NewClient("app-id", "app-secret", "https://cb.example/oauth")
	req := &socialsvc.PublishRequest{
		Title:       "Test",
		Description: "Caption text",
		ImageURL:    "",
	}
	_, err := c.Publish(t.Context(), "invalid-token", req)
	assert.Error(t, err)
}

func TestMetaClient_Publish_TitleFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real API call in short mode")
	}
	c := NewClient("app-id", "app-secret", "https://cb.example/oauth")
	req := &socialsvc.PublishRequest{
		Title:       "Fallback Title Text",
		Description: "",
		ImageURL:    "https://img.example/1.jpg",
	}
	_, err := c.Publish(t.Context(), "token", req)
	assert.Error(t, err)
}

func TestMetaClient_ExchangeCode_InvalidCode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real API call in short mode")
	}
	c := NewClient("app-id", "app-secret", "https://cb.example/oauth")
	_, err := c.ExchangeCode(t.Context(), "bad-code")
	assert.Error(t, err)
}
