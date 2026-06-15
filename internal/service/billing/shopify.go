// Package billing provides Shopify Billing GraphQL integration.
package billing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/JingxuanC/vela-engine/internal/config"
)

// ShopifyClient handles GraphQL communication with the Shopify Admin API.
type ShopifyClient struct {
	cfg    *config.Config
	client *http.Client
}

// NewShopifyClient creates a new Shopify GraphQL client.
func NewShopifyClient(cfg *config.Config) *ShopifyClient {
	return &ShopifyClient{
		cfg: cfg,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// SubscriptionCreateResult holds the response from Shopify's appSubscriptionCreate mutation.
type SubscriptionCreateResult struct {
	ConfirmationURL string `json:"confirmationUrl"`
	SubscriptionID  string `json:"subscription_id"`
}

// UserError represents a Shopify GraphQL user error.
type UserError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// gqlRequest executes a GraphQL mutation against the Shopify Admin API.
func (s *ShopifyClient) gqlRequest(shopDomain, accessToken, query string, variables map[string]interface{}) ([]byte, error) {
	url := s.cfg.ShopifyGraphQLURL(shopDomain)

	body := map[string]interface{}{
		"query": query,
	}
	if variables != nil {
		body["variables"] = variables
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("shopify gql: marshal body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("shopify gql: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shopify-Access-Token", accessToken)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("shopify gql: request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("shopify gql: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("shopify gql: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// gqlMutationResult parses a GraphQL response and extracts the data at the given field path.
// It returns the raw JSON of the mutation result field.
func (s *ShopifyClient) gqlMutationResult(respBody []byte, mutationField string) (json.RawMessage, error) {
	var gqlResp struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(respBody, &gqlResp); err != nil {
		return nil, fmt.Errorf("shopify gql: parse response: %w (body: %s)", err, string(respBody))
	}

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("shopify gql error: %s", gqlResp.Errors[0].Message)
	}

	result, ok := gqlResp.Data[mutationField]
	if !ok {
		return nil, fmt.Errorf("shopify gql: missing field %q in response", mutationField)
	}

	return result, nil
}

// SubscriptionMutation is the GraphQL mutation to create an app subscription.
const SubscriptionMutation = `
mutation AppSubscriptionCreate($name: String!, $returnUrl: URL!, $price: Decimal!, $trialDays: Int!) {
  appSubscriptionCreate(
    name: $name
    returnUrl: $returnUrl
    trialDays: $trialDays
    lineItems: [
      {
        plan: {
          appRecurringPricingDetails: {
            price: { amount: $price, currencyCode: USD }
          }
        }
      }
    ]
  ) {
    appSubscription {
      id
      status
    }
    confirmationUrl
    userErrors {
      field
      message
    }
  }
}
`

// CancelMutation is the GraphQL mutation to cancel an app subscription.
const CancelMutation = `
mutation AppSubscriptionCancel($id: ID!) {
  appSubscriptionCancel(id: $id) {
    appSubscription {
      id
      status
    }
    userErrors {
      field
      message
    }
  }
}
`

// ActiveSubscriptionQuery is the GraphQL query to get the active subscription.
const ActiveSubscriptionQuery = `
query {
  currentAppInstallation {
    activeSubscriptions {
      id
      name
      status
      currentPeriodEnd
      lineItems {
        plan {
          pricingDetails {
            ... on AppRecurringPricing {
              price {
                amount
                currencyCode
              }
              interval
            }
          }
        }
      }
    }
  }
}
`

// CreateSubscription calls Shopify's appSubscriptionCreate mutation.
// Returns the confirmation URL and the Shopify subscription ID.
// In dev mode (when GoEnv == "development"), returns a local confirmation URL
// that simulates the flow without a real Shopify GraphQL call.
func (s *ShopifyClient) CreateSubscription(shopDomain, accessToken, planName, planPrice, shopID string, trialDays int) (*SubscriptionCreateResult, error) {
	if s.cfg.IsDevelopment() {
		slog.Info("shopify billing: dev mode — using local mock confirmation URL",
			"shop", shopDomain, "plan", planName)
		// In dev mode, generate a confirmation URL that redirects back to the local
		// billing confirm endpoint. This simulates the Shopify billing flow locally.
		mockSubscriptionID := fmt.Sprintf("gid://shopify/AppSubscription/dev-%d", time.Now().UnixMilli())
		confirmationURL := fmt.Sprintf("%s?shop=%s&subscription_id=%s&status=accepted",
			s.cfg.BillingReturnURL, shopDomain, mockSubscriptionID)
		return &SubscriptionCreateResult{
			ConfirmationURL: confirmationURL,
			SubscriptionID:  mockSubscriptionID,
		}, nil
	}

	variables := map[string]interface{}{
		"name":      planName,
		"returnUrl": s.cfg.BillingReturnURL + "?shop=" + shopDomain,
		"price":     planPrice,
		"trialDays": trialDays,
	}

	respBody, err := s.gqlRequest(shopDomain, accessToken, SubscriptionMutation, variables)
	if err != nil {
		return nil, fmt.Errorf("shopify billing: create subscription: %w", err)
	}

	raw, err := s.gqlMutationResult(respBody, "appSubscriptionCreate")
	if err != nil {
		return nil, err
	}

	var result struct {
		AppSubscription struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"appSubscription"`
		ConfirmationURL string      `json:"confirmationUrl"`
		UserErrors      []UserError `json:"userErrors"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("shopify billing: parse result: %w", err)
	}

	if len(result.UserErrors) > 0 {
		errMsg := ""
		for _, ue := range result.UserErrors {
			errMsg += ue.Field + ": " + ue.Message + "; "
		}
		return nil, fmt.Errorf("shopify billing: user errors: %s", errMsg)
	}

	slog.Info("shopify billing: subscription created",
		"shop", shopDomain,
		"subscription_id", result.AppSubscription.ID,
		"status", result.AppSubscription.Status,
	)

	return &SubscriptionCreateResult{
		ConfirmationURL: result.ConfirmationURL,
		SubscriptionID:  result.AppSubscription.ID,
	}, nil
}

// CancelSubscription calls Shopify's appSubscriptionCancel mutation.
func (s *ShopifyClient) CancelSubscription(shopDomain, accessToken, subscriptionID string) error {
	if s.cfg.IsDevelopment() {
		slog.Info("shopify billing: dev mode — skipping cancel call to Shopify",
			"shop", shopDomain, "subscription_id", subscriptionID)
		return nil
	}

	variables := map[string]interface{}{
		"id": subscriptionID,
	}

	respBody, err := s.gqlRequest(shopDomain, accessToken, CancelMutation, variables)
	if err != nil {
		return fmt.Errorf("shopify billing: cancel subscription: %w", err)
	}

	raw, err := s.gqlMutationResult(respBody, "appSubscriptionCancel")
	if err != nil {
		return err
	}

	var result struct {
		AppSubscription struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"appSubscription"`
		UserErrors []UserError `json:"userErrors"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("shopify billing: parse cancel result: %w", err)
	}

	if len(result.UserErrors) > 0 {
		errMsg := ""
		for _, ue := range result.UserErrors {
			errMsg += ue.Field + ": " + ue.Message + "; "
		}
		return fmt.Errorf("shopify billing: cancel user errors: %s", errMsg)
	}

	slog.Info("shopify billing: subscription cancelled",
		"shop", shopDomain,
		"subscription_id", result.AppSubscription.ID,
		"status", result.AppSubscription.Status,
	)
	return nil
}

// ActiveSubscription represents a shop's active Shopify subscription.
type ActiveSubscription struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Status          string   `json:"status"`
	CurrentPeriodEnd string  `json:"currentPeriodEnd"`
	PlanPrice       *float64 `json:"plan_price,omitempty"`
}

// GetActiveSubscription queries Shopify for the currently active subscription.
func (s *ShopifyClient) GetActiveSubscription(shopDomain, accessToken string) (*ActiveSubscription, error) {
	respBody, err := s.gqlRequest(shopDomain, accessToken, ActiveSubscriptionQuery, nil)
	if err != nil {
		return nil, fmt.Errorf("shopify billing: get active subscription: %w", err)
	}

	raw, err := s.gqlMutationResult(respBody, "currentAppInstallation")
	if err != nil {
		return nil, err
	}

	var result struct {
		ActiveSubscriptions []struct {
			ID               string `json:"id"`
			Name             string `json:"name"`
			Status           string `json:"status"`
			CurrentPeriodEnd string `json:"currentPeriodEnd"`
			LineItems        []struct {
				Plan struct {
					PricingDetails *struct {
						Price struct {
							Amount       string `json:"amount"`
							CurrencyCode string `json:"currencyCode"`
						} `json:"price"`
						Interval string `json:"interval"`
					} `json:"pricingDetails"`
				} `json:"plan"`
			} `json:"lineItems"`
		} `json:"activeSubscriptions"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("shopify billing: parse active subscriptions: %w", err)
	}

	if len(result.ActiveSubscriptions) == 0 {
		return nil, nil
	}

	sub := result.ActiveSubscriptions[0]
	activeSub := &ActiveSubscription{
		ID:              sub.ID,
		Name:            sub.Name,
		Status:          sub.Status,
		CurrentPeriodEnd: sub.CurrentPeriodEnd,
	}

	if len(sub.LineItems) > 0 && sub.LineItems[0].Plan.PricingDetails != nil {
		amount := sub.LineItems[0].Plan.PricingDetails.Price.Amount
		var price float64
		if _, err := fmt.Sscanf(amount, "%f", &price); err == nil {
			activeSub.PlanPrice = &price
		}
	}

	return activeSub, nil
}
