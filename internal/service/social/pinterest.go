package social

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const pinterestAPIBase = "https://api.pinterest.com/v5"

// Board holds basic Pinterest board info.
type Board struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	PinCount int    `json:"pin_count"`
}

// PinResponse is the Pinterest API response for a created pin.
type PinResponse struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	BoardID string `json:"board_id"`
}

// PinterestClient implements PlatformPublisher for Pinterest.
type PinterestClient struct {
	clientID     string
	clientSecret string
	redirectURI  string
	httpClient   *http.Client
}

func NewPinterestClient(clientID, clientSecret, redirectURI string) *PinterestClient {
	return &PinterestClient{
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURI:  redirectURI,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *PinterestClient) Platform() string { return "pinterest" }

func (c *PinterestClient) AuthURL(state string) string {
	p := url.Values{
		"client_id":     {c.clientID},
		"redirect_uri":  {c.redirectURI},
		"response_type": {"code"},
		"scope":         {"boards:read,boards:write,pins:read,pins:write"},
		"state":         {state},
	}
	return "https://www.pinterest.com/oauth/?" + p.Encode()
}

func (c *PinterestClient) ExchangeCode(ctx context.Context, code string) (*TokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.redirectURI},
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.pinterest.com/v5/oauth/token", strings.NewReader(form.Encode()))
	req.SetBasicAuth(c.clientID, c.clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("pinterest: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var t TokenResponse
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("pinterest: parse token: %w", err)
	}
	return &t, nil
}

func (c *PinterestClient) ListBoards(ctx context.Context, accessToken string) ([]Board, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", pinterestAPIBase+"/boards", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		Items []Board `json:"items"`
	}
	json.Unmarshal(body, &r)
	return r.Items, nil
}

// Publish creates a Pin on Pinterest. BoardID from req is used if set;
// otherwise the first board from ListBoards is used as default.
func (c *PinterestClient) Publish(ctx context.Context, accessToken string, req *PublishRequest) (*PublishResult, error) {
	boardID := req.BoardID
	if boardID == "" {
		boards, err := c.ListBoards(ctx, accessToken)
		if err != nil {
			return nil, fmt.Errorf("pinterest: list boards: %w", err)
		}
		if len(boards) == 0 {
			return nil, fmt.Errorf("pinterest: no boards found; create a board first")
		}
		boardID = boards[0].ID
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"title":       req.Title,
		"description": req.Description,
		"board_id":    boardID,
		"media_source": map[string]string{
			"source_type": "image_url",
			"url":         req.ImageURL,
		},
		"link": req.LinkURL,
	})

	httpReq, _ := http.NewRequestWithContext(ctx, "POST", pinterestAPIBase+"/pins", strings.NewReader(string(payload)))
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return nil, fmt.Errorf("pinterest: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var pin PinResponse
	if err := json.Unmarshal(body, &pin); err != nil {
		return nil, fmt.Errorf("pinterest: parse pin: %w", err)
	}
	return &PublishResult{
		PlatformPostID: pin.ID,
		PermalinkURL:   "https://www.pinterest.com/pin/" + pin.ID,
		BoardID:        boardID,
	}, nil
}

// GetAnalytics fetches metrics for a specific Pin from the Pinterest Analytics API.
// It requests IMPRESSION, PIN_CLICK, and SAVE metrics since the given date.
func (c *PinterestClient) GetAnalytics(ctx context.Context, accessToken string, postID string, since time.Time) (*PlatformMetrics, error) {
	dateStr := since.Format("2006-01-02")
	apiURL := fmt.Sprintf("%s/pins/%s/analytics?start_date=%s&metrics=IMPRESSION,PIN_CLICK,SAVE",
		pinterestAPIBase, postID, dateStr)

	req, _ := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pinterest: analytics request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("pinterest: analytics HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Response shape: {"IMP RESSION": 123, "PIN_CLICK": 45, "SAVE": 6, ...}
	// Keys may arrive as-is or with spaces; try both forms.
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("pinterest: parse analytics: %w", err)
	}

	metrics := &PlatformMetrics{}
	for k, v := range raw {
		val, ok := toInt(v)
		if !ok {
			continue
		}
		switch k {
		case "IMPRESSION":
			metrics.Impressions = val
		case "PIN_CLICK":
			metrics.Clicks = val
		case "SAVE":
			metrics.Saves = val
		}
	}
	// Engagement = clicks + saves
	metrics.Engagement = metrics.Clicks + metrics.Saves

	return metrics, nil
}

// toInt converts an interface{} (float64 from JSON) to int.
func toInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	}
	return 0, false
}
