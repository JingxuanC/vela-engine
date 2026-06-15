package notify

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func newTestCenter() *Center {
	return NewCenter(nil)
}

// --- Trigger struct tests ---

func TestTrigger_EventMatch(t *testing.T) {
	c := newTestCenter()
	c.RegisterTrigger(Trigger{
		Event: "order.created",
		Condition: func(ctx context.Context, data interface{}) (bool, *Notification) {
			return true, &Notification{
				ShopID: uuid.MustParse("00000000-0000-0000-0000-000000000001"),
				Type:   "order_confirmation",
				Title:  "New Order",
				Body:   "A new order has been placed.",
			}
		},
	})
	results := c.Check(context.Background(), "order.created", nil)
	assert.Len(t, results, 1)
	assert.Equal(t, "New Order", results[0].Title)
}

func TestTrigger_NoMatch(t *testing.T) {
	c := newTestCenter()
	c.RegisterTrigger(Trigger{
		Event: "order.created",
		Condition: func(ctx context.Context, data interface{}) (bool, *Notification) {
			return true, &Notification{Title: "Order Notification"}
		},
	})
	results := c.Check(context.Background(), "product.created", nil)
	assert.Len(t, results, 0, "should not match a different event")
}

func TestTrigger_ConditionFalse(t *testing.T) {
	c := newTestCenter()
	c.RegisterTrigger(Trigger{
		Event: "low_stock",
		Condition: func(ctx context.Context, data interface{}) (bool, *Notification) {
			qty, ok := data.(int)
			if !ok {
				return false, nil
			}
			if qty < 10 {
				return true, &Notification{
					ShopID: uuid.MustParse("00000000-0000-0000-0000-000000000001"),
					Type:   "low_stock",
					Title:  "Low Stock Alert",
					Body:   "Stock is below threshold.",
				}
			}
			return false, nil
		},
	})
	// Qty = 100, condition should return false
	results := c.Check(context.Background(), "low_stock", 100)
	assert.Len(t, results, 0)

	// Qty = 5, condition should return true
	results = c.Check(context.Background(), "low_stock", 5)
	assert.Len(t, results, 1)
	assert.Equal(t, "Low Stock Alert", results[0].Title)
}

func TestTrigger_MultipleTriggersSameEvent(t *testing.T) {
	c := newTestCenter()
	c.RegisterTrigger(Trigger{
		Event: "order.created",
		Condition: func(ctx context.Context, data interface{}) (bool, *Notification) {
			return true, &Notification{Title: "Trigger 1"}
		},
	})
	c.RegisterTrigger(Trigger{
		Event: "order.created",
		Condition: func(ctx context.Context, data interface{}) (bool, *Notification) {
			return true, &Notification{Title: "Trigger 2"}
		},
	})
	results := c.Check(context.Background(), "order.created", nil)
	assert.Len(t, results, 2)
	assert.Equal(t, "Trigger 1", results[0].Title)
	assert.Equal(t, "Trigger 2", results[1].Title)
}

func TestTrigger_MultipleEvents(t *testing.T) {
	c := newTestCenter()
	c.RegisterTrigger(Trigger{
		Event: "order.created",
		Condition: func(ctx context.Context, data interface{}) (bool, *Notification) {
			return true, &Notification{Title: "Order Created"}
		},
	})
	c.RegisterTrigger(Trigger{
		Event: "customer.created",
		Condition: func(ctx context.Context, data interface{}) (bool, *Notification) {
			return true, &Notification{Title: "New Customer"}
		},
	})
	c.RegisterTrigger(Trigger{
		Event: "product.created",
		Condition: func(ctx context.Context, data interface{}) (bool, *Notification) {
			return true, &Notification{Title: "New Product"}
		},
	})
	results := c.Check(context.Background(), "customer.created", nil)
	assert.Len(t, results, 1)
	assert.Equal(t, "New Customer", results[0].Title)
}

func TestTrigger_NilCondition(t *testing.T) {
	c := newTestCenter()
	c.RegisterTrigger(Trigger{Event: "test.event"})
	results := c.Check(context.Background(), "test.event", nil)
	assert.Len(t, results, 0, "nil condition should not produce notifications")
}

func TestTrigger_NilNotificationOnTrue(t *testing.T) {
	c := newTestCenter()
	c.RegisterTrigger(Trigger{
		Event: "test.event",
		Condition: func(ctx context.Context, data interface{}) (bool, *Notification) {
			return true, nil
		},
	})
	results := c.Check(context.Background(), "test.event", nil)
	assert.Len(t, results, 0, "true with nil notification should be skipped")
}

func TestTrigger_EmptyEventsDontMatch(t *testing.T) {
	c := newTestCenter()
	c.RegisterTrigger(Trigger{
		Event: "",
		Condition: func(ctx context.Context, data interface{}) (bool, *Notification) {
			return true, &Notification{Title: "Empty Event"}
		},
	})
	results := c.Check(context.Background(), "some.event", nil)
	assert.Len(t, results, 0, "empty event trigger should not match non-empty event")
}

// --- Concurrency safety ---

func TestTrigger_ConcurrentRegisterAndCheck(t *testing.T) {
	c := newTestCenter()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			c.RegisterTrigger(Trigger{
				Event: "concurrent",
				Condition: func(ctx context.Context, data interface{}) (bool, *Notification) {
					return true, &Notification{Title: "Concurrent"}
				},
			})
		}
		close(done)
	}()

	for i := 0; i < 50; i++ {
		c.Check(context.Background(), "concurrent", nil)
	}
	<-done
	// Should not panic
	results := c.Check(context.Background(), "concurrent", nil)
	assert.GreaterOrEqual(t, len(results), 1)
}
