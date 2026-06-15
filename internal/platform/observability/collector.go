// Package observability provides lightweight metrics, structured logging, and
// tracing support for the Vela AI API server. All metrics are Prometheus-compatible
// and exposed via GET /metrics.
package observability

import (
	"fmt"
	"math"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ── Metrics Collector ───────────────────────────────────────────────────────

// Collector is a thread-safe metrics accumulator.
type Collector struct {
	mu sync.RWMutex

	// Counters
	requestsTotal      int64
	requestsByStatus   map[int]int64
	requestsByMethod   map[string]int64
	requestsByPath     map[string]int64
	errorsTotal         int64

	// DB metrics
	dbQueriesTotal     int64
	dbQueryErrors       int64
	dbQueryDurationSum  int64
	dbQueryDurationCount int64

	// External API metrics
	externalCallsTotal     int64
	externalCallErrors     int64
	externalCallDurationSum int64
	externalCallCount       int64

	// LLM metrics (global + per-module)
	llmCalls             int64
	llmTokensIn          int64
	llmTokensOut         int64
	llmCallsByModule     map[string]int64
	llmTokensInByModule  map[string]int64
	llmTokensOutByModule map[string]int64
	llmCallsByModel      map[string]int64
	llmErrorsByModule    map[string]int64

	// Sync metrics
	syncTotal    map[string]int64 // pipeline → count
	syncErrors   map[string]int64 // pipeline → errors
	syncDurations map[string][]float64 // pipeline → durations

	// RAG metrics
	ragSearchesTotal     int64
	ragEmbeddingsTotal   int64
	ragIndexChunksTotal  int64

	// EventBus metrics
	eventsPublished      int64
	eventsConsumed       int64
	eventsByType         map[string]int64
	eventsConsumerErrors int64
	eventPublishedByType  map[string]int64
	eventConsumedByType   map[string]int64

	// Cron metrics
	cronJobs      map[string]*cronJobState
	cronRunsTotal map[string]int64 // job → total runs
	cronRunErrors map[string]int64 // job → errors

	// Latency histograms
	histBuckets      []float64
	histLock         sync.Mutex
	requestDurations map[string][]float64
	dbDurations      []float64
	llmDurations     []float64

	// Active requests gauge
	activeRequests int64

	// Infrastructure health gauges (atomic 0/1)
	postgresUp int32
	redisUp    int32
	qdrantUp   int32
	ollamaUp   int32

	// Start time
	startTime time.Time
}

type cronJobState struct {
	mu          sync.Mutex
	lastRun     time.Time
	lastSuccess time.Time
	running     bool
	failCount   int64
	successCount int64
}

// NewCollector creates a new Collector with default histogram buckets.
func NewCollector() *Collector {
	return &Collector{
		requestsByStatus:     make(map[int]int64),
		requestsByMethod:     make(map[string]int64),
		requestsByPath:       make(map[string]int64),
		requestDurations:     make(map[string][]float64),
		eventsByType:         make(map[string]int64),
		cronJobs:             make(map[string]*cronJobState),
		llmCallsByModule:     make(map[string]int64),
		llmTokensInByModule:  make(map[string]int64),
		llmTokensOutByModule: make(map[string]int64),
		llmCallsByModel:      make(map[string]int64),
		llmErrorsByModule:    make(map[string]int64),
		syncTotal:            make(map[string]int64),
		syncErrors:           make(map[string]int64),
		syncDurations:        make(map[string][]float64),
		eventPublishedByType: make(map[string]int64),
		eventConsumedByType:  make(map[string]int64),
		cronRunsTotal:        make(map[string]int64),
		cronRunErrors:        make(map[string]int64),
		histBuckets:          []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
		startTime:            time.Now(),
	}
}

// ── Cron State ────────────────────────────────────────────────────────────

func (c *Collector) cronState(name string) *cronJobState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.cronJobs[name]; ok {
		return s
	}
	s := &cronJobState{}
	c.cronJobs[name] = s
	return s
}

