package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	socialsvc "github.com/JingxuanC/vela-engine/internal/service/social"
)

// ChannelInfo holds basic YouTube channel metadata.
type ChannelInfo struct {
	ID              string
	Title           string
	SubscriberCount uint64
	VideoCount      uint64
}

// Client implements social.PlatformPublisher for YouTube.
type Client struct {
	clientID     string
	clientSecret string
	redirectURI  string
	apiKey       string
	httpClient   *http.Client
}

func NewClient(cid, cs, ru, ak string) *Client {
	return &Client{cid, cs, ru, ak, &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Platform() string { return "youtube" }

func (c *Client) AuthURL(state string) string {
	p := url.Values{
		"client_id":     {c.clientID},
		"redirect_uri":  {c.redirectURI},
		"response_type": {"code"},
		"scope":         {"https://www.googleapis.com/auth/youtube.upload https://www.googleapis.com/auth/youtube.readonly"},
		"access_type":   {"offline"},
		"state":         {state},
		"prompt":        {"consent"},
	}
	return "https://accounts.google.com/o/oauth2/v2/auth?" + p.Encode()
}

func (c *Client) ExchangeCode(ctx context.Context, code string) (*socialsvc.TokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.redirectURI},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("youtube: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var t socialsvc.TokenResponse
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("youtube: parse token: %w", err)
	}
	return &t, nil
}

// GetChannel fetches the authenticated user's YouTube channel info.
func (c *Client) GetChannel(ctx context.Context, accessToken string) (*ChannelInfo, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/youtube/v3/channels?part=snippet,statistics&mine=true", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title      string `json:"title"`
				Thumbnails map[string]struct {
					URL string `json:"url"`
				} `json:"thumbnails"`
			} `json:"snippet"`
			Statistics struct {
				SubscriberCount string `json:"subscriberCount"`
				VideoCount      string `json:"videoCount"`
			} `json:"statistics"`
		} `json:"items"`
	}
	json.Unmarshal(body, &r)
	if len(r.Items) == 0 {
		return &ChannelInfo{ID: "unknown", Title: "Unknown"}, nil
	}
	return &ChannelInfo{
		ID:              r.Items[0].ID,
		Title:           r.Items[0].Snippet.Title,
		SubscriberCount: parseU64(r.Items[0].Statistics.SubscriberCount),
		VideoCount:      parseU64(r.Items[0].Statistics.VideoCount),
	}, nil
}

// Publish is not supported for YouTube via the text+image content factory flow.
// YouTube publishing requires a video file upload (multipart resumable upload).
// This returns a clear error instead of the old fake-success stub.
func (c *Client) Publish(ctx context.Context, accessToken string, req *socialsvc.PublishRequest) (*socialsvc.PublishResult, error) {
	return nil, fmt.Errorf("youtube: text+image publishing is not supported; YouTube requires video file upload via the YouTube Studio")
}

// GetAnalytics returns platform metrics for a YouTube video.
// Stub — YouTube Analytics API requires OAuth scope approval.
func (c *Client) GetAnalytics(ctx context.Context, accessToken string, postID string, since time.Time) (*socialsvc.PlatformMetrics, error) {
	return &socialsvc.PlatformMetrics{}, nil
}

func parseU64(s string) uint64 {
	var n uint64
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + uint64(ch-'0')
	}
	return n
}
