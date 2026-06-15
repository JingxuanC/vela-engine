package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// WebhookRegistrar registers Shopify webhooks for real-time data sync.
type WebhookRegistrar struct {
	apiVersion string
	http       *http.Client
}

// NewWebhookRegistrar creates a new webhook registrar.
func NewWebhookRegistrar(apiVersion string) *WebhookRegistrar {
	return &WebhookRegistrar{
		apiVersion: apiVersion,
		http:       &http.Client{Timeout: 15 * time.Second},
	}
}

type webhookRegistration struct {
	Topic   string `json:"topic"`
	Address string `json:"address"`
	Format  string `json:"format"`
}

// RegisterSyncWebhooks registers the essential webhooks for product, order, and customer sync.
func (r *WebhookRegistrar) RegisterSyncWebhooks(ctx context.Context, shopDomain, accessToken string) error {
	baseURL := fmt.Sprintf("https://%s/admin/api/%s", shopDomain, r.apiVersion)

	// Build the app's public webhook URL (requires SHOPIFY_APP_URL env var or cloudflare tunnel)
	appURL := "https://" + shopDomain // fallback
	if u := r.resolveAppURL(); u != "" {
		appURL = u
	}
	webhookURL := appURL + "/webhooks"

	topics := []string{
		"products/create", "products/update", "products/delete",
		"orders/create", "orders/updated", "orders/paid",
		"customers/create", "customers/update",
	}

	for _, topic := range topics {
		reg := map[string]interface{}{
			"webhook": webhookRegistration{
				Topic:   topic,
				Address: webhookURL,
				Format:  "json",
			},
		}
		body, _ := json.Marshal(reg)
		req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/webhooks.json", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create request for %s: %w", topic, err)
		}
		req.Header.Set("X-Shopify-Access-Token", accessToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := r.http.Do(req)
		if err != nil {
			return fmt.Errorf("register %s: %w", topic, err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("register %s: HTTP %d: %s", topic, resp.StatusCode, string(b))
		}
	}
	return nil
}

// resolveAppURL returns the app's public URL for webhook delivery.
func (r *WebhookRegistrar) resolveAppURL() string {
	// Try common env vars used by Shopify CLI / Cloudflare tunnel
	for _, key := range []string{"SHOPIFY_APP_URL", "APP_URL", "HOST"} {
		if v := getEnv(key); v != "" {
			return v
		}
	}
	return ""
}

func getEnv(key string) string { return os.Getenv(key) }
