// Package handler provides the merchant observability dashboard API.
// It forwards queries to Prometheus and Loki so merchants can monitor
// their store's service health without exposing Grafana ports.
package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/middleware"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// ── Types ──────────────────────────────────────────────────────────────────────

// ObservabilityHandler proxies Prometheus (metrics) and Loki (logs) queries.
type ObservabilityHandler struct {
	prometheusURL string
	lokiURL       string
	httpClient    *http.Client
}

// NewObservabilityHandler creates a new ObservabilityHandler.
// prometheusURL e.g. "http://localhost:9090"
// lokiURL       e.g. "http://localhost:3100"
func NewObservabilityHandler(prometheusURL, lokiURL string) *ObservabilityHandler {
	return &ObservabilityHandler{
		prometheusURL: prometheusURL,
		lokiURL:       lokiURL,
		httpClient:    &http.Client{Timeout: 15 * time.Second},
	}
}

// ── Response DTOs ─────────────────────────────────────────────────────────────

// ObsSummaryResponse is returned by GET /api/admin/observability/summary.
type ObsSummaryResponse struct {
	Success bool          `json:"success"`
	Sync    ObsSyncStats  `json:"sync"`
	AI      ObsAIStats    `json:"ai"`
	Errors  []ObsLogEntry `json:"errors"`
	Uptime  string        `json:"uptime"`
}

// ObsSyncStats holds sync pipeline statistics.
type ObsSyncStats struct {
	Products  int64 `json:"products"`
	Orders    int64 `json:"orders"`
	Customers int64 `json:"customers"`
}

// ObsAIStats holds AI/LLM call statistics.
type ObsAIStats struct {
	Calls  int64 `json:"calls"`
	Tokens int64 `json:"tokens"`
}

// ObsLogEntry is a structured log entry.
type ObsLogEntry struct {
	Timestamp string `json:"timestamp"`
	Module    string `json:"module"`
	Message   string `json:"message"`
	Level     string `json:"level"`
}

// ObsLogsResponse is returned by GET /api/admin/observability/logs.
type ObsLogsResponse struct {
	Success bool          `json:"success"`
	Logs    []ObsLogEntry `json:"logs"`
	Total   int           `json:"total"`
}

// ── Prometheus Helpers ────────────────────────────────────────────────────────

// promQuery runs an instant query against the Prometheus API.
func (h *ObservabilityHandler) promQuery(query string) (float64, error) {
	if h.prometheusURL == "" {
		return 0, fmt.Errorf("prometheus URL not configured")
	}

	endpoint := fmt.Sprintf("%s/api/v1/query", h.prometheusURL)
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return 0, err
	}

	q := req.URL.Query()
	q.Set("query", query)
	req.URL.RawQuery = q.Encode()

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("prometheus query: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	// Prometheus API response: {"status":"success","data":{"resultType":"vector","result":[...]}}
	var pr promResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return 0, fmt.Errorf("prometheus parse: %w", err)
	}

	if pr.Status != "success" || len(pr.Data.Result) == 0 {
		return 0, nil
	}

	// Sum all result values
	var total float64
	for _, r := range pr.Data.Result {
		if len(r.Value) >= 2 {
			if v, err := parsePromValue(r.Value[1]); err == nil {
				total += v
			}
		}
	}
	return total, nil
}

type promResponse struct {
	Status string   `json:"status"`
	Data   promData `json:"data"`
}

type promData struct {
	ResultType string       `json:"resultType"`
	Result     []promResult `json:"result"`
}

type promResult struct {
	Metric map[string]string `json:"metric"`
	Value  []interface{}     `json:"value"`
}

func parsePromValue(v interface{}) (float64, error) {
	switch val := v.(type) {
	case float64:
		return val, nil
	case string:
		return strconv.ParseFloat(val, 64)
	default:
		return 0, fmt.Errorf("unexpected prometheus value type: %T", v)
	}
}

// ── Loki Helpers ──────────────────────────────────────────────────────────────

