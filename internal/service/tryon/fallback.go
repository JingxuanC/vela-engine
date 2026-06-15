package tryon

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"log/slog"
)

// ErrorSeverity categorizes the severity of a processing error.
type ErrorSeverity string

const (
	SeverityLow      ErrorSeverity = "low"
	SeverityMedium   ErrorSeverity = "medium"
	SeverityHigh     ErrorSeverity = "high"
	SeverityCritical ErrorSeverity = "critical"
)

// FallbackResult is returned by RunWithFallback, capturing the full outcome.
type FallbackResult struct {
	Success    bool
	Data       interface{}
	Error      string
	Retries    int
	Degraded   bool
	Cached     bool
	Severity   ErrorSeverity
	DurationMs float64
}

// DegradationLevel defines one quality degradation step.
type DegradationLevel struct {
	Model       string
	Resolution  int
	RefinerPass bool
	Description string
}

// DegradationChain defines the quality degradation order.
// Level 0 = best quality, level 3 = fastest/cheapest.
var DegradationChain = []DegradationLevel{
	{ModelTryonPlus, 2048, true, "Best quality (aitryon-plus + refiner @ 2048)"},
	{ModelTryonPlus, 1024, true, "Good quality (aitryon-plus + refiner @ 1024)"},
	{ModelTryonPlus, 1024, false, "Standard quality (aitryon-plus @ 1024, no refiner)"},
	{ModelTryon, 1024, false, "Fast quality (aitryon @ 1024, cheapest)"},
}

// RetryWithBackoff executes an async task with exponential backoff and jitter.
// T is the return type of the task.
func RetryWithBackoff[T any](
	ctx context.Context,
	task func(context.Context) (T, error),
	maxRetries int,
	baseDelay time.Duration,
	maxDelay time.Duration,
	jitter bool,
) (T, error) {
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		default:
		}

		result, err := task(ctx)
		if err == nil {
			return result, nil
		}
		lastErr = err

		if attempt >= maxRetries {
			break
		}

		// Calculate delay with optional jitter
		delay := time.Duration(math.Min(float64(baseDelay)*math.Pow(2, float64(attempt)), float64(maxDelay)))
		if jitter {
			jitterFactor := 0.5 + rand.Float64() // [0.5, 1.5)
			delay = time.Duration(math.Min(float64(delay)*jitterFactor, float64(maxDelay)))
		}

		slog.Info("retrying after error",
			"attempt", attempt+1,
			"max_retries", maxRetries,
			"delay_sec", delay.Seconds(),
			"error", err.Error(),
		)

		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-time.After(delay):
		}
	}

	var zero T
	return zero, fmt.Errorf("all %d retries exhausted: %w", maxRetries, lastErr)
}

// DegradeQuality executes a task, falling back through quality levels on failure.
// Returns the result and whether degradation was used.
func DegradeQuality[T any](
	ctx context.Context,
	task func(context.Context, map[string]interface{}) (T, error),
	params map[string]interface{},
	maxDegradation int,
) (T, bool, error) {
	maxLevel := minInt(maxDegradation+1, len(DegradationChain))

	for level := 0; level < maxLevel; level++ {
		currentParams := copyParams(params)

		if level > 0 {
			dc := DegradationChain[level]
			currentParams["model"] = dc.Model
			currentParams["resolution"] = dc.Resolution
			currentParams["refiner_pass"] = dc.RefinerPass
			slog.Info("degrading quality", "level", level, "desc", dc.Description)
		}

		result, err := task(ctx, currentParams)
		if err == nil {
			return result, level > 0, nil
		}

		slog.Warn("degradation level failed", "level", level, "error", err.Error())
	}

	var zero T
	return zero, false, fmt.Errorf("all %d quality degradation levels exhausted", maxLevel)
}

func copyParams(params map[string]interface{}) map[string]interface{} {
	cp := make(map[string]interface{}, len(params))
	for k, v := range params {
		cp[k] = v
	}
	return cp
}

// --- Negative result cache ---

type negativeCacheEntry struct {
	error    string
	cachedAt time.Time
	ttl      time.Duration
}

// NegativeCache prevents repeated attempts on known-failing operations.
type NegativeCache struct {
	mu    sync.RWMutex
	items map[string]negativeCacheEntry
	stop  chan struct{}
}

