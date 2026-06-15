package rag

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// ── RAGFetcher — ContextInjector Adapter ─────────────────────────────────────

// RAGFetcher adapts the RAG service to the insight.DataFetcher interface.
// It implements QuerySetter so ContextInjector.InjectWithQuery can set the user query
// before calling Fetch, enabling semantic search in the AI inference path.
//
// Source filtering: call SetSources() before Fetch to limit results to specific
// source types (e.g. ["product","review"] for content generation, ["return","review"]
// for size recommendations). nil = all sources.
//
// Usage:
//
//	srv.injector.Register(&rag.RAGFetcher{RAG: ragSvc})
//	context := srv.injector.InjectWithQuery(ctx, db, shopID, userQuery)
type RAGFetcher struct {
	RAG     *RAGService
	query   string   // set via SetQuery before Fetch
	sources []string // source filter; nil = all sources
}

// SetQuery stores the user query for RAG search. Implements insight.QuerySetter.
func (f *RAGFetcher) SetQuery(q string) { f.query = q }

// SetSources limits the RAG search to specific source types. nil or empty = all.
func (f *RAGFetcher) SetSources(s []string) { f.sources = s }

// Name returns a human-readable name for debugging.
func (f *RAGFetcher) Name() string { return "rag" }

// Fetch retrieves semantic context and formats it as a prompt snippet.
// Uses the query set via SetQuery() before InjectWithQuery() calls this.
func (f *RAGFetcher) Fetch(ctx context.Context, db *gorm.DB, shopID string) string {
	if f.RAG == nil || f.query == "" {
		return ""
	}

	result, err := f.RAG.SearchWithText(ctx, shopID, f.query, SearchOptions{
		TopK:     5,
		MinScore: 0.5,
		Sources:  f.sources,
	})
	if err != nil || len(result.Hits) == 0 {
		return "" // silent degradation
	}

	var sb strings.Builder
	sb.WriteString("[语义上下文 — 来自 AI 知识库]\n")
	for i, hit := range result.Hits {
		content := hit.Content
		if len(content) > 300 {
			content = content[:300] + "..."
		}
		sb.WriteString(fmt.Sprintf("%d. %s (source: %s, relevance: %.0f%%)\n",
			i+1, content, hit.Source, hit.Score*100))
	}
	return sb.String()
}
