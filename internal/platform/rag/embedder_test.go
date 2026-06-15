package rag

import (
	"testing"
)

func TestCollectionName(t *testing.T) {
	tests := []struct {
		shopID string
		want   string
	}{
		{"a1b2c3d4-e5f6-7890-abcd-ef1234567890", "rag_a1b2c3d4-e5f"},
		{"short", "rag_short"},
		{"very-long-id-that-exceeds-12-chars", "rag_very-long-id"},
	}
	for _, tt := range tests {
		got := CollectionName(tt.shopID)
		if got != tt.want {
			t.Errorf("CollectionName(%q) = %q, want %q", tt.shopID, got, tt.want)
		}
	}
}

func TestIsValidSource(t *testing.T) {
	tests := []struct {
		source string
		valid  bool
	}{
		{"return", true},
		{"product", true},
		{"insight", true},
		{"ai_reply", true},
		{"order", false},
		{"dashboard", false},
		{"", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		if got := IsValidSource(tt.source); got != tt.valid {
			t.Errorf("IsValidSource(%q) = %v, want %v", tt.source, got, tt.valid)
		}
	}
}

func TestTTLForSource(t *testing.T) {
	if ttl := TTLForSource("return"); ttl == nil {
		t.Error("return source should have TTL")
	}
	if ttl := TTLForSource("insight"); ttl == nil {
		t.Error("insight source should have TTL")
	}
	if ttl := TTLForSource("ai_reply"); ttl == nil {
		t.Error("ai_reply source should have TTL")
	}
	if ttl := TTLForSource("product"); ttl != nil {
		t.Error("product source should be nil (never expires)")
	}
}

func TestNewOllamaEmbedder(t *testing.T) {
	e := NewOllamaEmbedder("http://localhost:11434", "nomic-embed-text", 768)
	if e == nil {
		t.Fatal("NewOllamaEmbedder returned nil")
	}
	if e.Dims() != 768 {
		t.Errorf("expected 768 dims, got %d", e.Dims())
	}
	if e.baseURL != "http://localhost:11434" {
		t.Errorf("expected baseURL, got %s", e.baseURL)
	}
}

func TestChunkFactory_MakeChunks_Whitelist(t *testing.T) {
	f := NewChunkFactory()

	// Valid source
	chunks := f.MakeChunks([]ChunkInput{{
		ShopID:   "test",
		Source:   "return",
		SourceID: "r1",
		Content:  "customer returned size M, reason: too small",
	}})
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for valid source, got %d", len(chunks))
	}

	// Invalid source → should be dropped
	chunks = f.MakeChunks([]ChunkInput{{
		ShopID:   "test",
		Source:   "dashboard",
		SourceID: "d1",
		Content:  "merchant viewed dashboard",
	}})
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for invalid source, got %d", len(chunks))
	}
}

func TestChunkFactory_MakeChunks_PIIStripping(t *testing.T) {
	f := NewChunkFactory()

	// Chinese address with street-level detail — should be stripped
	chunks := f.MakeChunks([]ChunkInput{{
		ShopID:   "test",
		Source:   "return",
		SourceID: "r1",
		Content:  "顾客 张三 (alice@example.com) 退货，M码偏小，地址 120号南京路步行街三楼",
	}})

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}

	clean := chunks[0].Content
	if contains(clean, "alice@example.com") {
		t.Error("email should be stripped")
	}
	if contains(clean, "Alice Johnson") {
		t.Error("customer name should be replaced")
	}
	// Chinese street address should be stripped
	if contains(clean, "南京路") {
		t.Error("street address should be stripped")
	}
	if !contains(clean, "退货") {
		t.Error("return content should be preserved")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstr(s, substr)
}

func searchSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
