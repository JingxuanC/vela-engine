// Package circuitbreaker provides a simple circuit breaker for external API calls.
package circuitbreaker

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen is returned when the circuit is open and the call is rejected.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// State represents the circuit breaker state.
type State int

const (
	// StateClosed — normal operation, calls are allowed.
	StateClosed State = iota
	// StateOpen — calls fail immediately without execution.
	StateOpen
	// StateHalfOpen — one trial call is allowed to test recovery.
	StateHalfOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreaker protects an external API with a simple state machine.
// It transitions from closed → open when failures hit threshold,
// open → half-open after resetTimeout, and half-open → closed on success
// or half-open → open on failure.
type CircuitBreaker struct {
	mu sync.RWMutex

	state         State
	failureCount  int
	lastFailureAt time.Time

	threshold    int
	resetTimeout time.Duration
}

// New creates a CircuitBreaker with the given threshold and reset timeout.
// threshold: number of consecutive failures before opening the circuit.
// resetTimeout: duration after which the circuit transitions from open to half-open.
func New(threshold int, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:         StateClosed,
		threshold:    threshold,
		resetTimeout: resetTimeout,
	}
}

// State returns the current circuit breaker state.
func (cb *CircuitBreaker) State() State {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// Call executes fn if the circuit is closed or half-open.
// Returns ErrCircuitOpen if the circuit is open and the reset timeout
// has not elapsed.
func (cb *CircuitBreaker) Call(ctx context.Context, fn func(context.Context) error) error {
	if !cb.allowRequest() {
		return ErrCircuitOpen
	}

	err := fn(ctx)

	cb.recordResult(err)
	return err
}

// allowRequest checks whether the request should be allowed through.
func (cb *CircuitBreaker) allowRequest() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		if time.Since(cb.lastFailureAt) > cb.resetTimeout {
			cb.state = StateHalfOpen
			return true
		}
		return false
	case StateHalfOpen:
		return true
	default:
		return true
	}
}

// recordResult records the outcome of a call and updates state accordingly.
func (cb *CircuitBreaker) recordResult(err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err == nil {
		// Success — reset if half-open (→ closed) or closed (reset counter).
		cb.failureCount = 0
		if cb.state == StateHalfOpen {
			cb.state = StateClosed
		}
		return
	}

	// Failure
	cb.lastFailureAt = time.Now()
	if cb.state == StateHalfOpen {
		// One failure in half-open → back to open
		cb.state = StateOpen
		cb.failureCount = 1
		return
	}

	// StateClosed
	cb.failureCount++
	if cb.failureCount >= cb.threshold {
		cb.state = StateOpen
	}
}

// Reset forces the circuit breaker back to closed state.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = StateClosed
	cb.failureCount = 0
	cb.lastFailureAt = time.Time{}
}
