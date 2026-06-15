package tryon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRetryWithBackoff_Success(t *testing.T) {
	attempts := 0
	task := func(ctx context.Context) (string, error) {
		attempts++
		return "ok", nil
	}

	result, err := RetryWithBackoff(context.Background(), task, 3, 10*time.Millisecond, 100*time.Millisecond, false)
	assert.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, 1, attempts)
}

func TestRetryWithBackoff_RetriesThenSucceeds(t *testing.T) {
	attempts := 0
	task := func(ctx context.Context) (string, error) {
		attempts++
		if attempts < 3 {
			return "", errors.New("transient error")
		}
		return "recovered", nil
	}

	result, err := RetryWithBackoff(context.Background(), task, 3, 10*time.Millisecond, 100*time.Millisecond, false)
	assert.NoError(t, err)
	assert.Equal(t, "recovered", result)
	assert.Equal(t, 3, attempts)
}

func TestRetryWithBackoff_ExhaustsRetries(t *testing.T) {
	task := func(ctx context.Context) (string, error) {
		return "", errors.New("always fails")
	}

	_, err := RetryWithBackoff(context.Background(), task, 2, 10*time.Millisecond, 100*time.Millisecond, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "all 2 retries exhausted")
}

func TestRetryWithBackoff_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	task := func(ctx context.Context) (string, error) {
		return "", errors.New("error")
	}

	_, err := RetryWithBackoff(ctx, task, 3, 10*time.Millisecond, 100*time.Millisecond, false)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

func TestNegativeCache_SetAndGet(t *testing.T) {
	nc := NewNegativeCache(time.Hour)

	cached, msg := nc.Get("task-1")
	assert.False(t, cached)
	assert.Empty(t, msg)

	nc.Set("task-1", "something went wrong", 10*time.Second)
	cached, msg = nc.Get("task-1")
	assert.True(t, cached)
	assert.Equal(t, "something went wrong", msg)
}

func TestNegativeCache_TTLExpiry(t *testing.T) {
	nc := NewNegativeCache(50 * time.Millisecond)

	nc.Set("task-1", "error", 1*time.Millisecond)
	time.Sleep(10 * time.Millisecond)

	cached, _ := nc.Get("task-1")
	assert.False(t, cached, "entry should have expired")
}

func TestNegativeCache_Clear(t *testing.T) {
	nc := NewNegativeCache(time.Hour)
	nc.Set("a", "err1", time.Minute)
	nc.Set("b", "err2", time.Minute)

	assert.Equal(t, 1, nc.Clear("a"))
	cached, _ := nc.Get("a")
	assert.False(t, cached)
	cached, _ = nc.Get("b")
	assert.True(t, cached)

	assert.Equal(t, 1, nc.Clear(""))
	cached, _ = nc.Get("b")
	assert.False(t, cached)
}

func TestDegradationChain(t *testing.T) {
	assert.Len(t, DegradationChain, 4)
	assert.Equal(t, "aitryon-plus", DegradationChain[0].Model)
	assert.Equal(t, 2048, DegradationChain[0].Resolution)
	assert.True(t, DegradationChain[0].RefinerPass)
	assert.Equal(t, "aitryon", DegradationChain[3].Model)
	assert.False(t, DegradationChain[3].RefinerPass)
}
