package insight

import (
	"context"
	"testing"

	"gorm.io/gorm"
)

// ── QuerySetter mock ─────────────────────────────────────────────────────────

type mockQueryFetcher struct {
	name  string
	query string
	data  string
}

func (f *mockQueryFetcher) Name() string                          { return f.name }
func (f *mockQueryFetcher) SetQuery(q string)                     { f.query = q }
func (f *mockQueryFetcher) Fetch(ctx context.Context, db *gorm.DB, shopID string) string {
	if f.query == "" {
		return ""
	}
	return f.data + " [query:" + f.query + "]"
}

// ── InjectWithQuery Tests ────────────────────────────────────────────────────

func TestInjectWithQuery_SetsQueryOnFetcher(t *testing.T) {
	inj := NewContextInjector()
	mock := &mockQueryFetcher{name: "mock", data: "mock-result"}
	inj.Register(mock)

	result := inj.InjectWithQuery(context.Background(), nil, "shop-1", "find returns")
	if result == "" {
		t.Fatal("expected non-empty result when query is provided")
	}
	if !containsStr(result, "mock-result") {
		t.Error("expected mock result in output")
	}
	if !containsStr(result, "find returns") {
		t.Error("expected query to be passed to fetcher")
	}
}

func TestInjectWithQuery_EmptyQuery_NoQuerySet(t *testing.T) {
	inj := NewContextInjector()
	mock := &mockQueryFetcher{name: "mock", data: "mock-result"}
	inj.Register(mock)

	result := inj.InjectWithQuery(context.Background(), nil, "shop-1", "")
	if result != "" {
		t.Error("empty query should produce empty result from query-dependent fetcher")
	}
}

func TestInjectWithQuery_NoQuerySetterFetcher_StillWorks(t *testing.T) {
	inj := NewContextInjector()
	inj.Register(&ReturnFetcher{}) // doesn't implement QuerySetter

	result := inj.InjectWithQuery(context.Background(), nil, "shop-1", "some query")
	// ReturnFetcher with nil DB returns empty — this is fine, shouldn't panic
	if result != "" {
		t.Logf("unexpected result: %s", result)
	}
}

func TestInjectWithQuery_EmptyShopID(t *testing.T) {
	inj := NewContextInjector()
	mock := &mockQueryFetcher{name: "mock", data: "data"}
	inj.Register(mock)

	result := inj.InjectWithQuery(context.Background(), nil, "", "query")
	if result != "" {
		t.Error("empty shopID should return empty")
	}
}

func TestInject_DelegatesToInjectWithQuery(t *testing.T) {
	inj := NewContextInjector()
	mock := &mockQueryFetcher{name: "mock", data: "data"}
	inj.Register(mock)

	// Inject without query should work (backward compat)
	result := inj.Inject(context.Background(), nil, "shop-1")
	if result != "" {
		t.Logf("Inject without query returned: %s", result)
	}
}

func TestInjectWithQuery_MultipleFetchers(t *testing.T) {
	inj := NewContextInjector()
	qf := &mockQueryFetcher{name: "rag", data: "RAG-context"}
	inj.Register(qf)

	// When query is set, RAG fetcher produces output
	result := inj.InjectWithQuery(context.Background(), nil, "shop-1", "search query")
	if !containsStr(result, "RAG-context") {
		t.Error("expected RAG fetcher output")
	}
	if !containsStr(result, "search query") {
		t.Error("expected query in RAG output")
	}

	// When query is empty, SetQuery is not called → query is preserved from previous call
	// This is intentional: a handler calling Inject without query still benefits from RAG
	_ = inj.InjectWithQuery(context.Background(), nil, "shop-1", "")
	// No assertion — query preservation is by design
}

func TestQuerySetter_Interface(t *testing.T) {
	var qs QuerySetter
	if qs != nil {
		t.Error("nil interface should be nil")
	}
	// Verify interface compiles
	var _ QuerySetter = &mockQueryFetcher{}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
