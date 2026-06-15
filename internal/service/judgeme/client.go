// Package judgeme provides a Judge.me API HTTP client.
package judgeme

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	defaultBaseURL = "https://api.judge.me/api/v1"
	defaultTimeout = 30 * time.Second
)

// --- Data Models (DTOs) ---

// JudgeMeReview represents a review from the Judge.me API.
type JudgeMeReview struct {
	ID                int     `json:"id"`
	Title             string  `json:"title"`
	Body              string  `json:"body"`
	Rating            float64 `json:"rating"`
	ReviewerName      string  `json:"reviewer_name"`
	ReviewerEmail     string  `json:"reviewer_email"`
	CreatedAt         string  `json:"created_at"`
	ProductExternalID int64   `json:"product_external_id"`
	PublicReply       *string `json:"public_reply"`
	State             string  `json:"state"` // published / unpublished / spam
	Verified          bool    `json:"verified"`
}

// GetReviewsResponse is the paginated response from GET /reviews.
type GetReviewsResponse struct {
	Reviews    []JudgeMeReview `json:"reviews"`
	Total      int             `json:"total"`
	Page       int             `json:"page"`
	PerPage    int             `json:"per_page"`
	TotalPages int             `json:"total_pages"`
}

// ListReviewsParams holds filter/pagination parameters for listing reviews.
type ListReviewsParams struct {
	RatingMin    *int
	RatingMax    *int
	CreatedAtMin *string // RFC3339
	CreatedAtMax *string // RFC3339
	Page         int
	PerPage      int
}

// Webhook represents a Judge.me webhook registration.
type Webhook struct {
	ID     int      `json:"id"`
	URL    string   `json:"url"`
	Events []string `json:"events"`
	Active bool     `json:"active"`
}

// CreateWebhookRequest is the request body for POST /webhooks.
type CreateWebhookRequest struct {
	Webhook struct {
		URL    string   `json:"url"`
		Events []string `json:"events"`
	} `json:"webhook"`
}

// CreatedWebhookResponse is the response from POST /webhooks.
type CreatedWebhookResponse struct {
	Webhook Webhook `json:"webhook"`
}

// ListWebhooksResponse is the response from GET /webhooks.
type ListWebhooksResponse struct {
	Webhooks []Webhook `json:"webhooks"`
}

// CreateReplyRequest is the request body for POST /replies.
type CreateReplyRequest struct {
	Reply struct {
		ReviewID int    `json:"review_id"`
		Content  string `json:"content"`
	} `json:"reply"`
}

// --- Client ---

// JudgeMeClient is an HTTP client for the Judge.me API.
type JudgeMeClient struct {
	apiToken   string
	shopDomain string
	baseURL    string
	httpClient *http.Client
}

// NewJudgeMeClient creates a new JudgeMeClient.
func NewJudgeMeClient(apiToken, shopDomain string) *JudgeMeClient {
	return &JudgeMeClient{
		apiToken:   apiToken,
		shopDomain: shopDomain,
		baseURL:    defaultBaseURL,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

// request performs an authenticated HTTP request to the Judge.me API.
func (c *JudgeMeClient) request(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	u, err := url.JoinPath(c.baseURL, path)
	if err != nil {
		return nil, fmt.Errorf("judgeme: build URL: %w", err)
	}

	var reqBodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("judgeme: marshal body: %w", err)
		}
		reqBodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBodyReader)
	if err != nil {
		return nil, fmt.Errorf("judgeme: new request: %w", err)
	}

	req.Header.Set("X-Api-Token", c.apiToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	slog.Debug("judgeme: request",
		"method", method,
		"path", path,
		"shop_domain", c.shopDomain,
	)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("judgeme: do request: %w", err)
	}

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("judgeme: API error: status=%d body=%s", resp.StatusCode, string(bodyBytes))
	}

	return resp, nil
}

