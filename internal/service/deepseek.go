package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// newDeepSeekProvider creates an LLM provider for DeepSeek's API.
// DeepSeek uses the same OpenAI-compatible chat completions format as DashScope,
// so we reuse DashScopeProvider configured for the DeepSeek endpoint.
func newDeepSeekProvider(apiKey, model string) LLMProvider {
	if model == "" {
		model = "deepseek-chat"
	}
	p := NewDashScopeProvider(apiKey, deepseekChatURL, model)
	return &deepSeekProvider{DashScopeProvider: p}
}

// deepSeekProvider wraps DashScopeProvider to identify as "deepseek".
type deepSeekProvider struct {
	*DashScopeProvider
}

// ProviderName returns "deepseek".
func (d *deepSeekProvider) ProviderName() string { return "deepseek" }

// HealthCheck sends a minimal ping to verify DeepSeek is reachable.
func (d *deepSeekProvider) HealthCheck(ctx context.Context) error {
	return d.DashScopeProvider.HealthCheck(ctx)
}

// CheckBalance queries DeepSeek's user balance endpoint.
func (d *deepSeekProvider) CheckBalance(ctx context.Context) (*BalanceInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://api.deepseek.com/user/balance", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.DashScopeProvider.apiKey)

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deepseek balance check: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("deepseek balance API returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		IsAvailable  bool `json:"is_available"`
		BalanceInfos []struct {
			Currency        string      `json:"currency"`
			TotalBalance    json.Number `json:"total_balance"`
			GrantedBalance  json.Number `json:"granted_balance"`
			ToppedUpBalance json.Number `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("deepseek parse balance: %w", err)
	}

	info := &BalanceInfo{
		Provider: "deepseek",
		Currency: "CNY",
	}
	if len(result.BalanceInfos) > 0 {
		b := result.BalanceInfos[0]
		tb, _ := b.TotalBalance.Float64()
		gb, _ := b.GrantedBalance.Float64()
		info.TotalBalance = tb
		info.UsedAmount = gb - tb
		if info.UsedAmount < 0 {
			info.UsedAmount = 0
		}
	}
	// Low balance: less than 1 CNY
	info.IsLow = info.TotalBalance < 1.0 && info.TotalBalance >= 0

	return info, nil
}

// Ensure deepSeekProvider implements LLMProvider and HealthChecker.
var _ LLMProvider = (*deepSeekProvider)(nil)
var _ HealthChecker = (*deepSeekProvider)(nil)

// ChatCompletion delegates to the underlying DashScopeProvider.
// Override if DeepSeek-specific behavior is needed.
func (d *deepSeekProvider) ChatCompletion(ctx context.Context, req *ChatCompletionRequest) (string, error) {
	return d.DashScopeProvider.ChatCompletion(ctx, req)
}

// ChatCompletionStream delegates to the underlying DashScopeProvider.
func (d *deepSeekProvider) ChatCompletionStream(ctx context.Context, req *ChatCompletionRequest) (<-chan StreamChunk, error) {
	return d.DashScopeProvider.ChatCompletionStream(ctx, req)
}