// RecordCronStart marks a cron job as running.
func (c *Collector) RecordCronStart(job string) {
	s := c.cronState(job)
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
}

// RecordCronEnd marks a cron job as completed.
func (c *Collector) RecordCronEnd(job string, success bool) {
	s := c.cronState(job)
	s.mu.Lock()
	s.running = false
	s.lastRun = time.Now()
	if success {
		s.lastSuccess = time.Now()
		s.successCount++
	} else {
		s.failCount++
	}
	s.mu.Unlock()
}

// RecordEventByType records an event by its type.
func (c *Collector) RecordEventByType(eventType string) {
	c.mu.Lock()
	c.eventsByType[eventType]++
	c.mu.Unlock()
}

// ── HTTP Metrics ────────────────────────────────────────────────────────────

// RecordRequest records an HTTP request.
func (c *Collector) RecordRequest(method, path string, statusCode int, durationMs float64) {
	atomic.AddInt64(&c.requestsTotal, 1)
	atomic.AddInt64(&c.activeRequests, -1)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.requestsByStatus[statusCode]++
	c.requestsByMethod[method]++
	c.requestsByPath[path]++

	if statusCode >= 400 {
		atomic.AddInt64(&c.errorsTotal, 1)
	}

	c.histLock.Lock()
	c.requestDurations[method] = append(c.requestDurations[method], durationMs)
	c.histLock.Unlock()
}

// IncActiveRequests increments the active request count.
func (c *Collector) IncActiveRequests() {
	atomic.AddInt64(&c.activeRequests, 1)
}

// ── DB Metrics ──────────────────────────────────────────────────────────────

// ── LLM Metrics ──────────────────────────────────────────────────────────────

// RecordLLMCall implements LLMMetricsCollector. Records LLM call metrics.
func (c *Collector) RecordLLMCall(model string, durationMs float64, tokensIn, tokensOut int, hasError bool) {
	atomic.AddInt64(&c.externalCallsTotal, 1)
	atomic.AddInt64(&c.externalCallDurationSum, int64(durationMs*1000))
	atomic.AddInt64(&c.externalCallCount, 1)
	if hasError {
		atomic.AddInt64(&c.externalCallErrors, 1)
	}

	c.mu.Lock()
	c.llmCalls++
	c.llmTokensIn += int64(tokensIn)
	c.llmTokensOut += int64(tokensOut)
	if model != "" {
		c.llmCallsByModel[model]++
	}
	c.mu.Unlock()

	c.histLock.Lock()
	c.llmDurations = append(c.llmDurations, durationMs)
	c.histLock.Unlock()
}

// RecordLLMCallByModule records an LLM call with module attribution (e.g., "salesagent", "product_qa").
func (c *Collector) RecordLLMCallByModule(module, model string, tokensIn, tokensOut int, durationMs float64, hasError bool) {
	c.RecordLLMCall(model, durationMs, tokensIn, tokensOut, hasError)

	c.mu.Lock()
	if module != "" {
		c.llmCallsByModule[module]++
		c.llmTokensInByModule[module] += int64(tokensIn)
		c.llmTokensOutByModule[module] += int64(tokensOut)
		if hasError {
			c.llmErrorsByModule[module]++
		}
	}
	c.mu.Unlock()
}

// ── Sync Metrics ─────────────────────────────────────────────────────────────

// RecordSync records a pipeline sync completion.
func (c *Collector) RecordSync(pipeline string, count int64, durationMs float64, hasError bool) {
	c.mu.Lock()
	c.syncTotal[pipeline] += count
	if hasError {
		c.syncErrors[pipeline]++
	}
	c.mu.Unlock()

	c.histLock.Lock()
	c.syncDurations[pipeline] = append(c.syncDurations[pipeline], durationMs)
	c.histLock.Unlock()
}

// ── Cron Metrics (extended) ──────────────────────────────────────────────────

