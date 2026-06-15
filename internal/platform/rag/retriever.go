package rag

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"strings"
)

// ── Search Methods ───────────────────────────────────────────────────────────

// SearchWithText performs a semantic search by embedding the query text first.
// Query embeddings are cached in Redis (TTL 30min by default).
// Returns an empty SearchResult (not an error) on degradation.
func (s *RAGService) SearchWithText(ctx context.Context, shopID, query string, opts SearchOptions) (*SearchResult, error) {
	if s.embedder == nil || s.qdrant == nil {
		return &SearchResult{}, nil // degradation: no backend available
	}
	if opts.TopK == 0 {
		opts.TopK = DefaultTopK
	}
	if opts.MinScore == 0 {
		opts.MinScore = DefaultMinScore
	}

	// Record search metric
	if s.metrics != nil { s.metrics.RecordRAGSearch() }

	// Try query embedding cache
	cacheKey := embedCacheKey(query)
	vector, err := s.getCachedEmbedding(ctx, cacheKey)
	if err != nil || vector == nil {
		// Compute embedding
		vectors, embedErr := s.embedder.Embed(ctx, []string{query})
		if embedErr != nil {
			slog.Warn("rag: embed failed, returning empty search result", "query", truncateText(query, 50), "error", embedErr)
			return &SearchResult{}, nil // degradation: return empty, not error
		}
		if len(vectors) == 0 {
			return &SearchResult{}, nil
		}
		vector = vectors[0]

		// Cache embedding
		_ = s.setCachedEmbedding(ctx, cacheKey, vector)
	}

	// Search Qdrant
	result, err := s.qdrant.Search(ctx, shopID, vector, opts)
		if err != nil && strings.Contains(err.Error(), "doesn't exist") {
			_ = s.qdrant.EnsureCollection(ctx, shopID, s.embedder.Dims())
			result, err = s.qdrant.Search(ctx, shopID, vector, opts)
		}
	if err != nil {
		slog.Warn("rag: qdrant search failed, returning empty", "shop_id", shopID, "error", err)
		return &SearchResult{}, nil // degradation
	}

	return result, nil
}

// ── Hybrid Search ────────────────────────────────────────────────────────────

// MergePrompt builds a combined LLM context from SQL data and semantic chunks.
func MergePrompt(sqlResult any, vectorHits []ChunkHit) string {
	var sb strings.Builder

	// SQL data section
	if sqlResult != nil {
		sb.WriteString("[精确数据 — 来自数据库]\n")
		sb.WriteString(fmt.Sprintf("%v\n\n", sqlResult))
	}

	// Semantic context section
	if len(vectorHits) > 0 {
		sb.WriteString("[语义上下文 — 来自 AI 知识库]\n")
		for i, hit := range vectorHits {
			content := hit.Content
			if len(content) > 300 {
				content = content[:300] + "..."
			}
			sb.WriteString(fmt.Sprintf("%d. %s (source: %s, relevance: %.0f%%)\n",
				i+1, content, hit.Source, hit.Score*100))
		}
	}

	return sb.String()
}

// ── Embedding Cache ──────────────────────────────────────────────────────────

func embedCacheKey(query string) string {
	hash := md5.Sum([]byte(query))
	return fmt.Sprintf("rag:query_emb:%x", hash)
}

func (s *RAGService) getCachedEmbedding(ctx context.Context, key string) ([]float32, error) {
	if s.redis == nil {
		return nil, nil
	}
	data, err := s.redis.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	return deserializeEmbedding(data), nil
}

func (s *RAGService) setCachedEmbedding(ctx context.Context, key string, vec []float32) error {
	if s.redis == nil {
		return nil
	}
	return s.redis.Set(ctx, key, serializeEmbedding(vec), s.cacheTTL).Err()
}

// serializeEmbedding packs []float32 losslessly using IEEE 754 binary encoding.
func serializeEmbedding(vec []float32) []byte {
	b := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(v))
	}
	return b
}

// deserializeEmbedding unpacks raw bytes back into []float32.
func deserializeEmbedding(data []byte) []float32 {
	if len(data)%4 != 0 {
		return nil
	}
	vec := make([]float32, len(data)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return vec
}

// truncateText cuts text for log messages.
func truncateText(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
