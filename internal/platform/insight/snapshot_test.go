package insight

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// ── ShopSnapshot JSON ────────────────────────────────────────────────────────

func TestShopSnapshot_JSONRoundTrip(t *testing.T) {
	snap := &ShopSnapshot{
		ShopID:           "test-shop-id",
		GeneratedAt:      time.Now().UTC(),
		ReturnRate:       12.5,
		ReturnCount:      15,
		OrderCount:       120,
		TopReturnReasons: []string{"size_too_small", "color_mismatch"},
		TotalProducts:    45,
		TopCategories:    []string{"tops", "dresses", "shoes"},
		TotalCustomers:   89,
		TotalTryOns:      67,
	}

	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored ShopSnapshot
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.ShopID != snap.ShopID {
		t.Errorf("ShopID: got %q, want %q", restored.ShopID, snap.ShopID)
	}
	if restored.ReturnRate != snap.ReturnRate {
		t.Errorf("ReturnRate: got %f, want %f", restored.ReturnRate, snap.ReturnRate)
	}
	if restored.TotalProducts != snap.TotalProducts {
		t.Errorf("TotalProducts: got %d, want %d", restored.TotalProducts, snap.TotalProducts)
	}
	if len(restored.TopReturnReasons) != len(snap.TopReturnReasons) {
		t.Errorf("TopReturnReasons count: got %d, want %d", len(restored.TopReturnReasons), len(snap.TopReturnReasons))
	}
}

func TestShopSnapshot_EmptyFields(t *testing.T) {
	snap := &ShopSnapshot{
		ShopID:      "minimal-shop",
		GeneratedAt: time.Now().UTC(),
	}
	data, _ := json.Marshal(snap)
	if len(data) < 10 {
		t.Error("empty snapshot should produce valid JSON")
	}
}

// ── Constants ────────────────────────────────────────────────────────────────

func TestSnapshotKeyPrefix(t *testing.T) {
	if snapshotKeyPrefix != "shop:snapshot:" {
		t.Errorf("key prefix = %q, want %q", snapshotKeyPrefix, "shop:snapshot:")
	}
}

func TestSnapshotTTL(t *testing.T) {
	if snapshotTTL != 25*time.Hour {
		t.Errorf("TTL = %v, want %v", snapshotTTL, 25*time.Hour)
	}
}

// ── NewSnapshotStore ─────────────────────────────────────────────────────────

func TestNewSnapshotStore(t *testing.T) {
	store := NewSnapshotStore(nil, nil)
	if store == nil {
		t.Fatal("NewSnapshotStore returned nil")
	}
	if store.db != nil {
		t.Error("expected nil db")
	}
}

// ── SnapshotStore Get without Redis ──────────────────────────────────────────

func TestSnapshotStore_Get_NoRedis(t *testing.T) {
	store := NewSnapshotStore(nil, nil)
	snap := store.Get(context.Background(), "test-shop")
	if snap != nil {
		t.Error("Get with nil redis should return nil")
	}
}

func TestSnapshotStore_GetOrRefresh_NoRedis(t *testing.T) {
	store := NewSnapshotStore(nil, nil)
	// Without Redis and without DB, Refresh will fail but GetOrRefresh handles it
	snap := store.GetOrRefresh(context.Background(), "test-shop")
	// Should return nil because Refresh fails (no DB)
	if snap != nil {
		t.Error("GetOrRefresh without DB should return nil")
	}
}

// ── Insight Types ────────────────────────────────────────────────────────────


func TestSeverity(t *testing.T) {
	severities := []Severity{
		SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical,
	}
	for _, s := range severities {
		if string(s) == "" {
			t.Error("severity should not be empty")
		}
	}
}

// ── MarshalInsights ──────────────────────────────────────────────────────────

func TestMarshalInsights(t *testing.T) {
	insights := []Insight{
		{Type: InsightTypeReturnRate, Severity: SeverityHigh, Title: "High return rate", Body: "Rate is 25%", SampleSize: 100, DataFreshness: "fresh", Confidence: "high"},
		{Type: InsightTypeTrend, Severity: SeverityLow, Title: "Stable trend", Body: "No changes", SampleSize: 50, DataFreshness: "stale", Confidence: "low"},
	}

	data, err := MarshalInsights(insights)
	if err != nil {
		t.Fatalf("MarshalInsights failed: %v", err)
	}

	restored, err := UnmarshalInsights(data)
	if err != nil {
		t.Fatalf("UnmarshalInsights failed: %v", err)
	}

	if len(restored) != 2 {
		t.Fatalf("expected 2 insights, got %d", len(restored))
	}
	if restored[0].Title != "High return rate" {
		t.Errorf("Title[0] = %q", restored[0].Title)
	}
	if restored[0].SampleSize != 100 {
		t.Errorf("SampleSize[0] = %d", restored[0].SampleSize)
	}
}

func TestUnmarshalInsights_Empty(t *testing.T) {
	insights, err := UnmarshalInsights([]byte("[]"))
	if err != nil {
		t.Fatalf("empty array should parse: %v", err)
	}
	if len(insights) != 0 {
		t.Errorf("expected 0 insights, got %d", len(insights))
	}
}

// ── NewInsightEngine ─────────────────────────────────────────────────────────


// ── computeReturnRate without DB ─────────────────────────────────────────────

func TestComputeReturnRate_NoDB(t *testing.T) {
	engine := NewInsightEngine(nil, nil, nil, nil)
	rate, orders, returns, reasons, lastReturn, err := engine.ComputeReturnRate(context.Background(), "test", 30)

	if err == nil {
		t.Error("expected error when db is nil")
	}
	if rate != 0 {
		t.Errorf("expected 0 rate, got %f", rate)
	}
	if orders != 0 || returns != 0 {
		t.Error("expected 0 orders and returns")
	}
	if len(reasons) != 0 {
		t.Error("expected empty reasons")
	}
	if lastReturn != nil {
		t.Error("expected nil lastReturn")
	}
}

// ── getTopCategories / getCategoryAveragePrice without DB ────────────────────

func TestGetTopCategories_NoDB(t *testing.T) {
	engine := NewInsightEngine(nil, nil, nil, nil)
	cats, err := engine.getTopCategories(context.Background(), "test", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cats) != 1 || cats[0] != "general" {
		t.Errorf("expected [general], got %v", cats)
	}
}

func TestGetCategoryAveragePrice_NoDB(t *testing.T) {
	engine := NewInsightEngine(nil, nil, nil, nil)
	avg, err := engine.getCategoryAveragePrice(context.Background(), "test", "tops")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if avg != 49.99 {
		t.Errorf("expected 49.99 fallback, got %f", avg)
	}
}

// ── Benchmarks ───────────────────────────────────────────────────────────────

func BenchmarkShopSnapshot_Marshal(b *testing.B) {
	snap := &ShopSnapshot{
		ShopID: "bench-shop", GeneratedAt: time.Now(),
		ReturnRate: 15.3, ReturnCount: 45, OrderCount: 294,
		TopReturnReasons: []string{"size", "color"},
		TotalProducts: 120, TopCategories: []string{"a", "b", "c"},
		TotalCustomers: 300, TotalTryOns: 150,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(snap)
	}
}

func BenchmarkMarshalInsights(b *testing.B) {
	insights := []Insight{
		{Type: InsightTypeReturnRate, Severity: SeverityHigh, Title: "t", Body: "b", SampleSize: 100},
		{Type: InsightTypeTrend, Severity: SeverityLow, Title: "t2", Body: "b2", SampleSize: 50},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = MarshalInsights(insights)
	}
}