// RecordCronRun records a cron job run with result.
func (c *Collector) RecordCronRun(job string, success bool, durationMs float64) {
	c.mu.Lock()
	c.cronRunsTotal[job]++
	if !success {
		c.cronRunErrors[job]++
	}
	c.mu.Unlock()

	// Also update the existing cron state
	if success {
		c.RecordCronEnd(job, true)
	} else {
		c.RecordCronEnd(job, false)
	}
}

// ── EventBus Metrics (extended by type) ──────────────────────────────────────

// RecordEventPublishedByType records a published event with its type.
func (c *Collector) RecordEventPublishedByType(eventType string) {
	atomic.AddInt64(&c.eventsPublished, 1)
	c.mu.Lock()
	c.eventPublishedByType[eventType]++
	c.eventsByType[eventType]++
	c.mu.Unlock()
}

// RecordEventConsumedByType records a consumed event with its type.
func (c *Collector) RecordEventConsumedByType(eventType string) {
	atomic.AddInt64(&c.eventsConsumed, 1)
	c.mu.Lock()
	c.eventConsumedByType[eventType]++
	c.eventsByType[eventType]++
	c.mu.Unlock()
}

// ── DB Metrics ──────────────────────────────────────────────────────────────

// RecordDBQuery records a database query execution.
func (c *Collector) RecordDBQuery(durationMs float64, hasError bool) {
	atomic.AddInt64(&c.dbQueriesTotal, 1)
	atomic.AddInt64(&c.dbQueryDurationSum, int64(durationMs*1000))
	atomic.AddInt64(&c.dbQueryDurationCount, 1)

	if hasError {
		atomic.AddInt64(&c.dbQueryErrors, 1)
	}

	c.histLock.Lock()
	c.dbDurations = append(c.dbDurations, durationMs)
	c.histLock.Unlock()
}

// ── External API Metrics ────────────────────────────────────────────────────

// RecordExternalCall records an external API call (DashScope, ShipEngine, etc).
func (c *Collector) RecordExternalCall(service string, durationMs float64, hasError bool) {
	atomic.AddInt64(&c.externalCallsTotal, 1)
	atomic.AddInt64(&c.externalCallDurationSum, int64(durationMs*1000))
	atomic.AddInt64(&c.externalCallCount, 1)
	if hasError {
		atomic.AddInt64(&c.externalCallErrors, 1)
	}
}

// ── RAG Metrics ─────────────────────────────────────────────────────────────

// RecordRAGSearch records a RAG semantic search.
func (c *Collector) RecordRAGSearch() { atomic.AddInt64(&c.ragSearchesTotal, 1) }

// RecordRAGEmbedding records an embedding computation.
func (c *Collector) RecordRAGEmbedding(count int) { atomic.AddInt64(&c.ragEmbeddingsTotal, int64(count)) }

// RecordRAGIndexChunk records an indexed chunk.
func (c *Collector) RecordRAGIndexChunk(count int) { atomic.AddInt64(&c.ragIndexChunksTotal, int64(count)) }

// ── EventBus Metrics ────────────────────────────────────────────────────────

// RecordEventPublished records a published event.
func (c *Collector) RecordEventPublished() { atomic.AddInt64(&c.eventsPublished, 1) }

// RecordEventConsumed records a consumed event.
func (c *Collector) RecordEventConsumed() { atomic.AddInt64(&c.eventsConsumed, 1) }

// ── Prometheus Exposition ───────────────────────────────────────────────────

// Prometheus returns all metrics in Prometheus text format.

// ── Infrastructure Health ─────────────────────────────────────────────────

func (c *Collector) SetPostgresUp(up bool) { c.setHealth(&c.postgresUp, up) }
func (c *Collector) SetRedisUp(up bool)    { c.setHealth(&c.redisUp, up) }
func (c *Collector) SetQdrantUp(up bool)   { c.setHealth(&c.qdrantUp, up) }
func (c *Collector) SetOllamaUp(up bool)   { c.setHealth(&c.ollamaUp, up) }
func (c *Collector) setHealth(field *int32, up bool) {
	if up { atomic.StoreInt32(field, 1) } else { atomic.StoreInt32(field, 0) }
}

