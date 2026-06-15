package sync

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	// Shopify REST API rate limit: 2 requests per second.
	rateInterval = 500 * time.Millisecond

	// Max retries for retryable errors (429, 5xx).
	maxRetries = 3

	// Base backoff for exponential retry: 1s, 2s, 4s.
	baseBackoff = 1 * time.Second
)

type ShopifyRESTClient struct {
	client     *http.Client
	apiVersion string
	limiter    *time.Ticker
}

func NewShopifyRESTClient(v string) *ShopifyRESTClient {
	return &ShopifyRESTClient{
		client:     &http.Client{Timeout: 30 * time.Second},
		apiVersion: v,
		limiter:    time.NewTicker(rateInterval),
	}
}

// Close stops the internal rate-limit ticker. Call when the client is no
// longer needed to avoid leaking the goroutine backing the ticker.
func (c *ShopifyRESTClient) Close() {
	if c.limiter != nil {
		c.limiter.Stop()
	}
}

type PageResult struct {
	Body        []byte
	NextPageURL string
}

func (c *ShopifyRESTClient) GetPaginated(ctx context.Context, domain, token, path string, params url.Values) (*PageResult, error) {
	u := fmt.Sprintf("https://%s/admin/api/%s/%s", domain, c.apiVersion, path)
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	return c.doWithRetry(ctx, u, token)
}

func (c *ShopifyRESTClient) GetNextPage(ctx context.Context, url, token string) (*PageResult, error) {
	return c.doWithRetry(ctx, url, token)
}

// doWithRetry calls do() with exponential backoff on retryable errors
// (429 Too Many Requests, 5xx server errors). It retries up to maxRetries
// times with backoff durations of 1s, 2s, and 4s.
func (c *ShopifyRESTClient) doWithRetry(ctx context.Context, u, token string) (*PageResult, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * baseBackoff
			slog.Warn("sync: retrying request", "url", u, "attempt", attempt, "backoff", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		result, err := c.do(ctx, u, token)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("sync: max retries (%d) exceeded: %w", maxRetries, lastErr)
}

// isRetryable returns true for 429 (rate limit) and 5xx (server) errors.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "HTTP 429") || strings.Contains(msg, "HTTP 5")
}

func (c *ShopifyRESTClient) do(ctx context.Context, u, token string) (*PageResult, error) {
	// Enforce Shopify rate limit (2 req/s).
	if c.limiter != nil {
		select {
		case <-c.limiter.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("X-Shopify-Access-Token", token)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("sync: HTTP %d: %s", resp.StatusCode, string(body))
	}
	re := regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)
	next := ""
	if m := re.FindStringSubmatch(resp.Header.Get("Link")); len(m) >= 2 {
		next = m[1]
	}
	return &PageResult{Body: body, NextPageURL: next}, nil
}

var linkNextRE = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func parseNextPageURL(linkHeader string) string {
	if linkHeader == "" {
		return ""
	}
	matches := linkNextRE.FindStringSubmatch(linkHeader)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}