// NewNegativeCache creates a new negative cache with automatic pruning.
// Call Stop() to clean up the prune goroutine when the cache is no longer needed.
func NewNegativeCache(pruneInterval time.Duration) *NegativeCache {
	nc := &NegativeCache{
		items: make(map[string]negativeCacheEntry),
		stop:  make(chan struct{}),
	}
	go nc.pruneLoop(pruneInterval)
	return nc
}

// Stop signals the prune goroutine to exit. Safe to call multiple times.
func (nc *NegativeCache) Stop() {
	select {
	case <-nc.stop:
		// Already stopped
	default:
		close(nc.stop)
	}
}

// Set caches a failed task result.
func (nc *NegativeCache) Set(taskID, errMsg string, ttl time.Duration) {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	nc.items[taskID] = negativeCacheEntry{
		error:    errMsg,
		cachedAt: time.Now(),
		ttl:      ttl,
	}
}

// Get checks if a task is in the negative cache and not expired.
// Returns (cached, error_message).
// Performs check and eviction atomically under a single lock to prevent TOCTOU races.
func (nc *NegativeCache) Get(taskID string) (bool, string) {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	entry, ok := nc.items[taskID]
	if !ok {
		return false, ""
	}
	if time.Since(entry.cachedAt) > entry.ttl {
		delete(nc.items, taskID)
		return false, ""
	}
	return true, entry.error
}

// Clear removes a specific entry or all entries from the cache.
func (nc *NegativeCache) Clear(taskID string) int {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	if taskID != "" {
		if _, ok := nc.items[taskID]; ok {
			delete(nc.items, taskID)
			return 1
		}
		return 0
	}
	count := len(nc.items)
	nc.items = make(map[string]negativeCacheEntry)
	return count
}

func (nc *NegativeCache) pruneLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-nc.stop:
			return
		case <-ticker.C:
			nc.prune()
		}
	}
}

func (nc *NegativeCache) prune() {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	now := time.Now()
	for id, entry := range nc.items {
		if now.Sub(entry.cachedAt) > entry.ttl {
			delete(nc.items, id)
		}
	}
}

// --- High-level orchestrator ---

// RunWithFallback executes a task with full fallback orchestration:
// 1. Check negative cache
// 2. Retry with backoff at full quality
// 3. If still failing, degrade quality and retry
// 4. On total failure, cache negative result
func RunWithFallback[T any](
	ctx context.Context,
	taskID string,
	task func(context.Context, map[string]interface{}) (T, error),
	params map[string]interface{},
	nc *NegativeCache,
	maxRetries int,
	maxDegradation int,
	negativeCacheTTL time.Duration,
) FallbackResult {
	start := time.Now()

	// 1. Check negative cache
	if nc != nil {
		if cached, errMsg := nc.Get(taskID); cached {
			slog.Info("task found in negative cache, skipping", "task_id", taskID)
			return FallbackResult{
				Success:    false,
				Error:      errMsg,
				Cached:     true,
				DurationMs: float64(time.Since(start).Milliseconds()),
			}
		}
	}

	// 2. Run task with retry
	result, err := RetryWithBackoff(ctx, func(ctx context.Context) (T, error) {
		return task(ctx, params)
	}, maxRetries, 1*time.Second, 30*time.Second, true)

	if err == nil {
		return FallbackResult{
			Success:    true,
			Data:       result,
			DurationMs: float64(time.Since(start).Milliseconds()),
		}
	}

	// 3. Try degradation
	degResult, wasDegraded, degErr := DegradeQuality(ctx, func(ctx context.Context, p map[string]interface{}) (T, error) {
		return RetryWithBackoff(ctx, func(ctx context.Context) (T, error) {
			return task(ctx, p)
		}, maxRetries, 1*time.Second, 30*time.Second, true)
	}, params, maxDegradation)

	var zero T
	_ = zero

	if degErr == nil {
		return FallbackResult{
			Success:    true,
			Data:       degResult,
			Degraded:   wasDegraded,
			DurationMs: float64(time.Since(start).Milliseconds()),
		}
	}

	// 4. Cache negative result
	if nc != nil {
		nc.Set(taskID, degErr.Error(), negativeCacheTTL)
	}

	return FallbackResult{
		Success:    false,
		Error:      degErr.Error(),
		Severity:   SeverityHigh,
		DurationMs: float64(time.Since(start).Milliseconds()),
	}
}