func (c *Collector) postgresUpVal() int32 { return atomic.LoadInt32(&c.postgresUp) }
func (c *Collector) redisUpVal() int32    { return atomic.LoadInt32(&c.redisUp) }
func (c *Collector) qdrantUpVal() int32   { return atomic.LoadInt32(&c.qdrantUp) }
func (c *Collector) ollamaUpVal() int32   { return atomic.LoadInt32(&c.ollamaUp) }

func (c *Collector) Prometheus() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var b strings.Builder

	// HELP + TYPE + metric lines for each metric
	c.writeCounter(&b, "vela_requests_total", "Total HTTP requests",
		atomic.LoadInt64(&c.requestsTotal))
	c.writeCounter(&b, "vela_errors_total", "Total HTTP errors (status >= 400)",
		atomic.LoadInt64(&c.errorsTotal))
	c.writeGauge(&b, "vela_active_requests", "Currently active requests",
		float64(atomic.LoadInt64(&c.activeRequests)))

	// Request by status
	b.WriteString("# HELP vela_requests_by_status HTTP requests by status code\n")
	b.WriteString("# TYPE vela_requests_by_status counter\n")
	for status, count := range c.requestsByStatus {
		fmt.Fprintf(&b, "vela_requests_by_status{status=\"%d\"} %d\n", status, count)
	}
	b.WriteString("\n")

	// Request by method
	b.WriteString("# HELP vela_requests_by_method HTTP requests by method\n")
	b.WriteString("# TYPE vela_requests_by_method counter\n")
	for method, count := range c.requestsByMethod {
		fmt.Fprintf(&b, "vela_requests_by_method{method=\"%s\"} %d\n", method, count)
	}
	b.WriteString("\n")
	b.WriteString("# HELP vela_requests_by_endpoint HTTP requests by endpoint\n")
	b.WriteString("# TYPE vela_requests_by_endpoint counter\n")
	for path, count := range c.requestsByPath {
		fmt.Fprintf(&b, "vela_requests_by_endpoint{endpoint=\"%s\"} %d\n", path, count)
	}


	// DB metrics
	dbTotal := atomic.LoadInt64(&c.dbQueriesTotal)
	dbErrors := atomic.LoadInt64(&c.dbQueryErrors)
	c.writeCounter(&b, "vela_db_queries_total", "Total DB queries", dbTotal)
	c.writeCounter(&b, "vela_db_query_errors_total", "Total DB query errors", dbErrors)

	dbCount := atomic.LoadInt64(&c.dbQueryDurationCount)
	if dbCount > 0 {
		avg := float64(atomic.LoadInt64(&c.dbQueryDurationSum)) / float64(dbCount) / 1000
		fmt.Fprintf(&b, "vela_db_query_duration_avg_ms %.2f\n", avg)
	}
	b.WriteString("\n")

	// External API
	extTotal := atomic.LoadInt64(&c.externalCallsTotal)
	extErrors := atomic.LoadInt64(&c.externalCallErrors)
	c.writeCounter(&b, "vela_external_calls_total", "Total external API calls", extTotal)
	c.writeCounter(&b, "vela_external_call_errors_total", "Total external API call errors", extErrors)

	extCount := atomic.LoadInt64(&c.externalCallCount)
	if extCount > 0 {
		avg := float64(atomic.LoadInt64(&c.externalCallDurationSum)) / float64(extCount) / 1000
		fmt.Fprintf(&b, "vela_external_call_duration_avg_ms %.2f\n", avg)
	}
	b.WriteString("\n")

	// RAG
	c.writeCounter(&b, "vela_rag_searches_total", "Total RAG searches",
		atomic.LoadInt64(&c.ragSearchesTotal))
	c.writeCounter(&b, "vela_rag_embeddings_total", "Total embeddings computed",
		atomic.LoadInt64(&c.ragEmbeddingsTotal))
	c.writeCounter(&b, "vela_rag_index_chunks_total", "Total chunks indexed",
		atomic.LoadInt64(&c.ragIndexChunksTotal))
	b.WriteString("\n")

	// EventBus
	c.writeCounter(&b, "vela_events_published_total", "Total events published",
		atomic.LoadInt64(&c.eventsPublished))
	c.writeCounter(&b, "vela_events_consumed_total", "Total events consumed",
		atomic.LoadInt64(&c.eventsConsumed))
	b.WriteString("# HELP vela_events_by_type Events by type\n")
	b.WriteString("# TYPE vela_events_by_type counter\n")
	for eventType, count := range c.eventsByType {
		fmt.Fprintf(&b, "vela_events_by_type{type=\"%s\"} %d\n", eventType, count)
	}
	// Per-type with direction
	b.WriteString("# HELP vela_events_total Events by type and direction\n")
	b.WriteString("# TYPE vela_events_total counter\n")
	for eventType, count := range c.eventPublishedByType {
		fmt.Fprintf(&b, "vela_events_total{type=\"%s\",direction=\"published\"} %d\n", eventType, count)
	}
	for eventType, count := range c.eventConsumedByType {
		fmt.Fprintf(&b, "vela_events_total{type=\"%s\",direction=\"consumed\"} %d\n", eventType, count)
	}
	b.WriteString("\n")

	// LLM
	c.mu.RLock()
	llmCalls := c.llmCalls
	llmTokensIn := c.llmTokensIn
	llmTokensOut := c.llmTokensOut
	c.mu.RUnlock()
	c.writeCounter(&b, "vela_llm_calls_total", "Total LLM calls", llmCalls)
	c.writeCounter(&b, "vela_llm_tokens_input_total", "Total input tokens", llmTokensIn)
	c.writeCounter(&b, "vela_llm_tokens_output_total", "Total output tokens", llmTokensOut)

	// LLM by module
	c.mu.RLock()
	for module, count := range c.llmCallsByModule {
		errors := c.llmErrorsByModule[module]
		fmt.Fprintf(&b, "vela_llm_calls_by_module{module=\"%s\",status=\"ok\"} %d\n", module, count-errors)
		if errors > 0 {
			fmt.Fprintf(&b, "vela_llm_calls_by_module{module=\"%s\",status=\"error\"} %d\n", module, errors)
		}
		fmt.Fprintf(&b, "vela_llm_tokens_input_by_module{module=\"%s\"} %d\n", module, c.llmTokensInByModule[module])
		fmt.Fprintf(&b, "vela_llm_tokens_output_by_module{module=\"%s\"} %d\n", module, c.llmTokensOutByModule[module])
	}
	// LLM by model
	for model, count := range c.llmCallsByModel {
		fmt.Fprintf(&b, "vela_llm_calls_by_model{model=\"%s\"} %d\n", model, count)
	}
	c.mu.RUnlock()
	b.WriteString("\n")

	// Sync metrics
	c.mu.RLock()
	b.WriteString("# HELP vela_sync_total Sync pipeline events\n")
	b.WriteString("# TYPE vela_sync_total counter\n")
	for pipeline, count := range c.syncTotal {
		errors := c.syncErrors[pipeline]
		fmt.Fprintf(&b, "vela_sync_total{pipeline=\"%s\",status=\"success\"} %d\n", pipeline, count-errors)
		if errors > 0 {
			fmt.Fprintf(&b, "vela_sync_total{pipeline=\"%s\",status=\"error\"} %d\n", pipeline, errors)
		}
	}
	c.mu.RUnlock()
	b.WriteString("\n")

	// Histograms (histLock prevents data race with Record methods)
	c.histLock.Lock()
	c.writeHistogram(&b, "vela_request_duration_ms", "Request latency in ms",
		c.requestDurations)
	c.writeHistogram(&b, "vela_db_query_duration_ms", "DB query latency in ms",
		map[string][]float64{"": c.dbDurations})
	c.writeHistogram(&b, "vela_llm_call_duration_ms", "LLM call latency",
		map[string][]float64{"": c.llmDurations})
	c.writeHistogram(&b, "vela_sync_duration_ms", "Sync pipeline latency",
		c.syncDurations)
	c.histLock.Unlock()

	// Cron runs total
	c.mu.RLock()
	b.WriteString("# HELP vela_cron_runs_total Cron job runs\n")
	b.WriteString("# TYPE vela_cron_runs_total counter\n")
	for job, count := range c.cronRunsTotal {
		errors := c.cronRunErrors[job]
		fmt.Fprintf(&b, "vela_cron_runs_total{job=\"%s\",status=\"success\"} %d\n", job, count-errors)
		if errors > 0 {
			fmt.Fprintf(&b, "vela_cron_runs_total{job=\"%s\",status=\"error\"} %d\n", job, errors)
		}
	}
	c.mu.RUnlock()

	// Cron (snapshot to avoid holding c.mu.RLock during per-job state.mu.Lock)
	type cs struct{ r int; s, f int64 }
	snap := make(map[string]cs)
	for job, state := range c.cronJobs {
		state.mu.Lock()
		r := 0
		if state.running { r = 1 }
		snap[job] = cs{r, state.successCount, state.failCount}
		state.mu.Unlock()
	}
	for job, s := range snap {
		fmt.Fprintf(&b, "vela_cron_job_running{job=\"%s\"} %d\n", job, s.r)
		fmt.Fprintf(&b, "vela_cron_job_success_total{job=\"%s\"} %d\n", job, s.s)
		fmt.Fprintf(&b, "vela_cron_job_fail_total{job=\"%s\"} %d\n", job, s.f)
	}

	// Infrastructure health
	fmt.Fprintf(&b, "vela_postgres_up %d\n", c.postgresUpVal())
	fmt.Fprintf(&b, "vela_redis_up %d\n", c.redisUpVal())
	fmt.Fprintf(&b, "vela_qdrant_up %d\n", c.qdrantUpVal())
	fmt.Fprintf(&b, "vela_ollama_up %d\n", c.ollamaUpVal())
	b.WriteString("\n")

	// Runtime
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	// Go runtime metrics — see dedicated section below

	// Uptime
	fmt.Fprintf(&b, "# HELP vela_uptime_seconds Server uptime\n")
	fmt.Fprintf(&b, "# TYPE vela_uptime_seconds gauge\n")
	fmt.Fprintf(&b, "vela_uptime_seconds %.0f\n", time.Since(c.startTime).Seconds())

	// ── Go Runtime Metrics ──
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	gcStats := debug.GCStats{}
	debug.ReadGCStats(&gcStats)

	var lastGcPause float64
	if gcStats.PauseQuantiles != nil && len(gcStats.PauseQuantiles) > 0 {
		lastGcPause = float64(gcStats.PauseQuantiles[0].Microseconds()) / 1000.0
	}

	fmt.Fprintf(&b, "# HELP go_goroutines Number of goroutines\n")
	fmt.Fprintf(&b, "# TYPE go_goroutines gauge\n")
	fmt.Fprintf(&b, "go_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintf(&b, "# HELP go_threads Number of OS threads\n")
	fmt.Fprintf(&b, "# TYPE go_threads gauge\n")
	fmt.Fprintf(&b, "go_threads %d\n", runtime.GOMAXPROCS(0))
	fmt.Fprintf(&b, "# HELP go_memstats_alloc_bytes Number of bytes allocated\n")
	fmt.Fprintf(&b, "# TYPE go_memstats_alloc_bytes gauge\n")
	fmt.Fprintf(&b, "go_memstats_alloc_bytes %d\n", memStats.Alloc)
	fmt.Fprintf(&b, "# HELP go_memstats_heap_inuse_bytes Bytes in use\n")
	fmt.Fprintf(&b, "# TYPE go_memstats_heap_inuse_bytes gauge\n")
	fmt.Fprintf(&b, "go_memstats_heap_inuse_bytes %d\n", memStats.HeapInuse)
	fmt.Fprintf(&b, "# HELP go_memstats_heap_objects Number of allocated objects\n")
	fmt.Fprintf(&b, "# TYPE go_memstats_heap_objects gauge\n")
	fmt.Fprintf(&b, "go_memstats_heap_objects %d\n", memStats.HeapObjects)
	fmt.Fprintf(&b, "# HELP go_memstats_sys_bytes Bytes from OS\n")
	fmt.Fprintf(&b, "# TYPE go_memstats_sys_bytes gauge\n")
	fmt.Fprintf(&b, "go_memstats_sys_bytes %d\n", memStats.Sys)
	fmt.Fprintf(&b, "# HELP go_gc_duration_seconds GC pause duration\n")
	fmt.Fprintf(&b, "# TYPE go_gc_duration_seconds gauge\n")
	fmt.Fprintf(&b, "go_gc_duration_seconds %.6f\n", lastGcPause/1000.0)
	fmt.Fprintf(&b, "# HELP go_gc_count Total GC cycles\n")
	fmt.Fprintf(&b, "# TYPE go_gc_count counter\n")
	fmt.Fprintf(&b, "go_gc_count %d\n", memStats.NumGC)

	return b.String()
}