// lokiQueryRange queries the Loki range API and returns log entries.
func (h *ObservabilityHandler) lokiQueryRange(shopID, level string, limit int) ([]ObsLogEntry, error) {
	if h.lokiURL == "" {
		return nil, fmt.Errorf("loki URL not configured")
	}

	endpoint := fmt.Sprintf("%s/loki/api/v1/query_range", h.lokiURL)

	// Build LogQL query — validate and sanitize shopID first
	if _, err := uuid.Parse(shopID); err != nil {
		return nil, fmt.Errorf("invalid shop_id format")
	}
	shopID = strings.ReplaceAll(shopID, `"`, `"`)
	query := fmt.Sprintf(`{shop_id="%s"}`, shopID)
	if level != "" {
		level = strings.ReplaceAll(level, `"`, `"`)
		query += fmt.Sprintf(` |= "%s"`, level)
	}

	now := time.Now()
	start := now.Add(-24 * time.Hour)

	form := url.Values{}
	form.Set("query", query)
	form.Set("start", fmt.Sprintf("%d", start.UnixNano()))
	form.Set("end", fmt.Sprintf("%d", now.UnixNano()))
	form.Set("limit", strconv.Itoa(limit))
	form.Set("direction", "backward")

	fullURL := endpoint + "?" + form.Encode()
	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loki query: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var lr lokiRangeResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, fmt.Errorf("loki parse: %w", err)
	}

	var entries []ObsLogEntry
	for _, stream := range lr.Data.Result {
		for _, entry := range stream.Values {
			// entry is [timestamp_ns, log_line]
			if len(entry) < 2 {
				continue
			}
			ts := ""
			if tsFloat, ok := entry[0].(float64); ok {
				ts = time.Unix(0, int64(tsFloat)).Format(time.RFC3339)
			}
			msg := ""
			if msgStr, ok := entry[1].(string); ok {
				msg = msgStr
			}

			module := "unknown"
			if v, ok := stream.Stream["module"]; ok {
				module = v
			}

			logLevel := "info"
			if v, ok := stream.Stream["level"]; ok {
				logLevel = v
			}

			entries = append(entries, ObsLogEntry{
				Timestamp: ts,
				Module:    module,
				Message:   msg,
				Level:     logLevel,
			})
		}
	}
	return entries, nil
}

type lokiRangeResponse struct {
	Status string   `json:"status"`
	Data   lokiData `json:"data"`
}

type lokiData struct {
	ResultType string       `json:"resultType"`
	Result     []lokiStream `json:"result"`
}

type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][]interface{}   `json:"values"`
}

// ── HTTP Handlers ──────────────────────────────────────────────────────────────

// Summary returns a merchant's observability summary.
// GET /api/admin/observability/summary?shop_id=xxx
func (h *ObservabilityHandler) Summary(w http.ResponseWriter, r *http.Request) {
	shopID := middleware.ShopIDFromContext(r.Context())
	if shopID == "" {
		shopID = r.URL.Query().Get("shop_id")
	}
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	resp := ObsSummaryResponse{
		Success: true,
		Sync:    ObsSyncStats{},
		AI:      ObsAIStats{},
		Errors:  []ObsLogEntry{},
		Uptime:  "unknown",
	}

	// Query Prometheus for sync metrics
	// vela_sync_total{pipeline="products"}
	if v, err := h.promQuery(fmt.Sprintf(`vela_sync_total{pipeline="products"}`)); err == nil {
		resp.Sync.Products = int64(v)
	} else {
		slog.Warn("observability: prometheus sync/products query failed", "error", err)
	}
	if v, err := h.promQuery(fmt.Sprintf(`vela_sync_total{pipeline="orders"}`)); err == nil {
		resp.Sync.Orders = int64(v)
	} else {
		slog.Warn("observability: prometheus sync/orders query failed", "error", err)
	}
	if v, err := h.promQuery(fmt.Sprintf(`vela_sync_total{pipeline="customers"}`)); err == nil {
		resp.Sync.Customers = int64(v)
	} else {
		slog.Warn("observability: prometheus sync/customers query failed", "error", err)
	}

	// Query Prometheus for LLM metrics
	if v, err := h.promQuery(`vela_llm_calls_total`); err == nil {
		resp.AI.Calls = int64(v)
	} else {
		slog.Warn("observability: prometheus llm_calls query failed", "error", err)
	}
	if v, err := h.promQuery(`vela_llm_tokens_input_total + vela_llm_tokens_output_total`); err == nil {
		resp.AI.Tokens = int64(v)
	} else {
		// Try individual queries
		in, _ := h.promQuery(`vela_llm_tokens_input_total`)
		out, _ := h.promQuery(`vela_llm_tokens_output_total`)
		resp.AI.Tokens = int64(in + out)
	}

	// Query Loki for recent error logs (last 24h)
	if entries, err := h.lokiQueryRange(shopID, "error", 20); err == nil {
		resp.Errors = entries
	} else {
		slog.Warn("observability: loki query failed", "error", err)
	}

	// Uptime: use vela_requests_total as a proxy for uptime
	// (if > 0, the server has been handling requests)
	if v, err := h.promQuery(`vela_requests_total`); err == nil && v > 0 {
		// Try to get process start time if available
		resp.Uptime = "active"
	}

	httputil.WriteOK(w, resp)
}

// Logs returns structured logs for a merchant.
// GET /api/admin/observability/logs?shop_id=xxx&level=error&limit=20
func (h *ObservabilityHandler) Logs(w http.ResponseWriter, r *http.Request) {
	shopID := middleware.ShopIDFromContext(r.Context())
	if shopID == "" {
		shopID = r.URL.Query().Get("shop_id")
	}
	if shopID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	level := r.URL.Query().Get("level")
	if level == "" {
		level = "error"
	}

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}

	entries, err := h.lokiQueryRange(shopID, level, limit)
	if err != nil {
		slog.Error("observability: logs query failed", "shop_id", shopID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "failed to query logs")
		return
	}

	httputil.WriteOK(w, ObsLogsResponse{
		Success: true,
		Logs:    entries,
		Total:   len(entries),
	})
}
