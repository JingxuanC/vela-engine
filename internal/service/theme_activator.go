// Package service provides theme activation for auto-enabling the App Embed Block
// when a merchant installs Vela. No manual theme editing required.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// ThemeActivator auto-enables the Vela App Embed Block in the merchant's published theme.
type ThemeActivator struct {
	http       *http.Client
	apiVersion string
}

// NewThemeActivator creates a new ThemeActivator.
func NewThemeActivator(apiVersion string) *ThemeActivator {
	return &ThemeActivator{
		http:       &http.Client{Timeout: 30 * time.Second},
		apiVersion: apiVersion,
	}
}

// EnableAppEmbedBlock enables the "VTron AI Chat" App Embed Block in the shop's
// published theme. Called during app install/OAuth callback.
//
// Requires write_themes scope.
func (a *ThemeActivator) EnableAppEmbedBlock(ctx context.Context, shopDomain, accessToken, blockType string) error {
	// 1. Find the published theme
	themeID, err := a.getPublishedThemeID(ctx, shopDomain, accessToken)
	if err != nil {
		return fmt.Errorf("theme_activator: find published theme: %w", err)
	}
	if themeID == 0 {
		slog.Info("theme_activator: no published theme found", "shop", shopDomain)
		return nil
	}

	// 2. Get current settings_data.json
	settings, err := a.getSettingsData(ctx, shopDomain, accessToken, themeID)
	if err != nil {
		return fmt.Errorf("theme_activator: get settings: %w", err)
	}

	// 3. Enable our App Embed Block in settings
	if err := a.enableBlock(settings, blockType); err != nil {
		return fmt.Errorf("theme_activator: enable block: %w", err)
	}

	// 4. Update settings_data.json
	if err := a.updateSettingsData(ctx, shopDomain, accessToken, themeID, settings); err != nil {
		return fmt.Errorf("theme_activator: update settings: %w", err)
	}

	slog.Info("theme_activator: App Embed Block enabled", "shop", shopDomain, "theme_id", themeID, "block", blockType)
	return nil
}

// getPublishedThemeID finds the ID of the shop's published theme.
func (a *ThemeActivator) getPublishedThemeID(ctx context.Context, shopDomain, token string) (int64, error) {
	url := fmt.Sprintf("https://%s/admin/api/%s/themes.json", shopDomain, a.apiVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Shopify-Access-Token", token)

	resp, err := a.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var result struct {
		Themes []struct {
			ID    int64  `json:"id"`
			Role  string `json:"role"`
		} `json:"themes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	for _, t := range result.Themes {
		if t.Role == "main" {
			return t.ID, nil
		}
	}
	return 0, nil
}

// getSettingsData retrieves the settings_data.json asset.
func (a *ThemeActivator) getSettingsData(ctx context.Context, shopDomain, token string, themeID int64) (map[string]interface{}, error) {
	url := fmt.Sprintf("https://%s/admin/api/%s/themes/%d/assets.json?asset[key]=config/settings_data.json", shopDomain, a.apiVersion, themeID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Shopify-Access-Token", token)

	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var result struct {
		Asset struct {
			Value string `json:"value"`
		} `json:"asset"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var settings map[string]interface{}
	if err := json.Unmarshal([]byte(result.Asset.Value), &settings); err != nil {
		return nil, fmt.Errorf("parse settings_data.json: %w", err)
	}
	return settings, nil
}

// enableBlock adds/updates the App Embed Block entry in the theme settings.
func (a *ThemeActivator) enableBlock(settings map[string]interface{}, blockType string) error {
	// Navigate to current -> blocks
	current, ok := settings["current"].(map[string]interface{})
	if !ok {
		current = make(map[string]interface{})
		settings["current"] = current
	}

	blocks, ok := current["blocks"].(map[string]interface{})
	if !ok {
		blocks = make(map[string]interface{})
		current["blocks"] = blocks
	}

	// Set the block type to enabled with default settings.
	// App Embed Block keys in settings_data.json use the extension handle directly.
	blocks[blockType] = map[string]interface{}{
		"type":     blockType,
		"disabled": false,
	}

	return nil
}

// updateSettingsData writes the modified settings back to the theme.
func (a *ThemeActivator) updateSettingsData(ctx context.Context, shopDomain, token string, themeID int64, settings map[string]interface{}) error {
	url := fmt.Sprintf("https://%s/admin/api/%s/themes/%d/assets.json", shopDomain, a.apiVersion, themeID)

	valueBytes, err := json.Marshal(settings)
	if err != nil {
		return err
	}

	body := map[string]interface{}{
		"asset": map[string]interface{}{
			"key":   "config/settings_data.json",
			"value": string(valueBytes),
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("X-Shopify-Access-Token", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