// ── Prometheus Formatters ────────────────────────────────────────────────────

func (c *Collector) writeCounter(b *strings.Builder, name, help string, value int64) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s counter\n", name)
	fmt.Fprintf(b, "%s %d\n\n", name, value)
}

func (c *Collector) writeGauge(b *strings.Builder, name, help string, value float64) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s gauge\n", name)
	fmt.Fprintf(b, "%s %.0f\n\n", name, value)
}

func (c *Collector) writeHistogram(b *strings.Builder, name, help string, data map[string][]float64) {
	b.WriteString(fmt.Sprintf("# HELP %s %s\n", name, help))
	b.WriteString(fmt.Sprintf("# TYPE %s summary\n", name))

	for label, values := range data {
		if len(values) == 0 { continue }
		sort.Float64s(values)

		p50 := percentile(values, 0.50)
		p95 := percentile(values, 0.95)
		p99 := percentile(values, 0.99)
		avg := average(values)

		labelStr := ""
		if label != "" {
			labelStr = fmt.Sprintf("method=\"%s\"", label)
		}

		renderQuantile := func(q string, val float64) string {
			if labelStr != "" {
				return fmt.Sprintf("%s{%s,quantile=\"%s\"} %.2f\n", name, labelStr, q, val)
			}
			return fmt.Sprintf("%s{quantile=\"%s\"} %.2f\n", name, q, val)
		}

		fmt.Fprint(b, renderQuantile("0.5", p50))
		fmt.Fprint(b, renderQuantile("0.95", p95))
		fmt.Fprint(b, renderQuantile("0.99", p99))
		if labelStr != "" {
			fmt.Fprintf(b, "%s_avg{%s} %.2f\n", name, labelStr, avg)
			fmt.Fprintf(b, "%s_count{%s} %d\n\n", name, labelStr, len(values))
		} else {
			fmt.Fprintf(b, "%s_avg %.2f\n", name, avg)
			fmt.Fprintf(b, "%s_count %d\n\n", name, len(values))
		}
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 { return 0 }
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 { idx = 0 }
	if idx >= len(sorted) { idx = len(sorted) - 1 }
	return sorted[idx]
}

func average(values []float64) float64 {
	if len(values) == 0 { return 0 }
	var sum float64
	for _, v := range values { sum += v }
	return sum / float64(len(values))
}
