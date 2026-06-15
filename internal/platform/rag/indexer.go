package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// ── QdrantClient ─────────────────────────────────────────────────────────────

// QdrantClient wraps the Qdrant REST API for collection management, upsert, and search.
type QdrantClient struct {
	baseURL string
	http    *http.Client
}

// NewQdrantClient creates a QdrantClient targeting the given REST API address.
func NewQdrantClient(addr string) *QdrantClient {
	return &QdrantClient{
		baseURL: addr,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Health checks Qdrant connectivity.
func (c *QdrantClient) Health(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/healthz", nil)
	if err != nil {
		return fmt.Errorf("rag: qdrant health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rag: qdrant unhealthy: %d", resp.StatusCode)
	}
	return nil
}

// ── Collection Management ────────────────────────────────────────────────────

// EnsureCollection creates the collection for a shop if it doesn't exist.
// Returns an error if the collection exists with mismatched dimensions.
func (c *QdrantClient) EnsureCollection(ctx context.Context, shopID string, dims int) error {
	name := CollectionName(shopID)

	// Check if already exists
	exists, existingDims, err := c.getCollection(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		if existingDims != dims {
			return fmt.Errorf("rag: collection %s exists with dims %d, requested %d", name, existingDims, dims)
		}
		return nil
	}

	// Create collection
	payload := map[string]any{
		"vectors": map[string]any{
			"size":     dims,
			"distance": "Cosine",
		},
	}
	resp, err := c.doJSON(ctx, http.MethodPut, "/collections/"+name, payload)
	if err != nil {
		return fmt.Errorf("rag: create collection %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("rag: create collection %s returned %d: %s", name, resp.StatusCode, string(body))
	}

	slog.Info("rag: collection created", "name", name, "dims", dims)
	return nil
}

// getCollection checks if a collection exists and returns its vector dimensions.
func (c *QdrantClient) getCollection(ctx context.Context, name string) (exists bool, dims int, err error) {
	resp, err := c.do(ctx, http.MethodGet, "/collections/"+name, nil)
	if err != nil {
		return false, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, 0, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, 0, fmt.Errorf("rag: get collection %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Result struct {
			Config struct {
				Params struct {
					Vectors struct {
						Size int `json:"size"`
					} `json:"vectors"`
				} `json:"params"`
			} `json:"config"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return true, 0, fmt.Errorf("rag: decode collection info: %w", err)
	}

	return true, result.Result.Config.Params.Vectors.Size, nil
}

// DeleteCollection removes all data for a shop (GDPR / uninstall).
func (c *QdrantClient) DeleteCollection(ctx context.Context, shopID string) error {
	name := CollectionName(shopID)
	resp, err := c.do(ctx, http.MethodDelete, "/collections/"+name, nil)
	if err != nil {
		return fmt.Errorf("rag: delete collection %s: %w", name, err)
	}
	defer resp.Body.Close()
	slog.Info("rag: collection deleted", "name", name)
	return nil
}

// ── Points ───────────────────────────────────────────────────────────────────

type qdrantPoint struct {
	ID      string         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload"`
}

// UpsertBatch writes multiple points to a shop's collection.
func (c *QdrantClient) UpsertBatch(ctx context.Context, shopID string, chunks []Chunk, vectors [][]float32) error {
	if len(chunks) != len(vectors) {
		return fmt.Errorf("rag: chunks and vectors length mismatch: %d vs %d", len(chunks), len(vectors))
	}
	if len(chunks) == 0 {
		return nil
	}

	name := CollectionName(shopID)
	points := make([]qdrantPoint, len(chunks))
	for i, ch := range chunks {
		// Prefer metadata summary as payload summary for display
		summary := ch.Metadata.Summary
		if summary == "" && len(ch.Content) > 200 {
			summary = ch.Content[:200]
		} else if summary == "" {
			summary = ch.Content
		}

		payloadMap := map[string]any{
			"shop_id":    ch.ShopID,
			"source":     ch.Source,
			"source_id":  ch.SourceID,
			"content":    ch.Content,
			"summary":    summary,
		}
		if ch.Metadata.ProductID != "" {
			payloadMap["product_id"] = ch.Metadata.ProductID
		}
		if ch.Metadata.ProductName != "" {
			payloadMap["product_name"] = ch.Metadata.ProductName
		}
		if ch.Metadata.Severity != "" {
			payloadMap["severity"] = ch.Metadata.Severity
		}

		points[i] = qdrantPoint{
			ID:      ch.ID,
			Vector:  vectors[i],
			Payload: payloadMap,
		}
	}

	payload := map[string]any{
		"points": points,
	}

	resp, err := c.doJSON(ctx, http.MethodPut, "/collections/"+name+"/points?wait=true", payload)
	if err != nil {
		return fmt.Errorf("rag: upsert: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("rag: upsert returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// ── Search ───────────────────────────────────────────────────────────────────

// Search performs semantic search in a shop's collection.
func (c *QdrantClient) Search(ctx context.Context, shopID string, vector []float32, opts SearchOptions) (*SearchResult, error) {
	name := CollectionName(shopID)
	if opts.TopK == 0 {
		opts.TopK = DefaultTopK
	}
	if opts.MinScore == 0 {
		opts.MinScore = DefaultMinScore
	}

	// Build search payload
	reqPayload := map[string]any{
		"vector":      vector,
		"limit":       opts.TopK,
		"score_threshold": opts.MinScore,
		"with_payload": true,
	}

	// Add source filter if specified
	if len(opts.Sources) > 0 {
		reqPayload["filter"] = map[string]any{
			"must": []map[string]any{
				{
					"key":   "source",
					"match": map[string]any{"any": opts.Sources},
				},
			},
		}
	}

	resp, err := c.doJSON(ctx, http.MethodPost, "/collections/"+name+"/points/search", reqPayload)
	if err != nil {
		return nil, fmt.Errorf("rag: search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("rag: search returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Result []struct {
			ID      string         `json:"id"`
			Score   float64        `json:"score"`
			Payload map[string]any `json:"payload"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("rag: decode search result: %w", err)
	}

	hits := make([]ChunkHit, len(result.Result))
	for i, r := range result.Result {
		chunk := Chunk{
			ID:       tryParseUUID(r.ID),
			ShopID:   strFromPayload(r.Payload, "shop_id"),
			Source:   strFromPayload(r.Payload, "source"),
			SourceID: strFromPayload(r.Payload, "source_id"),
			Content:  strFromPayload(r.Payload, "content"),
			Metadata: ChunkMeta{
				ProductID:   strFromPayload(r.Payload, "product_id"),
				ProductName: strFromPayload(r.Payload, "product_name"),
				Summary:     strFromPayload(r.Payload, "summary"),
				Severity:    strFromPayload(r.Payload, "severity"),
			},
		}
		hits[i] = ChunkHit{Chunk: chunk, Score: r.Score}
	}

	return &SearchResult{Hits: hits, Total: len(hits)}, nil
}

// ── HTTP Helpers ─────────────────────────────────────────────────────────────

func (c *QdrantClient) doJSON(ctx context.Context, method, path string, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("rag: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rag: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.http.Do(req)
}

func (c *QdrantClient) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("rag: request: %w", err)
	}
	return c.http.Do(req)
}

// ── Payload Helpers ──────────────────────────────────────────────────────────

func strFromPayload(p map[string]any, key string) string {
	if v, ok := p[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func tryParseUUID(s string) string {
	// Validate it's a UUID format; if not, return as-is
	if len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-' {
		return s
	}
	return s
}
