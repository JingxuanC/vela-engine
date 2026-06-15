package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ── serialize/deserialize round-trip ─────────────────────────────────────────

func TestEmbeddingSerializeRoundTrip(t *testing.T) {
	original := []float32{0.123456, -0.987654, 0.0, 1.0, -1.0, 0.555555}
	data := serializeEmbedding(original)
	if len(data) != len(original)*4 {
		t.Fatalf("serialized length %d, expected %d", len(data), len(original)*4)
	}
	restored := deserializeEmbedding(data)
	if len(restored) != len(original) {
		t.Fatalf("length mismatch: %d vs %d", len(restored), len(original))
	}
	for i := range original {
		if restored[i] != original[i] {
			t.Errorf("[%d] %f != %f — IEEE 754 round-trip failure", i, original[i], restored[i])
		}
	}
}

func TestDeserializeEmbedding_BadLength(t *testing.T) {
	if deserializeEmbedding([]byte{1, 2, 3}) != nil {
		t.Error("non-multiple-of-4 bytes should return nil")
	}
}

// ── MergePrompt ──────────────────────────────────────────────────────────────

func TestMergePrompt_SQLOnly(t *testing.T) {
	sqlResult := "该SKU退货率 38%，尺码偏小占 70%"
	prompt := MergePrompt(sqlResult, nil)
	if prompt == "" {
		t.Fatal("MergePrompt returned empty for SQL-only")
	}
	if !containsStr(prompt, "精确数据") {
		t.Error("MergePrompt missing [精确数据] section")
	}
	if containsStr(prompt, "语义上下文") {
		t.Error("MergePrompt should not have semantic section with no hits")
	}
}

func TestMergePrompt_VectorOnly(t *testing.T) {
	hits := []ChunkHit{
		{Chunk: Chunk{Content: "同类棉质产品退货率高出行业均值40%", Source: "insight"}, Score: 0.89},
		{Chunk: Chunk{Content: "历史案例:SKU-5678添加偏大标识后退货率下降40%", Source: "return"}, Score: 0.82},
	}
	prompt := MergePrompt(nil, hits)
	if !containsStr(prompt, "语义上下文") {
		t.Error("MergePrompt missing [语义上下文] section")
	}
	if !containsStr(prompt, "insight") {
		t.Error("MergePrompt missing source annotation")
	}
	if !containsStr(prompt, "89%") {
		t.Error("MergePrompt missing relevance score")
	}
}

func TestMergePrompt_Full(t *testing.T) {
	hits := []ChunkHit{
		{Chunk: Chunk{Content: "similar case", Source: "return"}, Score: 0.9},
	}
	prompt := MergePrompt("SQL data here", hits)
	if !containsStr(prompt, "精确数据") {
		t.Error("missing SQL section")
	}
	if !containsStr(prompt, "语义上下文") {
		t.Error("missing vector section")
	}
}

// ── Mock Qdrant HTTP Server ──────────────────────────────────────────────────

func newMockQdrantServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path

		switch {
		case r.Method == http.MethodGet && path == "/health":
			w.WriteHeader(200)
			w.Write([]byte(`{"status":"ok"}`))

		case r.Method == http.MethodGet && containsStr(path, "/collections/") && !containsStr(path, "/points"):
			// getCollection
			name := path[len("/collections/"):]
			if name == "rag_nonexistent" {
				w.WriteHeader(404)
			} else {
				json.NewEncoder(w).Encode(map[string]any{
					"result": map[string]any{
						"config": map[string]any{
							"params": map[string]any{
								"vectors": map[string]any{"size": float64(768)},
							},
						},
					},
				})
			}

		case r.Method == http.MethodPut && containsStr(path, "/collections/") && !containsStr(path, "/points"):
			w.WriteHeader(200)
			w.Write([]byte(`{"result":true}`))

		case r.Method == http.MethodDelete && containsStr(path, "/collections/"):
			w.WriteHeader(200)
			w.Write([]byte(`{"result":true}`))

		case r.Method == http.MethodPut && containsStr(path, "/points"):
			w.WriteHeader(200)
			w.Write([]byte(`{"result":{"operation_id":1}}`))

		case r.Method == http.MethodPost && containsStr(path, "/search"):
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(map[string]any{
				"result": []map[string]any{
					{"id": "test-id-1", "score": 0.92, "payload": map[string]any{
						"shop_id": "test", "source": "return", "source_id": "r1",
						"content": "mock search result content", "summary": "mock summary",
					}},
					{"id": "test-id-2", "score": 0.78, "payload": map[string]any{
						"shop_id": "test", "source": "insight", "source_id": "i1",
						"content": "second mock result", "summary": "mock summary 2",
					}},
				},
			})

		default:
			w.WriteHeader(404)
		}
	}))
}

