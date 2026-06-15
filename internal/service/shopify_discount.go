package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ShopifyDiscountClient creates and manages Shopify PriceRules and DiscountCodes.
type ShopifyDiscountClient struct {
	client *http.Client
}

// NewShopifyDiscountClient creates a new Shopify discount API client.
func NewShopifyDiscountClient() *ShopifyDiscountClient {
	return &ShopifyDiscountClient{client: &http.Client{Timeout: 10 * time.Second}}
}

// PriceRuleInput is the input for creating a PriceRule.
type PriceRuleInput struct {
	Title        string  `json:"title"`
	TargetType   string  `json:"target_type"`    // "line_item"
	TargetSelect string  `json:"target_selection"` // "all"
	Allocation   string  `json:"allocation_method"` // "across"
	ValueType    string  `json:"value_type"`      // "percentage"
	Value        float64 `json:"value"`            // negative: -10.0 for 10% off
	UsageLimit   int     `json:"usage_limit"`
	StartsAt     string  `json:"starts_at"`        // RFC3339
	EndsAt       string  `json:"ends_at"`          // RFC3339
}

// CreatePriceRule creates a new Shopify PriceRule via REST API.
func (c *ShopifyDiscountClient) CreatePriceRule(domain, accessToken string, input PriceRuleInput) (int64, error) {
	body := map[string]interface{}{
		"price_rule": map[string]interface{}{
			"title":              input.Title,
			"target_type":        input.TargetType,
			"target_selection":   input.TargetSelect,
			"allocation_method":  input.Allocation,
			"value_type":         input.ValueType,
			"value":              input.Value,
			"customer_selection": "all",
			"once_per_customer":  true,
			"usage_limit":        input.UsageLimit,
			"starts_at":          input.StartsAt,
			"ends_at":            input.EndsAt,
			"combines_with": map[string]interface{}{
				"product_discounts":  true,
				"order_discounts":    true,
				"shipping_discounts": true,
			},
		},
	}

	return c.postAndExtractID(fmt.Sprintf("https://%s/admin/api/2024-04/price_rules.json", domain), accessToken, body, "price_rule")
}

// CreateDiscountCode creates a discount code for an existing PriceRule.
func (c *ShopifyDiscountClient) CreateDiscountCode(domain, accessToken string, priceRuleID int64, code string) (int64, error) {
	body := map[string]interface{}{
		"discount_code": map[string]string{"code": code},
	}

	url := fmt.Sprintf("https://%s/admin/api/2024-04/price_rules/%d/discount_codes.json", domain, priceRuleID)
	return c.postAndExtractID(url, accessToken, body, "discount_code")
}

// DeletePriceRule deletes a PriceRule (used when Campaign is deleted).
func (c *ShopifyDiscountClient) DeletePriceRule(domain, accessToken string, priceRuleID int64) error {
	url := fmt.Sprintf("https://%s/admin/api/2024-04/price_rules/%d.json", domain, priceRuleID)
	req, _ := http.NewRequest("DELETE", url, nil)
	req.Header.Set("X-Shopify-Access-Token", accessToken)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode != 404 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("shopify: delete price rule: %s — %s", resp.Status, string(b))
	}
	return nil
}

func (c *ShopifyDiscountClient) postAndExtractID(url, accessToken string, body interface{}, key string) (int64, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("shopify: marshal request body: %w", err)
	}
	req, err := http.NewRequest("POST", url, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Shopify-Access-Token", accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("shopify: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("shopify: %s — %s", resp.Status, string(b))
	}

	var result map[string]struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("shopify: decode response: %w", err)
	}

	item, ok := result[key]
	if !ok {
		return 0, fmt.Errorf("shopify: response missing %s key", key)
	}
	return item.ID, nil
}
