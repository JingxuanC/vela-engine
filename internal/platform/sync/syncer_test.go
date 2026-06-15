package sync

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/google/uuid"
)

// ── Mock EventBus ────────────────────────────────────────────────────────────

type mockEventBus struct {
	mu       sync.Mutex
	events   []*eventbus.Event
	publishCh chan *eventbus.Event
}

func newMockEventBus() *mockEventBus {
	return &mockEventBus{publishCh: make(chan *eventbus.Event, 100)}
}

func (m *mockEventBus) Publish(ctx context.Context, event *eventbus.Event) error {
	m.mu.Lock()
	m.events = append(m.events, event)
	m.mu.Unlock()
	select {
	case m.publishCh <- event:
	default:
	}
	return nil
}

func (m *mockEventBus) Subscribe(eventType eventbus.EventType, handler eventbus.EventHandler) (func(), error) {
	return func() {}, nil
}

func (m *mockEventBus) StartConsumers(ctx context.Context) error { return nil }
func (m *mockEventBus) Close() error                              { return nil }

func (m *mockEventBus) publishedEvents() []*eventbus.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*eventbus.Event{}, m.events...)
}

// ── PipelineEventPayload Tests ───────────────────────────────────────────────

func TestPipelineEventPayload_JSONRoundTrip(t *testing.T) {
	payload := eventbus.PipelineEventPayload{
		EntityID:  "12345",
		ShopID:    uuid.New().String(),
		Source:    "cron",
		Action:    "upsert",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored eventbus.PipelineEventPayload
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.EntityID != payload.EntityID {
		t.Errorf("EntityID: got %q, want %q", restored.EntityID, payload.EntityID)
	}
	if restored.Source != payload.Source {
		t.Errorf("Source: got %q, want %q", restored.Source, payload.Source)
	}
	if restored.Action != payload.Action {
		t.Errorf("Action: got %q, want %q", restored.Action, payload.Action)
	}
}

func TestPipelineEventPayload_RequiredFields(t *testing.T) {
	payload := eventbus.PipelineEventPayload{
		EntityID: "test-entity",
		Source:   "webhook",
		Action:   "delete",
	}

	data, _ := json.Marshal(payload)
	var m map[string]interface{}
	json.Unmarshal(data, &m)

	required := []string{"entity_id", "shop_id", "source", "action", "ts"}
	for _, field := range required {
		if _, ok := m[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

// ── Event Type Tests ─────────────────────────────────────────────────────────

func TestPipelineEventTypes(t *testing.T) {
	tests := []struct {
		eventType eventbus.EventType
		want      string
	}{
		{eventbus.EventProductSynced, "pipeline.product.synced"},
		{eventbus.EventOrderSynced, "pipeline.order.synced"},
		{eventbus.EventCustomerSynced, "pipeline.customer.synced"},
		{eventbus.EventReviewSynced, "pipeline.review.synced"},
		{eventbus.EventReviewReplied, "pipeline.review.replied"},
	}
	for _, tt := range tests {
		if string(tt.eventType) != tt.want {
			t.Errorf("EventType %q = %q, want %q", tt.eventType, string(tt.eventType), tt.want)
		}
	}
}

func TestPipelineEvent_StreamName(t *testing.T) {
	stream := eventbus.EventProductSynced.StreamName()
	if stream != "eventbus:stream:pipeline.product.synced" {
		t.Errorf("unexpected stream name: %s", stream)
	}
}

// ── Syncer Constructor Tests ─────────────────────────────────────────────────

func TestNewProductSyncer_NilEventBus(t *testing.T) {
	// Should not panic with nil EventBus
	s := NewProductSyncer(nil, nil, nil)
	if s == nil {
		t.Fatal("NewProductSyncer returned nil")
	}
	if s.eventBus != nil {
		t.Error("expected nil eventBus")
	}
}

func TestNewProductSyncer_WithMockEventBus(t *testing.T) {
	bus := newMockEventBus()
	s := NewProductSyncer(nil, nil, bus)
	if s.eventBus == nil {
		t.Fatal("expected non-nil eventBus")
	}
}

func TestNewOrderSyncer_WithMockEventBus(t *testing.T) {
	bus := newMockEventBus()
	s := NewOrderSyncer(nil, nil, bus)
	if s.eventBus == nil {
		t.Fatal("expected non-nil eventBus")
	}
}

func TestNewCustomerSyncer_WithMockEventBus(t *testing.T) {
	bus := newMockEventBus()
	s := NewCustomerSyncer(nil, nil, bus)
	if s.eventBus == nil {
		t.Fatal("expected non-nil eventBus")
	}
}

// ── EventBus Nil Safety Tests ────────────────────────────────────────────────

func TestProductSyncer_EntityType(t *testing.T) {
	s := NewProductSyncer(nil, nil, nil)
	if s.EntityType() != "products" {
		t.Errorf("EntityType = %q, want %q", s.EntityType(), "products")
	}
}

func TestOrderSyncer_EntityType(t *testing.T) {
	s := NewOrderSyncer(nil, nil, nil)
	if s.EntityType() != "orders" {
		t.Errorf("EntityType = %q, want %q", s.EntityType(), "orders")
	}
}

func TestCustomerSyncer_EntityType(t *testing.T) {
	s := NewCustomerSyncer(nil, nil, nil)
	if s.EntityType() != "customers" {
		t.Errorf("EntityType = %q, want %q", s.EntityType(), "customers")
	}
}

// ── NewEvent with PipelineEventPayload ──────────────────────────────────────

func TestNewEvent_PipelinePayload(t *testing.T) {
	shopID := uuid.New()
	payload := eventbus.PipelineEventPayload{
		EntityID:  "999888",
		ShopID:    shopID.String(),
		Source:    "manual",
		Action:    "upsert",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	ev, err := eventbus.NewEvent(eventbus.EventProductSynced, shopID, payload, "test")
	if err != nil {
		t.Fatalf("NewEvent failed: %v", err)
	}
	if ev.Type != eventbus.EventProductSynced {
		t.Errorf("event type: got %q, want %q", ev.Type, eventbus.EventProductSynced)
	}
	if ev.ShopID != shopID {
		t.Errorf("shop ID mismatch")
	}
	if ev.Source != "test" {
		t.Errorf("source: got %q, want %q", ev.Source, "test")
	}

	// Verify payload round-trips through the event
	var restored eventbus.PipelineEventPayload
	if err := json.Unmarshal(ev.Payload, &restored); err != nil {
		t.Fatalf("unmarshal event payload: %v", err)
	}
	if restored.EntityID != "999888" {
		t.Errorf("EntityID round-trip: got %q", restored.EntityID)
	}
	if restored.Source != "manual" {
		t.Errorf("Source round-trip: got %q", restored.Source)
	}
}

// ── Benchmark ───────────────────────────────────────────────────────────────

func BenchmarkNewEvent_PipelinePayload(b *testing.B) {
	shopID := uuid.New()
	payload := eventbus.PipelineEventPayload{
		EntityID:  "12345",
		ShopID:    shopID.String(),
		Source:    "cron",
		Action:    "upsert",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = eventbus.NewEvent(eventbus.EventProductSynced, shopID, payload, "bench")
	}
}

func BenchmarkPipelineEventPayload_Marshal(b *testing.B) {
	payload := eventbus.PipelineEventPayload{
		EntityID:  "12345",
		ShopID:    uuid.New().String(),
		Source:    "cron",
		Action:    "upsert",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(payload)
	}
}
