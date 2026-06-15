package rag

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ── RAGService ───────────────────────────────────────────────────────────────

// RAGService orchestrates the full RAG pipeline: embed, index, search.
// RAGMetricsCollector records RAG-specific metrics.
type RAGMetricsCollector interface {
	RecordRAGSearch()
	RecordRAGEmbedding(count int)
	RecordRAGIndexChunk(count int)
}

type RAGService struct {
	embedder          Embedder
	qdrant            *QdrantClient
	chunker           *ChunkFactory
	redis             *redis.Client
	cacheTTL          time.Duration
	createdCollections sync.Map
	metrics           RAGMetricsCollector
}

// SetMetricsCollector sets an optional metrics collector.
func (s *RAGService) SetMetricsCollector(m RAGMetricsCollector) { s.metrics = m }

// RAGServiceConfig holds initialization parameters.
type RAGServiceConfig struct {
	QdrantAddr    string
	OllamaURL     string
	EmbedModel    string
	EmbedDims     int
	RedisClient   *redis.Client
	CacheTTLMin   int
}

// NewRAGService creates a new RAGService with Ollama embedder and Qdrant backend.
func NewRAGService(cfg *RAGServiceConfig) (*RAGService, error) {
	emb := NewOllamaEmbedder(cfg.OllamaURL, cfg.EmbedModel, cfg.EmbedDims)

	qdrant := NewQdrantClient(cfg.QdrantAddr)

	// Health check Qdrant (non-fatal: Qdrant down is a degradation, not an error)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := qdrant.Health(ctx); err != nil {
		slog.Warn("rag: qdrant not reachable, will degrade to SQL-only", "error", err)
	}

	cacheTTL := time.Duration(cfg.CacheTTLMin) * time.Minute
	if cacheTTL == 0 {
		cacheTTL = 30 * time.Minute
	}

	return &RAGService{
		embedder: emb,
		qdrant:   qdrant,
		chunker:  NewChunkFactory(),
		redis:    cfg.RedisClient,
		cacheTTL: cacheTTL,
	}, nil
}

// ── Index Operations ─────────────────────────────────────────────────────────

// IndexBatch processes a batch of chunks: validate, embed, ensure collection, upsert.
// Chunks are grouped by shopID internally. Max 50 chunks per call.
func (s *RAGService) IndexBatch(ctx context.Context, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	if len(chunks) > MaxBatchSize {
		chunks = chunks[:MaxBatchSize]
	}

	// Group by shopID
	groups := make(map[string][]Chunk)
	for _, ch := range chunks {
		groups[ch.ShopID] = append(groups[ch.ShopID], ch)
	}

	for shopID, group := range groups {
		if err := s.indexGroup(ctx, shopID, group); err != nil {
			slog.Error("rag: index group failed", "shop_id", shopID, "count", len(group), "error", err)
			continue // one shop failure doesn't block others
		}
	}

	return nil
}

func (s *RAGService) indexGroup(ctx context.Context, shopID string, chunks []Chunk) error {
	// 1. Extract texts
	texts := make([]string, len(chunks))
	for i, ch := range chunks {
		texts[i] = ch.Content
	}

	// 2. Batch embed
	vectors, err := s.embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("embed: %w", err)
	}
	if len(vectors) != len(chunks) {
		return fmt.Errorf("embed returned %d vectors for %d texts", len(vectors), len(chunks))
	}

	// 3. Ensure collection exists (local cache to avoid Qdrant API call on every batch)
	if _, ok := s.createdCollections.Load(shopID); !ok {
		if err := s.qdrant.EnsureCollection(ctx, shopID, s.embedder.Dims()); err != nil {
			return fmt.Errorf("ensure collection: %w", err)
		}
		s.createdCollections.Store(shopID, true)
	}

	// 4. Upsert
	if err := s.qdrant.UpsertBatch(ctx, shopID, chunks, vectors); err != nil {
		return fmt.Errorf("upsert: %w", err)
	}

	if s.metrics != nil { s.metrics.RecordRAGIndexChunk(len(chunks)) }
	slog.Info("rag: indexed", "shop_id", shopID, "count", len(chunks))
	return nil
}

// Index is a convenience method for indexing a single chunk.
func (s *RAGService) Index(ctx context.Context, chunk Chunk) error {
	return s.IndexBatch(ctx, []Chunk{chunk})
}

// ── Tenant Operations ────────────────────────────────────────────────────────

// DeleteTenant removes all vector data for a shop. Call on app uninstall.
func (s *RAGService) DeleteTenant(ctx context.Context, shopID string) error {
	return s.qdrant.DeleteCollection(ctx, shopID)
}

// ── Accessors ────────────────────────────────────────────────────────────────

// Chunker returns the chunk factory for use by external callers (e.g., EventBus handlers).
func (s *RAGService) Chunker() *ChunkFactory { return s.chunker }

// Health returns true if Qdrant is reachable.
func (s *RAGService) Health(ctx context.Context) bool {
	return s.qdrant.Health(ctx) == nil
}
