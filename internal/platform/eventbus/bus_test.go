package eventbus

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupBus(t *testing.T) (EventBus, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return NewRedisEventBus(c), c
}
func TestEventBus_Publish(t *testing.T) {
	bus, client := setupBus(t)
	e, _ := NewEvent(EventProductCreated, uuid.New(), map[string]string{"id": "1"}, "test")
	require.NoError(t, bus.Publish(context.Background(), e))
	msgs, _ := client.XRange(context.Background(), e.Type.StreamName(), "-", "+").Result()
	assert.Len(t, msgs, 1)
}
func TestEventBus_Subscribe(t *testing.T) {
	bus, _ := setupBus(t)
	var calls int32
	bus.Subscribe(EventOrderCreated, func(ctx context.Context, e *Event) error { atomic.AddInt32(&calls, 1); return nil })
	bus.StartConsumers(context.Background())
	time.Sleep(100 * time.Millisecond)
	e, _ := NewEvent(EventOrderCreated, uuid.New(), map[string]string{}, "test")
	bus.Publish(context.Background(), e)
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}
func TestEventBus_NilEvent(t *testing.T) {
	bus, _ := setupBus(t)
	assert.Error(t, bus.Publish(context.Background(), nil))
}
func TestEventBus_NewEvent(t *testing.T) {
	e, err := NewEvent(EventProductCreated, uuid.New(), map[string]interface{}{"name": "Test"}, "webhook")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, e.ID)
	var d map[string]interface{}
	json.Unmarshal(e.Payload, &d)
	assert.Equal(t, "Test", d["name"])
}
func TestEventBus_MultipleHandlers(t *testing.T) {
	bus, _ := setupBus(t)
	var c1, c2 int32
	bus.Subscribe(EventProductCreated, func(ctx context.Context, e *Event) error { atomic.AddInt32(&c1, 1); return nil })
	bus.Subscribe(EventProductCreated, func(ctx context.Context, e *Event) error { atomic.AddInt32(&c2, 1); return nil })
	bus.StartConsumers(context.Background())
	time.Sleep(100 * time.Millisecond)
	e, _ := NewEvent(EventProductCreated, uuid.New(), map[string]string{}, "test")
	bus.Publish(context.Background(), e)
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, int32(1), atomic.LoadInt32(&c1))
	assert.Equal(t, int32(1), atomic.LoadInt32(&c2))
}
func TestEventBus_Close(t *testing.T) {
	bus, _ := setupBus(t)
	assert.NoError(t, bus.Close())
}
