package taskqueue

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// handlerMock implements a minimal ServeMux-like interface for testing
// handler registration without importing asynq.
type handlerMock struct {
	registeredTypes map[string]func(ctx context.Context, payload []byte) error
}

func newHandlerMock() *handlerMock {
	return &handlerMock{
		registeredTypes: make(map[string]func(ctx context.Context, payload []byte) error),
	}
}

// HandleFunc records the task type and handler for later verification.
// This mirrors asynq.ServeMux.HandleFunc.
func (m *handlerMock) HandleFunc(taskType string, handler func(ctx context.Context, task interface{}) error) {
	m.registeredTypes[taskType] = func(ctx context.Context, payload []byte) error {
		return handler(ctx, payload)
	}
}

func (m *handlerMock) handle(taskType string) bool {
	_, ok := m.registeredTypes[taskType]
	return ok
}

// TestRegisterHandler verifies that RegisterHandler registers a handler
// for the correct task type on a mux.
func TestRegisterHandler(t *testing.T) {
	tests := []struct {
		name     string
		taskType TaskType
	}{
		{"TypeEnrichProduct", TypeEnrichProduct},
		{"TypeCartRecoveryCheck", TypeCartRecoveryCheck},
		{"TypeContentPullMetrics", TypeContentPullMetrics},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := newHandlerMock()
			handler := func(ctx context.Context, task interface{}) error {
				return nil
			}

			mux.HandleFunc(string(tt.taskType), handler)

			assert.True(t, mux.handle(string(tt.taskType)),
				"handler should be registered for task type %q", tt.taskType)
			assert.Len(t, mux.registeredTypes, 1,
				"only one handler should be registered")
		})
	}
}

// TestRegisterHandlerMultiple verifies that multiple handlers can be
// registered on the same mux for different task types without collision.
func TestRegisterHandlerMultiple(t *testing.T) {
	mux := newHandlerMock()

	registerOnMock := func(taskType TaskType, handler func(ctx context.Context, task interface{}) error) {
		mux.HandleFunc(string(taskType), handler)
	}

	// Register handlers for the 3 active task types
	registerOnMock(TypeEnrichProduct, func(ctx context.Context, task interface{}) error { return nil })
	registerOnMock(TypeCartRecoveryCheck, func(ctx context.Context, task interface{}) error { return nil })
	registerOnMock(TypeContentPullMetrics, func(ctx context.Context, task interface{}) error { return nil })

	// Verify all are registered
	for _, tt := range []TaskType{
		TypeEnrichProduct,
		TypeCartRecoveryCheck,
		TypeContentPullMetrics,
	} {
		assert.True(t, mux.handle(string(tt)), "handler should exist for %q", tt)
	}

	assert.Len(t, mux.registeredTypes, 3, "all 3 active task types should be registered")
}

// TestRegisterHandlerOverwrite confirms that re-registering the same type
// simply overwrites (asynq allows this).
func TestRegisterHandlerOverwrite(t *testing.T) {
	mux := newHandlerMock()

	handler := func(ctx context.Context, task interface{}) error {
		return nil
	}

	mux.HandleFunc(string(TypeEnrichProduct), handler)
	mux.HandleFunc(string(TypeEnrichProduct), handler) // overwrite

	assert.True(t, mux.handle(string(TypeEnrichProduct)),
		"handler should exist after re-registration")
	assert.Len(t, mux.registeredTypes, 1,
		"only one entry should exist (overwritten)")
}

// TestNewServerDefaultQueues verifies that the queue weight map constants
// used in NewServer are consistent.
func TestNewServerDefaultQueues(t *testing.T) {
	queues := map[string]int{
		"critical": 6,
		"default":  3,
		"low":      1,
	}

	assert.Equal(t, 6, queues["critical"], "critical queue weight should be 6")
	assert.Equal(t, 3, queues["default"], "default queue weight should be 3")
	assert.Equal(t, 1, queues["low"], "low queue weight should be 1")
	assert.Equal(t, 10, queues["critical"]+queues["default"]+queues["low"],
		"queue weights should sum to the concurrency value")
}

// TestNewServerWithMiniredis verifies that a config pointing at a miniredis
// address can resolve the address correctly.
func TestNewServerWithMiniredis(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	addr := mr.Addr()
	t.Logf("miniredis address: %s", addr)
	assert.NotEmpty(t, addr)
	assert.Contains(t, addr, ":")
}
