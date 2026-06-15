package circuitbreaker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errTest = errors.New("test error")

func TestCircuitBreaker_InitialState(t *testing.T) {
	cb := New(3, 100*time.Millisecond)
	assert.Equal(t, StateClosed, cb.State())
}

func TestCircuitBreaker_ClosedToOpen(t *testing.T) {
	cb := New(3, 100*time.Millisecond)
	ctx := context.Background()

	// First two failures — still closed
	err1 := cb.Call(ctx, func(ctx context.Context) error { return errTest })
	assert.ErrorIs(t, err1, errTest)
	assert.Equal(t, StateClosed, cb.State())

	err2 := cb.Call(ctx, func(ctx context.Context) error { return errTest })
	assert.ErrorIs(t, err2, errTest)
	assert.Equal(t, StateClosed, cb.State())

	// Third failure — transitions to open
	err3 := cb.Call(ctx, func(ctx context.Context) error { return errTest })
	assert.ErrorIs(t, err3, errTest)
	assert.Equal(t, StateOpen, cb.State())
}

func TestCircuitBreaker_OpenRejectsCalls(t *testing.T) {
	cb := New(2, 1*time.Hour) // long timeout so it stays open
	ctx := context.Background()

	// Two failures → open
	_ = cb.Call(ctx, func(ctx context.Context) error { return errTest })
	_ = cb.Call(ctx, func(ctx context.Context) error { return errTest })
	require.Equal(t, StateOpen, cb.State())

	// Next call should be rejected with ErrCircuitOpen
	err := cb.Call(ctx, func(ctx context.Context) error { return errTest })
	assert.ErrorIs(t, err, ErrCircuitOpen)
	assert.Equal(t, StateOpen, cb.State())
}

func TestCircuitBreaker_OpenToHalfOpenToClosed(t *testing.T) {
	cb := New(2, 50*time.Millisecond)
	ctx := context.Background()

	// Two failures → open
	_ = cb.Call(ctx, func(ctx context.Context) error { return errTest })
	_ = cb.Call(ctx, func(ctx context.Context) error { return errTest })
	require.Equal(t, StateOpen, cb.State())

	// Wait for reset timeout
	time.Sleep(60 * time.Millisecond)

	// This call should be allowed (half-open)
	var called atomic.Bool
	err := cb.Call(ctx, func(ctx context.Context) error {
		called.Store(true)
		return nil
	})
	assert.NoError(t, err)
	assert.True(t, called.Load())
	assert.Equal(t, StateClosed, cb.State())
}

func TestCircuitBreaker_HalfOpenFailure(t *testing.T) {
	cb := New(2, 50*time.Millisecond)
	ctx := context.Background()

	// Two failures → open
	_ = cb.Call(ctx, func(ctx context.Context) error { return errTest })
	_ = cb.Call(ctx, func(ctx context.Context) error { return errTest })
	require.Equal(t, StateOpen, cb.State())

	// Wait for reset timeout
	time.Sleep(60 * time.Millisecond)

	// Half-open attempt fails — back to open
	err := cb.Call(ctx, func(ctx context.Context) error { return errTest })
	assert.ErrorIs(t, err, errTest)
	assert.Equal(t, StateOpen, cb.State())

	// Still open — call rejected
	err = cb.Call(ctx, func(ctx context.Context) error { return nil })
	assert.ErrorIs(t, err, ErrCircuitOpen)
}

func TestCircuitBreaker_Reset(t *testing.T) {
	cb := New(1, 1*time.Hour)
	ctx := context.Background()

	// One failure → open
	_ = cb.Call(ctx, func(ctx context.Context) error { return errTest })
	require.Equal(t, StateOpen, cb.State())

	cb.Reset()
	assert.Equal(t, StateClosed, cb.State())

	// After reset, calls go through again
	err := cb.Call(ctx, func(ctx context.Context) error { return nil })
	assert.NoError(t, err)
	assert.Equal(t, StateClosed, cb.State())
}

func TestCircuitBreaker_ConcurrentSafety(t *testing.T) {
	cb := New(5, 100*time.Millisecond)
	ctx := context.Background()

	var failed atomic.Int32
	var completed atomic.Int32

	const goroutines = 20
	done := make(chan struct{})

	for range goroutines {
		go func() {
			err := cb.Call(ctx, func(ctx context.Context) error {
				completed.Add(1)
				return errTest // all fail
			})
			if err != nil {
				failed.Add(1)
			}
			done <- struct{}{}
		}()
	}

	for range goroutines {
		<-done
	}

	// At least 5 calls were executed (before circuit opened), rest rejected
	executed := completed.Load()
	rejected := failed.Load() - executed
	t.Logf("executed=%d rejected=%d", executed, rejected)
	assert.GreaterOrEqual(t, int(executed), 5)
	assert.GreaterOrEqual(t, int(rejected), goroutines-5)
}
