// Package shopify provides a Shopify Admin API client for GraphQL queries.
package shopify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// AdminClient queries the Shopify Admin GraphQL API.
type AdminClient struct {
	apiVersion string
	httpClient *http.Client
}

// NewAdminClient creates a new AdminClient.
func NewAdminClient(apiVersion string) *AdminClient {
	return &AdminClient{
		apiVersion: apiVersion,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// FetchOrderLandingPage queries the Shopify Admin GraphQL API for the order's
// lastVisit landingPage URL. Returns the URL string, or empty if unavailable.
func (c *AdminClient) FetchOrderLandingPage(
	ctx context.Context, shopDomain, accessToken, orderGID string,
) (string, error) {
	query := fmt.Sprintf(`{
		node(id: "%s") {
			... on Order {
				customerJourney {
					lastVisit {
						landingPage { url }
					}
				}
			}
		}
	}`, orderGID)

	body := map[string]interface{}{"query": query}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal request body: %w", err)
	}

	url := fmt.Sprintf("https://%s/admin/api/%s/graphql.json", shopDomain, c.apiVersion)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyJSON))
	if err != nil {
		return "", fmt.Errorf("admin API request: %w", err)
	}
	req.Header.Set("X-Shopify-Access-Token", accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("admin API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("admin API: %s — %s", resp.Status, string(b))
	}

	var result struct {
		Data struct {
			Node struct {
				CustomerJourney struct {
					LastVisit struct {
						LandingPage struct {
							URL string `json:"url"`
						} `json:"landingPage"`
					} `json:"lastVisit"`
				} `json:"customerJourney"`
			} `json:"node"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	return result.Data.Node.CustomerJourney.LastVisit.LandingPage.URL, nil
}
