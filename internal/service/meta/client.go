package meta

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	socialsvc "github.com/JingxuanC/vela-engine/internal/service/social"
)

// InstagramAccount holds basic Instagram business account info.
type InstagramAccount struct {
	ID         string
	Username   string
	Name       string
	ProfilePic string
	Followers  int
	MediaCount int
}

// Client implements social.PlatformPublisher for Instagram (via Meta Graph API).
type Client struct {
	appID       string
	appSecret   string
	redirectURI string
	httpClient  *http.Client
}

func NewClient(aid, asec, ru string) *Client {
	return &Client{aid, asec, ru, &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Platform() string { return "instagram" }

func (c *Client) AuthURL(state string) string {
	p := url.Values{
		"client_id":     {c.appID},
		"redirect_uri":  {c.redirectURI},
		"response_type": {"code"},
		"scope":         {"instagram_basic,instagram_content_publish,pages_show_list,pages_read_engagement"},
		"state":         {state},
	}
	return "https://www.facebook.com/v19.0/dialog/oauth?" + p.Encode()
}

func (c *Client) ExchangeCode(ctx context.Context, code string) (*socialsvc.TokenResponse, error) {
	p := url.Values{
		"client_id":     {c.appID},
		"redirect_uri":  {c.redirectURI},
		"client_secret": {c.appSecret},
		"code":          {code},
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://graph.facebook.com/v19.0/oauth/access_token?"+p.Encode(), nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("meta: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var t struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("meta: parse token: %w", err)
	}
	return &socialsvc.TokenResponse{
		AccessToken: t.AccessToken,
		TokenType:   t.TokenType,
		ExpiresIn:   t.ExpiresIn,
	}, nil
}

// ExchangeLongLivedToken converts a short-lived token to a long-lived one (60-day expiry).
func (c *Client) ExchangeLongLivedToken(ctx context.Context, shortToken string) (string, error) {
	p := url.Values{
		"grant_type":        {"fb_exchange_token"},
		"client_id":         {c.appID},
		"client_secret":     {c.appSecret},
		"fb_exchange_token": {shortToken},
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://graph.facebook.com/v19.0/oauth/access_token?"+p.Encode(), nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return "", fmt.Errorf("meta: parse long-lived token: %w", err)
	}
	return t.AccessToken, nil
}

// GetInstagramAccount fetches the Instagram Business Account for the authenticated user.
func (c *Client) GetInstagramAccount(ctx context.Context, accessToken string) (*InstagramAccount, error) {
	u := fmt.Sprintf("https://graph.facebook.com/v19.0/me/accounts?fields=instagram_business_account{id,username,name,profile_picture_url,followers_count,media_count}&access_token=%s", accessToken)
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		Data []struct {
			InstagramBusinessAccount *struct {
				ID         string `json:"id"`
				Username   string `json:"username"`
				Name       string `json:"name"`
				ProfilePic string `json:"profile_picture_url"`
				Followers  int    `json:"followers_count"`
				MediaCount int    `json:"media_count"`
			} `json:"instagram_business_account"`
		} `json:"data"`
	}
	json.Unmarshal(body, &r)
	for _, d := range r.Data {
		if d.InstagramBusinessAccount != nil {
			a := d.InstagramBusinessAccount
			return &InstagramAccount{
				ID: a.ID, Username: a.Username, Name: a.Name,
				ProfilePic: a.ProfilePic, Followers: a.Followers, MediaCount: a.MediaCount,
			}, nil
		}
	}
	return nil, fmt.Errorf("meta: no Instagram business account found; link your IG to a Facebook Page first")
}

// Publish posts content to Instagram as a single-image media post.
// It first creates a media container, then publishes it.
// Requires the access token to have instagram_content_publish scope.
func (c *Client) Publish(ctx context.Context, accessToken string, req *socialsvc.PublishRequest) (*socialsvc.PublishResult, error) {
	// 1. Get Instagram Business Account ID
	ig, err := c.GetInstagramAccount(ctx, accessToken)
	if err != nil {
		return nil, fmt.Errorf("meta: get IG account: %w", err)
	}

	// 2. Create media container
	caption := req.Description
	if caption == "" {
		caption = req.Title
	}

	createURL := fmt.Sprintf("https://graph.facebook.com/v19.0/%s/media", ig.ID)
	payload := map[string]string{
		"caption":   caption,
		"image_url": req.ImageURL,
	}
	body, _ := json.Marshal(payload)

	httpReq, _ := http.NewRequestWithContext(ctx, "POST", createURL, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	q := httpReq.URL.Query()
	q.Set("access_token", accessToken)
	httpReq.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("meta: create media: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("meta: create media HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var createResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &createResp); err != nil {
		return nil, fmt.Errorf("meta: parse create media response: %w", err)
	}

	// 3. Publish the media container
	publishURL := fmt.Sprintf("https://graph.facebook.com/v19.0/%s/media_publish", ig.ID)
	pubPayload := map[string]string{"creation_id": createResp.ID}
	pubBody, _ := json.Marshal(pubPayload)

	httpReq2, _ := http.NewRequestWithContext(ctx, "POST", publishURL, bytes.NewReader(pubBody))
	httpReq2.Header.Set("Content-Type", "application/json")
	q2 := httpReq2.URL.Query()
	q2.Set("access_token", accessToken)
	httpReq2.URL.RawQuery = q2.Encode()

	resp2, err := c.httpClient.Do(httpReq2)
	if err != nil {
		return nil, fmt.Errorf("meta: publish media: %w", err)
	}
	defer resp2.Body.Close()
	respBody2, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != 200 {
		return nil, fmt.Errorf("meta: publish media HTTP %d: %s", resp2.StatusCode, string(respBody2))
	}

	var pubResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody2, &pubResp); err != nil {
		return nil, fmt.Errorf("meta: parse publish response: %w", err)
	}

	return &socialsvc.PublishResult{
		PlatformPostID: pubResp.ID,
		PermalinkURL:   fmt.Sprintf("https://www.instagram.com/p/%s/", pubResp.ID),
	}, nil
}

// GetAnalytics returns platform metrics for an Instagram media post.
// NOTE: Requires instagram_manage_insights scope (not yet requested — see Phase 2).
func (c *Client) GetAnalytics(ctx context.Context, accessToken string, postID string, since time.Time) (*socialsvc.PlatformMetrics, error) {
	return nil, fmt.Errorf("meta: analytics not yet implemented — requires instagram_manage_insights scope")
}
