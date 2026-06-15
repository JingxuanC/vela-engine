package rag

import (
	"context"
	"testing"
)

func TestRAGFetcher_SetQuery(t *testing.T) {
	f := &RAGFetcher{}
	if f.query != "" {
		t.Error("query should be empty initially")
	}
	f.SetQuery("how to reduce returns")
	if f.query != "how to reduce returns" {
		t.Errorf("SetQuery: got %q, want %q", f.query, "how to reduce returns")
	}
}

func TestRAGFetcher_Name(t *testing.T) {
	f := &RAGFetcher{}
	if f.Name() != "rag" {
		t.Errorf("Name: got %q, want %q", f.Name(), "rag")
	}
}

func TestRAGFetcher_Fetch_NilRAG(t *testing.T) {
	f := &RAGFetcher{RAG: nil}
	f.SetQuery("test query")
	result := f.Fetch(context.Background(), nil, "test-shop")
	if result != "" {
		t.Error("Fetch with nil RAG should return empty")
	}
}

func TestRAGFetcher_Fetch_EmptyQuery(t *testing.T) {
	f := &RAGFetcher{RAG: &RAGService{}}
	result := f.Fetch(context.Background(), nil, "test-shop")
	if result != "" {
		t.Error("Fetch with empty query should return empty")
	}
}

func TestRAGFetcher_ImplementsQuerySetter(t *testing.T) {
	var qs interface{ SetQuery(string) } = &RAGFetcher{}
	if qs == nil {
		t.Fatal("RAGFetcher should satisfy SetQuery(string)")
	}
}

func TestRAGFetcher_MultipleSetQuery(t *testing.T) {
	f := &RAGFetcher{}
	f.SetQuery("first")
	f.SetQuery("second")
	if f.query != "second" {
		t.Error("consecutive SetQuery should overwrite")
	}
}

func TestRAGFetcher_Fetch_AfterSetQuery(t *testing.T) {
	// Without a real RAGService (no Qdrant/Ollama), Fetch should return empty
	// but should not panic
	f := &RAGFetcher{RAG: &RAGService{}}
	f.SetQuery("valid query")
	result := f.Fetch(context.Background(), nil, "shop")
	// No Qdrant → SearchWithText returns empty → Fetch returns empty
	if result != "" {
		t.Logf("unexpected result (may happen if qdrant is running): %s", result)
	}
}