// decodeJSON reads the response body and decodes it into v.
func decodeJSON(resp *http.Response, v interface{}) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// GetReviews retrieves a paginated list of reviews with optional filters.
func (c *JudgeMeClient) GetReviews(ctx context.Context, params ListReviewsParams) (*GetReviewsResponse, error) {
	q := url.Values{}
	if params.RatingMin != nil {
		q.Set("rating_min", strconv.Itoa(*params.RatingMin))
	}
	if params.RatingMax != nil {
		q.Set("rating_max", strconv.Itoa(*params.RatingMax))
	}
	if params.CreatedAtMin != nil {
		q.Set("created_at_min", *params.CreatedAtMin)
	}
	if params.CreatedAtMax != nil {
		q.Set("created_at_max", *params.CreatedAtMax)
	}
	if params.Page > 0 {
		q.Set("page", strconv.Itoa(params.Page))
	}
	if params.PerPage > 0 {
		q.Set("per_page", strconv.Itoa(params.PerPage))
	}

	path := "/reviews"
	if len(q) > 0 {
		path = path + "?" + q.Encode()
	}

	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("judgeme: get reviews: %w", err)
	}

	var result GetReviewsResponse
	if err := decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("judgeme: decode reviews: %w", err)
	}

	slog.Info("judgeme: got reviews",
		"shop_domain", c.shopDomain,
		"total", result.Total,
		"page", result.Page,
		"per_page", result.PerPage,
	)

	return &result, nil
}

// GetReview retrieves a single review by ID.
func (c *JudgeMeClient) GetReview(ctx context.Context, id int) (*JudgeMeReview, error) {
	path := fmt.Sprintf("/reviews/%d", id)

	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("judgeme: get review %d: %w", id, err)
	}

	// The API may return the review directly or wrapped
	var raw json.RawMessage
	if err := decodeJSON(resp, &raw); err != nil {
		return nil, fmt.Errorf("judgeme: decode review raw: %w", err)
	}

	// Try direct unwrap first, then wrapped in "review" key
	var review JudgeMeReview
	if err := json.Unmarshal(raw, &review); err != nil {
		// Try wrapped
		var wrapper struct {
			Review JudgeMeReview `json:"review"`
		}
		if err2 := json.Unmarshal(raw, &wrapper); err2 != nil {
			return nil, fmt.Errorf("judgeme: parse review %d: direct=%v wrapped=%v", id, err, err2)
		}
		review = wrapper.Review
	}

	slog.Info("judgeme: got review", "id", id, "rating", review.Rating)
	return &review, nil
}

// CreateReply creates a public reply to a review.
func (c *JudgeMeClient) CreateReply(ctx context.Context, reviewID int, content string) error {
	req := CreateReplyRequest{}
	req.Reply.ReviewID = reviewID
	req.Reply.Content = content

	resp, err := c.request(ctx, http.MethodPost, "/replies", req)
	if err != nil {
		return fmt.Errorf("judgeme: create reply for review %d: %w", reviewID, err)
	}
	resp.Body.Close()

	slog.Info("judgeme: reply created", "review_id", reviewID)
	return nil
}

// ListWebhooks retrieves all registered webhooks.
func (c *JudgeMeClient) ListWebhooks(ctx context.Context) ([]Webhook, error) {
	resp, err := c.request(ctx, http.MethodGet, "/webhooks", nil)
	if err != nil {
		return nil, fmt.Errorf("judgeme: list webhooks: %w", err)
	}

	var result ListWebhooksResponse
	if err := decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("judgeme: decode webhooks: %w", err)
	}

	slog.Info("judgeme: listed webhooks", "count", len(result.Webhooks))
	return result.Webhooks, nil
}

// CreateWebhook registers a new webhook.
func (c *JudgeMeClient) CreateWebhook(ctx context.Context, url string, events []string) error {
	req := CreateWebhookRequest{}
	req.Webhook.URL = url
	req.Webhook.Events = events

	resp, err := c.request(ctx, http.MethodPost, "/webhooks", req)
	if err != nil {
		return fmt.Errorf("judgeme: create webhook: %w", err)
	}
	resp.Body.Close()

	slog.Info("judgeme: webhook created", "url", url, "events", events)
	return nil
}

// DeleteWebhook removes a webhook by ID.
func (c *JudgeMeClient) DeleteWebhook(ctx context.Context, id int) error {
	path := fmt.Sprintf("/webhooks/%d", id)

	resp, err := c.request(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("judgeme: delete webhook %d: %w", id, err)
	}
	resp.Body.Close()

	slog.Info("judgeme: webhook deleted", "id", id)
	return nil
}

// VerifyWebhookSignature verifies an HMAC-SHA256 signature from a Judge.me webhook.
// The signature comes from the X-Judgeme-Signature header.
func VerifyWebhookSignature(signature string, body []byte, secret string) bool {
	if signature == "" || secret == "" {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(signature))
}
