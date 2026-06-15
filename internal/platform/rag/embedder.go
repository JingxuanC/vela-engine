package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ── Embedder Interface ───────────────────────────────────────────────────────

// Embedder converts texts into vectors. All implementations share this interface.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dims() int
}

// ── Ollama Embedder ──────────────────────────────────────────────────────────

// OllamaEmbedder calls the local Ollama HTTP API for embeddings.
type OllamaEmbedder struct {
	baseURL string
	model   string
	dims    int
	http    *http.Client
}

// NewOllamaEmbedder creates an OllamaEmbedder with the given model and base URL.
func NewOllamaEmbedder(baseURL, model string, dims int) *OllamaEmbedder {
	return &OllamaEmbedder{
		baseURL: baseURL,
		model:   model,
		dims:    dims,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Embed sends texts to Ollama POST /api/embed and returns the resulting vectors.
func (e *OllamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	type embedReq struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	type embedResp struct {
		Embeddings [][]float32 `json:"embeddings"`
	}

	body := embedReq{Model: e.model, Input: texts}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("rag: marshal embed request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.baseURL+"/api/embed", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("rag: create embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rag: ollama embed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("rag: ollama embed returned %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result embedResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("rag: decode embed response: %w", err)
	}

	return result.Embeddings, nil
}

// Dims returns the expected embedding dimension for this model.
func (e *OllamaEmbedder) Dims() int { return e.dims }
