// Package rag implements a local-first Retrieval Augmented Generation extension.
//
// All embeddings are computed locally via Ollama (nomic-embed-text).
// All vectors are stored in Qdrant with per-tenant collection isolation.
// No merchant data leaves the server during embedding or storage.
//
// Architecture:
//
//	EventBus → ChunkFactory(PII脱敏) → Asynq → Ollama Embed → Qdrant
//
// Degradation: Ollama or Qdrant failure → falls back to SQL-only.
package rag

import (
	"time"
)

// ── Chunk ────────────────────────────────────────────────────────────────────

// Chunk is a semantic text fragment ready for embedding and indexing.
type Chunk struct {
	ID         string     `json:"id"` // UUID string (from uuid.New() or Qdrant point ID)
	ShopID     string     `json:"shop_id"`
	Source     string     `json:"source"`   // return | product | insight | ai_reply
	SourceID   string     `json:"source_id"` // PG record ID
	Content    string     `json:"content"`   // 200-800 chars, PII already stripped
	Metadata   ChunkMeta  `json:"metadata"`
	ValidUntil *time.Time `json:"valid_until,omitempty"` // nil = never expires
	CreatedAt  time.Time  `json:"created_at"`
}

// ChunkMeta holds optional structured metadata carried alongside the vector.
type ChunkMeta struct {
	ProductID   string `json:"product_id,omitempty"`
	ProductName string `json:"product_name,omitempty"`
	ReturnID    string `json:"return_id,omitempty"`
	Severity    string `json:"severity,omitempty"` // critical | warning | info
	Summary     string `json:"summary,omitempty"`
}

// ── Search ───────────────────────────────────────────────────────────────────

// SearchOptions controls semantic search behavior.
type SearchOptions struct {
	TopK     int      `json:"top_k"`
	Sources  []string `json:"sources,omitempty"` // empty = all sources
	MinScore float64  `json:"min_score"`
}

// ChunkHit is a search result with similarity score.
type ChunkHit struct {
	Chunk
	Score float64 `json:"score"` // cosine similarity 0-1
}

// SearchResult bundles a set of hits.
type SearchResult struct {
	Hits  []ChunkHit `json:"hits"`
	Total int        `json:"total"`
}

// ── Hybrid ───────────────────────────────────────────────────────────────────

// HybridSearchRequest combines SQL results with a vector search query.
type HybridSearchRequest struct {
	ShopID     string   `json:"shop_id"`
	Query      string   `json:"query"`
	SQLResult  any      `json:"sql_result"`  // raw data from PG, any struct
	VectorTopK int      `json:"vector_top_k"` // default 5
	Sources    []string `json:"sources,omitempty"`
}

// HybridSearchResult merges SQL precision with semantic recall.
type HybridSearchResult struct {
	SQLHits     any        `json:"sql_hits"`     // PG query results (pass-through)
	VectorHits  []ChunkHit `json:"vector_hits"`  // Qdrant search results
	MergePrompt string     `json:"merge_prompt"` // combined context for LLM injection
}

// ── Embedding ────────────────────────────────────────────────────────────────

// Embedding is a slice of float32 representing a vector.
type Embedding []float32

// ── Constants ────────────────────────────────────────────────────────────────

const (
	CollectionPrefix = "rag_"
	DefaultDims      = 768
	DefaultTopK      = 10
	DefaultMinScore  = 0.5
	DefaultModel     = "nomic-embed-text"
	MaxBatchSize     = 50 // max chunks per Asynq task / Ollama batch call
)

// CollectionName returns the Qdrant collection name for a given shop ID.
func CollectionName(shopID string) string {
	if len(shopID) > 12 {
		shopID = shopID[:12]
	}
	return CollectionPrefix + shopID
}

// ── Source whitelist ─────────────────────────────────────────────────────────

var ValidSources = map[string]bool{
	"return":   true,
	"product":  true,
	"insight":  true,
	"ai_reply": true,
	"review":   true, // customer review content from judgeme/loox/etc.
}

// IsValidSource returns true if the source type is in the indexing whitelist.
func IsValidSource(source string) bool {
	return ValidSources[source]
}

// TTLForSource returns the default TTL for a chunk based on its source type.
func TTLForSource(source string) *time.Time {
	var t time.Time
	switch source {
	case "return":
		t = time.Now().Add(90 * 24 * time.Hour)
	case "insight":
		t = time.Now().Add(30 * 24 * time.Hour)
	case "ai_reply":
		t = time.Now().Add(180 * 24 * time.Hour)
	default:
		return nil // product: never expires
	}
	return &t
}