func newQdrantClientWithServer(server *httptest.Server) *QdrantClient {
	return &QdrantClient{
		baseURL: server.URL,
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

// ── QdrantClient Tests ───────────────────────────────────────────────────────

func TestQdrantHealth(t *testing.T) {
	srv := newMockQdrantServer(t)
	defer srv.Close()

	client := newQdrantClientWithServer(srv)
	if err := client.Health(context.Background()); err != nil {
		t.Errorf("health check failed: %v", err)
	}
}

func TestQdrantEnsureCollection_Create(t *testing.T) {
	srv := newMockQdrantServer(t)
	defer srv.Close()

	client := newQdrantClientWithServer(srv)
	// "rag_nonexistent" triggers 404 → should create
	err := client.EnsureCollection(context.Background(), "nonexistent", 768)
	if err != nil {
		t.Errorf("EnsureCollection failed: %v", err)
	}
}

func TestQdrantEnsureCollection_Exists(t *testing.T) {
	srv := newMockQdrantServer(t)
	defer srv.Close()

	client := newQdrantClientWithServer(srv)
	// Any existing collection → getCollection returns 200/768
	err := client.EnsureCollection(context.Background(), "existing", 768)
	if err != nil {
		t.Errorf("EnsureCollection for existing collection failed: %v", err)
	}
}

func TestQdrantDeleteCollection(t *testing.T) {
	srv := newMockQdrantServer(t)
	defer srv.Close()

	client := newQdrantClientWithServer(srv)
	err := client.DeleteCollection(context.Background(), "test-shop")
	if err != nil {
		t.Errorf("DeleteCollection failed: %v", err)
	}
}

func TestQdrantSearch(t *testing.T) {
	srv := newMockQdrantServer(t)
	defer srv.Close()

	client := newQdrantClientWithServer(srv)
	result, err := client.Search(context.Background(), "test-shop",
		[]float32{0.1, 0.2, 0.3},
		SearchOptions{TopK: 5, MinScore: 0.5},
	)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(result.Hits))
	}
	if result.Hits[0].Score != 0.92 {
		t.Errorf("expected score 0.92, got %f", result.Hits[0].Score)
	}
	if result.Hits[0].Source != "return" {
		t.Errorf("expected source 'return', got '%s'", result.Hits[0].Source)
	}
}

func TestQdrantSearch_Defaults(t *testing.T) {
	srv := newMockQdrantServer(t)
	defer srv.Close()

	client := newQdrantClientWithServer(srv)
	// TopK=0 and MinScore=0 should use defaults
	_, err := client.Search(context.Background(), "test-shop",
		[]float32{0.1}, SearchOptions{})
	if err != nil {
		t.Errorf("Search with defaults failed: %v", err)
	}
}

// ── Mock Ollama Server ───────────────────────────────────────────────────────

func newMockOllamaServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/embed" && r.Method == http.MethodPost {
			var req struct {
				Input []string `json:"input"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			// Generate mock embeddings: one 3D vector per input text
			embeddings := make([][]float32, len(req.Input))
			for i := range req.Input {
				embeddings[i] = []float32{0.1 * float32(i+1), 0.2, 0.3}
			}
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(map[string]any{"embeddings": embeddings})
			return
		}
		w.WriteHeader(404)
	}))
}

func TestOllamaEmbedder_Embed(t *testing.T) {
	srv := newMockOllamaServer(t)
	defer srv.Close()

	e := NewOllamaEmbedder(srv.URL, "nomic-embed-text", 768)
	vecs, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}
	if e.Dims() != 768 {
		t.Errorf("expected 768 dims, got %d", e.Dims())
	}
}

// ── IndexBatch with Mock Backend ─────────────────────────────────────────────

func TestIndexBatch_Integration(t *testing.T) {
	qdrantSrv := newMockQdrantServer(t)
	defer qdrantSrv.Close()
	ollamaSrv := newMockOllamaServer(t)
	defer ollamaSrv.Close()

	svc := &RAGService{
		embedder: NewOllamaEmbedder(ollamaSrv.URL, "nomic-embed-text", 768),
		qdrant:   newQdrantClientWithServer(qdrantSrv),
		chunker:  NewChunkFactory(),
		cacheTTL: 30 * time.Minute,
	}

	chunks := []Chunk{
		{ID: "id1", ShopID: "shop-a", Source: "return", SourceID: "r1", Content: "test chunk one"},
		{ID: "id2", ShopID: "shop-a", Source: "return", SourceID: "r2", Content: "test chunk two"},
		{ID: "id3", ShopID: "shop-b", Source: "insight", SourceID: "i1", Content: "another shop chunk"},
	}

	err := svc.IndexBatch(context.Background(), chunks)
	if err != nil {
		t.Fatalf("IndexBatch failed: %v", err)
	}
}

func TestIndexBatch_Empty(t *testing.T) {
	svc := &RAGService{}
	if err := svc.IndexBatch(context.Background(), nil); err != nil {
		t.Errorf("empty IndexBatch should not error: %v", err)
	}
}

// ── Service Constructor ──────────────────────────────────────────────────────

func TestNewRAGService(t *testing.T) {
	qdrantSrv := newMockQdrantServer(t)
	defer qdrantSrv.Close()

	cfg := &RAGServiceConfig{
		QdrantAddr:  qdrantSrv.URL,
		OllamaURL:   "http://localhost:9999", // not used here, just for init
		EmbedModel:  "nomic-embed-text",
		EmbedDims:   768,
		CacheTTLMin: 30,
	}

	svc, err := NewRAGService(cfg)
	if err != nil {
		t.Fatalf("NewRAGService failed: %v", err)
	}
	if svc == nil {
		t.Fatal("NewRAGService returned nil")
	}
	if svc.embedder == nil {
		t.Error("embedder not initialized")
	}
	if svc.qdrant == nil {
		t.Error("qdrant not initialized")
	}
	if svc.chunker == nil {
		t.Error("chunker not initialized")
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
