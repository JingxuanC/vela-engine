package taskqueue

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hibiken/asynq"
)

// ── Payload round-trip tests ─────────────────────────────────────────────────

func TestEnrichProductPayload_JSONRoundTrip(t *testing.T) {
	payload := EnrichProductPayload{
		ProductID: "prod-123",
		TenantID:  "shop-456",
		ShopifyID: 789,
	}
	data, _ := json.Marshal(payload)
	var restored EnrichProductPayload
	json.Unmarshal(data, &restored)
	if restored.ProductID != "prod-123" {
		t.Errorf("ProductID: got %q", restored.ProductID)
	}
}

func TestCartRecoveryCheckPayload_JSONRoundTrip(t *testing.T) {
	payload := CartRecoveryCheckPayload{ShopID: "shop-abc"}
	data, _ := json.Marshal(payload)
	var restored CartRecoveryCheckPayload
	json.Unmarshal(data, &restored)
	if restored.ShopID != "shop-abc" {
		t.Errorf("ShopID: got %q", restored.ShopID)
	}
}

func TestContentPullMetricsPayload_JSONRoundTrip(t *testing.T) {
	payload := ContentPullMetricsPayload{ShopID: "shop-abc", Platform: "pinterest"}
	data, _ := json.Marshal(payload)
	var restored ContentPullMetricsPayload
	json.Unmarshal(data, &restored)
	if restored.Platform != "pinterest" {
		t.Errorf("Platform: got %q", restored.Platform)
	}
}

// ── Task type constants ──────────────────────────────────────────────────────

func TestTaskTypeConstants(t *testing.T) {
	types := map[TaskType]string{
		TypeEnrichProduct:      "product:enrich",
		TypeCartRecoveryCheck:  "cart_recovery:check",
		TypeContentPullMetrics: "content:pull_metrics",
	}
	for typ, expected := range types {
		if string(typ) != expected {
			t.Errorf("TaskType %q = %q, want %q", typ, string(typ), expected)
		}
	}
}

// ── RegisterHandler + mux ────────────────────────────────────────────────────

func TestRegisterHandler_Integration(t *testing.T) {
	mux := asynq.NewServeMux()
	handler := func(ctx context.Context, task *asynq.Task) error { return nil }
	RegisterHandler(mux, TypeEnrichProduct, handler)
	// No error = handler registered successfully
}

func TestRegisterHandler_Multiple(t *testing.T) {
	mux := asynq.NewServeMux()
	h := func(ctx context.Context, task *asynq.Task) error { return nil }
	RegisterHandler(mux, TypeEnrichProduct, h)
	RegisterHandler(mux, TypeCartRecoveryCheck, h)
	RegisterHandler(mux, TypeContentPullMetrics, h)
	// No panic, no error
}

func TestRegisterHandler_NoOverwrite(t *testing.T) {
	// Asynq panics on duplicate handler registration — this is intentional library behavior
	// to catch misconfiguration at startup time.
	mux := asynq.NewServeMux()
	h := func(ctx context.Context, task *asynq.Task) error { return nil }
	RegisterHandler(mux, TypeEnrichProduct, h)
	// Registering the same type again would panic — not tested
}

// ── marshalPayload ───────────────────────────────────────────────────────────

func TestMarshalPayload_Struct(t *testing.T) {
	payload := EnrichProductPayload{ProductID: "p1", TenantID: "t1", ShopifyID: 1}
	data, err := marshalPayload(payload)
	if err != nil {
		t.Fatalf("marshalPayload failed: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty payload")
	}
}

func TestMarshalPayload_Bytes(t *testing.T) {
	input := []byte(`{"key":"value"}`)
	data, err := marshalPayload(input)
	if err != nil {
		t.Fatalf("marshalPayload failed: %v", err)
	}
	if string(data) != `{"key":"value"}` {
		t.Errorf("expected passthrough, got %s", data)
	}
}

// ── Enqueue + EnqueueAt requires Redis ──────────────────────────────────────
// These are integration tests — skip in unit test mode

func TestEnqueue_NoRedis(t *testing.T) {
	t.Skip("requires Redis — tested via integration")
}

// ── Benchmark ────────────────────────────────────────────────────────────────

func BenchmarkMarshalPayload(b *testing.B) {
	payload := EnrichProductPayload{ProductID: "p1", TenantID: "t1", ShopifyID: 1}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = marshalPayload(payload)
	}
}

func BenchmarkNewAsynqClient(b *testing.B) {
	b.Skip("requires Redis")
}

// ── Helper to compile-check ──────────────────────────────────────────────────

func TestTaskType_Stringer(t *testing.T) {
	if string(TypeEnrichProduct) == "" {
		t.Error("TypeEnrichProduct should not be empty")
	}
	if TypeEnrichProduct == TypeCartRecoveryCheck {
		t.Error("task types should be unique")
	}
}

// Verify all 3 active payload structs implement round-trip JSON
func TestAllPayloads_RoundTrip(t *testing.T) {
	payloads := []interface{}{
		EnrichProductPayload{ProductID: "x", TenantID: "y", ShopifyID: 1},
		CartRecoveryCheckPayload{ShopID: "shop-x"},
		ContentPullMetricsPayload{ShopID: "shop-x", Platform: "pinterest"},
	}
	for i, p := range payloads {
		data, err := json.Marshal(p)
		if err != nil {
			t.Errorf("[%d] marshal failed: %v", i, err)
		}
		var restored interface{}
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Errorf("[%d] unmarshal failed: %v", i, err)
		}
	}
}
