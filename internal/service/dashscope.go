package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"log/slog"
)

const dashscopeChatURL = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
const deepseekChatURL = "https://api.deepseek.com/v1/chat/completions"
const dashscopeSTTURL = "https://dashscope.aliyuncs.com/api/v1/services/audio/asr/transcription"
const dashscopeTTSURL = "https://dashscope.aliyuncs.com/api/v1/services/audio/tts/synthesis"

// LLMProvider is the unified interface for all LLM backends (DashScope, DeepSeek, OpenAI, etc.).
// All providers use OpenAI-compatible request/response formats.
type LLMProvider interface {
	ChatCompletion(ctx context.Context, req *ChatCompletionRequest) (string, error)
	ChatCompletionStream(ctx context.Context, req *ChatCompletionRequest) (<-chan StreamChunk, error)
	ChatCompletionJSON(ctx context.Context, req *ChatCompletionRequest, target interface{}) error
	ProviderName() string
}

// LLMMetricsCollector receives LLM call metrics (optional).
type LLMMetricsCollector interface {
	RecordLLMCall(model string, durationMs float64, tokensIn, tokensOut int, hasError bool)
}

// DashScopeProvider is a unified client for LLM APIs (DashScope Qwen / DeepSeek).
// Implements LLMProvider.
type DashScopeProvider struct {
	apiKey    string
	endpoint  string
	http      *http.Client
	model     string
	metrics   LLMMetricsCollector
}

// DashScopeClient is a backward-compatible type alias for DashScopeProvider.
// Deprecated: use DashScopeProvider directly.
type DashScopeClient = DashScopeProvider

// ChatMessage represents a message in a chat completion request.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionRequest is the request body for the DashScope chat API.
type ChatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
}

// StreamChunk is a single token from a streaming response.
type StreamChunk struct {
	Token   string `json:"token"`
	Finish  bool   `json:"finish"`
	Error   string `json:"error,omitempty"`
}

