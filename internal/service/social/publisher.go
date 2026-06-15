package social

import (
	"context"
	"time"
)

// TokenResponse is the standard OAuth token response shared across all platforms.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// PublishRequest holds the content to publish to a social platform.
type PublishRequest struct {
	Title       string
	Description string
	ImageURL    string
	LinkURL     string
	BoardID     string // Pinterest only; ignored by other platforms
}

// PublishResult holds the result of a platform publish operation.
type PublishResult struct {
	PlatformPostID string
	PermalinkURL   string
	BoardID        string // Pinterest board; empty for other platforms
}

// PlatformMetrics holds analytics metrics pulled from a platform for a given post.
type PlatformMetrics struct {
	Impressions int
	Clicks      int
	Saves       int
	Engagement  int
}

// PlatformPublisher is the unified interface for all social platform API clients.
// Each platform (Pinterest, Instagram, YouTube) implements this interface.
type PlatformPublisher interface {
	// Platform returns the platform identifier (e.g. "pinterest", "instagram", "youtube").
	Platform() string

	// AuthURL builds the OAuth authorization URL for the given state token.
	AuthURL(state string) string

	// ExchangeCode exchanges an OAuth authorization code for an access token.
	ExchangeCode(ctx context.Context, code string) (*TokenResponse, error)

	// Publish publishes content to the platform.
	Publish(ctx context.Context, accessToken string, req *PublishRequest) (*PublishResult, error)

	// GetAnalytics returns platform metrics for a specific post since a given date.
	GetAnalytics(ctx context.Context, accessToken string, postID string, since time.Time) (*PlatformMetrics, error)
}