// ChatCompletionResponse is the response from the DashScope chat API.
type ChatCompletionResponse struct {
	Choices []struct {
		Message ChatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// NewDashScopeClient creates a new LLM provider (DashScope by default).
// Uses DASHSCOPE_API_KEY for auth. Supports DashScope (default) and DeepSeek:
//
//	LLM_ENDPOINT=https://api.deepseek.com/v1/chat/completions
//	LLM_MODEL=deepseek-chat
//
// Deprecated: use NewDashScopeProvider for new code.
func NewDashScopeClient(apiKey string) *DashScopeProvider {
	return NewDashScopeProvider(apiKey, "", "")
}

// NewDashScopeProvider creates a new LLM provider with explicit endpoint and model.
// If endpoint or model are empty, falls back to LLM_ENDPOINT/LLM_MODEL env vars,
// then to the hardcoded defaults (dashscopeChatURL / qwen-plus).
func NewDashScopeProvider(apiKey, endpoint, model string) *DashScopeProvider {
	if endpoint == "" {
		endpoint = os.Getenv("LLM_ENDPOINT")
	}
	if endpoint == "" {
		endpoint = dashscopeChatURL
	}
	if model == "" {
		model = os.Getenv("LLM_MODEL")
	}
	if model == "" {
		model = "qwen-plus"
	}

	return &DashScopeProvider{
		apiKey:   apiKey,
		endpoint: endpoint,
		http: &http.Client{
			Timeout: 90 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:       100,
				IdleConnTimeout:    90 * time.Second,
				DisableCompression: false,
			},
		},
		model: model,
	}
}

// ProviderName returns "dashscope" as the provider identifier.
func (c *DashScopeProvider) ProviderName() string { return "dashscope" }

// SetModel overrides the default model name.
func (c *DashScopeProvider) SetModel(model string) {
	c.model = model
}

// SetMetricsCollector sets an optional metrics collector for LLM observability.
func (c *DashScopeProvider) SetMetricsCollector(m LLMMetricsCollector) {
	c.metrics = m
}

// ChatCompletion sends a chat completion request and returns the raw text response.
func (c *DashScopeProvider) ChatCompletion(ctx context.Context, req *ChatCompletionRequest) (string, error) {
	if c.apiKey == "" {
		return "", fmt.Errorf("dashscope: API key not configured")
	}

	if req.Model == "" {
		req.Model = c.model
	}

	start := time.Now()
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("dashscope: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("dashscope: create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	durationMs := float64(time.Since(start).Microseconds()) / 1000.0

	if err != nil {
		c.recordMetrics(req.Model, durationMs, 0, 0, true)
		return "", fmt.Errorf("dashscope: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.recordMetrics(req.Model, durationMs, 0, 0, true)
		return "", fmt.Errorf("dashscope: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		c.recordMetrics(req.Model, durationMs, 0, 0, true)
		return "", fmt.Errorf("dashscope: HTTP %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 500)]))
	}

	var chatResp ChatCompletionResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		c.recordMetrics(req.Model, durationMs, 0, 0, true)
		return "", fmt.Errorf("dashscope: parse response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		c.recordMetrics(req.Model, durationMs, 0, 0, true)
		return "", fmt.Errorf("dashscope: no choices in response")
	}

	c.recordMetrics(req.Model, durationMs, chatResp.Usage.PromptTokens, chatResp.Usage.CompletionTokens, false)
	return chatResp.Choices[0].Message.Content, nil
}

func (c *DashScopeProvider) recordMetrics(model string, durationMs float64, tokensIn, tokensOut int, hasError bool) {
	if c.metrics != nil {
		c.metrics.RecordLLMCall(model, durationMs, tokensIn, tokensOut, hasError)
	}
}

// ChatCompletionStream sends a streaming chat request and returns a channel of tokens.
// The caller MUST read the channel to completion to avoid goroutine leaks.
// When the stream ends, the channel emits StreamChunk{Finish: true} and closes.
func (c *DashScopeProvider) ChatCompletionStream(ctx context.Context, req *ChatCompletionRequest) (<-chan StreamChunk, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("dashscope: marshal stream request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("dashscope: create stream request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("dashscope: stream request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("dashscope: stream HTTP %d: %s", resp.StatusCode, string(body))
	}

	ch := make(chan StreamChunk, 10)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				// Parse SSE: "data: {...}\n\n"
				lines := string(buf[:n])
				for _, line := range strings.Split(lines, "\n") {
					line = strings.TrimSpace(line)
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					data := strings.TrimPrefix(line, "data: ")
					if data == "[DONE]" {
						ch <- StreamChunk{Finish: true}
						return
					}
					var sseResp struct {
						Choices []struct {
							Delta struct {
								Content string `json:"content"`
							} `json:"delta"`
							FinishReason string `json:"finish_reason"`
						} `json:"choices"`
					}
					if err := json.Unmarshal([]byte(data), &sseResp); err != nil {
						continue
					}
					for _, choice := range sseResp.Choices {
						if choice.Delta.Content != "" {
							ch <- StreamChunk{Token: choice.Delta.Content}
						}
						if choice.FinishReason == "stop" {
							ch <- StreamChunk{Finish: true}
							return
						}
					}
				}
			}
			if err != nil {
				if err != io.EOF {
					ch <- StreamChunk{Error: err.Error()}
				}
				ch <- StreamChunk{Finish: true}
				return
			}
		}
	}()
	return ch, nil
}

// ChatCompletionJSON sends a chat completion and unmarshals the JSON response into target.
// It automatically handles markdown fence removal and other common LLM JSON quirks.
func (c *DashScopeProvider) ChatCompletionJSON(ctx context.Context, req *ChatCompletionRequest, target interface{}) error {
	raw, err := c.ChatCompletion(ctx, req)
	if err != nil {
		return err
	}

	cleaned := extractJSON(raw)
	if cleaned == nil {
		slog.Warn("dashscope: could not extract JSON from response",
			"raw_prefix", raw[:min(len(raw), 200)],
		)
		return fmt.Errorf("dashscope: failed to extract JSON from response")
	}

	if err := json.Unmarshal(cleaned, target); err != nil {
		return fmt.Errorf("dashscope: unmarshal JSON response: %w", err)
	}

	return nil
}

// ExtractJSONHelper exposes JSON extraction for use by other service packages.
func ExtractJSONHelper(text string) []byte {
	return extractJSON(text)
}

// extractJSON attempts to extract a JSON object from LLM output.
// Handles markdown fences, leading/trailing text, and other common formats.
func extractJSON(text string) []byte {
	text = strings.TrimSpace(text)

	// Remove markdown fences
	text = regexp.MustCompile("(?i)^```(?:json)?\\s*").ReplaceAllString(text, "")
	text = regexp.MustCompile("(?i)\\s*```$").ReplaceAllString(text, "")

	text = strings.TrimSpace(text)

	// Try direct parse
	if json.Valid([]byte(text)) {
		return []byte(text)
	}

	// Try to find JSON object within the text using brace-depth counting
	// This is more reliable than regex for nested JSON
	braceDepth := 0
	jsonStart := -1
	for i, c := range text {
		switch c {
		case '{':
			if braceDepth == 0 {
				jsonStart = i
			}
			braceDepth++
		case '}':
			braceDepth--
			if braceDepth == 0 && jsonStart >= 0 {
				candidate := text[jsonStart : i+1]
				if json.Valid([]byte(candidate)) {
					return []byte(candidate)
				}
			}
		}
	}

	return nil
}

// ChatCompletionWithMessages is a convenience wrapper that builds a request
// from messages and returns the raw text.
func (c *DashScopeProvider) ChatCompletionWithMessages(ctx context.Context, messages []ChatMessage, temperature float64, maxTokens int) (string, error) {
	return c.ChatCompletion(ctx, &ChatCompletionRequest{
		Messages:    messages,
		Temperature: temperature,
		MaxTokens:   maxTokens,
	})
}

// Close releases resources. The HTTP client connection pool is cleaned up
// automatically by the garbage collector.
func (c *DashScopeProvider) Close() error {
	c.http.CloseIdleConnections()
	return nil
}

// HealthChecker is an optional interface for providers that support health checks.
type HealthChecker interface {
	HealthCheck(ctx context.Context) error
	CheckBalance(ctx context.Context) (*BalanceInfo, error)
}

// BalanceInfo holds account balance / credit information.
type BalanceInfo struct {
	Provider      string  `json:"provider"`
	TotalBalance  float64 `json:"total_balance"`
	UsedAmount    float64 `json:"used_amount"`
	Currency      string  `json:"currency"`
	IsLow         bool    `json:"is_low"` // true when balance is critically low
}

// HealthCheck sends a minimal chat request to verify the provider is reachable.
func (c *DashScopeProvider) HealthCheck(ctx context.Context) error {
	_, err := c.ChatCompletion(ctx, &ChatCompletionRequest{
		Model:    c.model,
		Messages: []ChatMessage{{Role: "user", Content: "ping"}},
		MaxTokens: 1,
	})
	if err != nil {
		return fmt.Errorf("health check failed for %s: %w", c.endpoint, err)
	}
	return nil
}

// CheckBalance queries the provider's billing/balance endpoint.
// For DashScope, this checks the dashscope billing API.
func (c *DashScopeProvider) CheckBalance(ctx context.Context) (*BalanceInfo, error) {
	// DashScope billing endpoint
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://dashscope.aliyuncs.com/api/v1/billing/usage", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dashscope balance check: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("dashscope balance API returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data struct {
			TotalUsage float64 `json:"total_usage"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("dashscope parse balance response: %w", err)
	}

	return &BalanceInfo{
		Provider:     "dashscope",
		TotalBalance: -1, // DashScope doesn't return balance, only usage
		UsedAmount:   result.Data.TotalUsage,
		Currency:     "CNY",
		IsLow:        false,
	}, nil
}

// Transcribe converts speech audio (base64-encoded WAV/MP3) to text using DashScope Paraformer.
func (c *DashScopeProvider) Transcribe(ctx context.Context, audioBase64 string) (string, error) {
	if c.apiKey == "" {
		return "", fmt.Errorf("dashscope: API key not configured for speech")
	}
	reqBody := map[string]interface{}{
		"model": "paraformer-v2",
		"input": map[string]string{"audio": audioBase64},
	}
	body, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, "POST", dashscopeSTTURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("dashscope stt: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("dashscope stt: HTTP %d: %s", resp.StatusCode, string(b[:min(len(b),300)]))
	}
	var result struct {
		Output struct {
			Text string `json:"text"`
		} `json:"output"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("dashscope stt parse: %w", err)
	}
	return result.Output.Text, nil
}

// Synthesize converts text to speech audio bytes using DashScope CosyVoice.
func (c *DashScopeProvider) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("dashscope: API key not configured for speech")
	}
	reqBody := map[string]interface{}{
		"model": "cosyvoice-v1",
		"input": map[string]string{"text": text},
		"parameters": map[string]interface{}{"voice": "longxiaochun", "format": "mp3"},
	}
	body, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, "POST", dashscopeTTSURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dashscope tts: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("dashscope tts: HTTP %d: %s", resp.StatusCode, string(b[:min(len(b),300)]))
	}
	return io.ReadAll(resp.Body)
}

// Ensure DashScopeProvider implements HealthChecker.
var _ HealthChecker = (*DashScopeProvider)(nil)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
