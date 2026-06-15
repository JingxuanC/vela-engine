package observability

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
)

// GormPlugin records DB query metrics and attaches trace IDs to logs.
type GormPlugin struct {
	collector *Collector
}

// NewGormPlugin creates a GormPlugin.
func NewGormPlugin(collector *Collector) *GormPlugin {
	return &GormPlugin{collector: collector}
}

// Name returns the plugin name.
func (p *GormPlugin) Name() string { return "observability" }

// Initialize registers GORM callbacks.
func (p *GormPlugin) Initialize(db *gorm.DB) error {
	// Before query: record start time + inject trace context for logging
	db.Callback().Query().Before("gorm:query").Register("obs:before_query", p.beforeSQL)
	db.Callback().Create().Before("gorm:create").Register("obs:before_create", p.beforeSQL)
	db.Callback().Update().Before("gorm:update").Register("obs:before_update", p.beforeSQL)
	db.Callback().Delete().Before("gorm:delete").Register("obs:before_delete", p.beforeSQL)
	db.Callback().Row().Before("gorm:row").Register("obs:before_row", p.beforeSQL)
	db.Callback().Raw().Before("gorm:raw").Register("obs:before_raw", p.beforeSQL)

	// After query: record duration + errors
	db.Callback().Query().After("gorm:query").Register("obs:after_query", p.afterSQL)
	db.Callback().Create().After("gorm:create").Register("obs:after_create", p.afterSQL)
	db.Callback().Update().After("gorm:update").Register("obs:after_update", p.afterSQL)
	db.Callback().Delete().After("gorm:delete").Register("obs:after_delete", p.afterSQL)
	db.Callback().Row().After("gorm:row").Register("obs:after_row", p.afterSQL)
	db.Callback().Raw().After("gorm:raw").Register("obs:after_raw", p.afterSQL)

	return nil
}

func (p *GormPlugin) beforeSQL(db *gorm.DB) {
	db.InstanceSet("obs:start_time", time.Now())
}

func (p *GormPlugin) afterSQL(db *gorm.DB) {
	start, ok := db.InstanceGet("obs:start_time")
	if !ok {
		return
	}
	duration := time.Since(start.(time.Time))
	durationMs := float64(duration.Microseconds()) / 1000.0

	// Extract operation and table name
	operation := "SQL"
	table := "unknown"
	stmt := db.Statement
	if stmt != nil {
		sql := stmt.SQL.String()
		if len(sql) >= 6 {
			operation = strings.ToUpper(sql[:6])
		}
		table = stmt.Table
	}

	// Extract source from context
	source := DBSourceFromContext(db.Statement.Context)

	// Record metric
	p.collector.RecordDBQuery(durationMs, db.Error != nil)

	// Log slow queries (>300ms)
	if durationMs > 300 {
		traceID := TraceIDFromContext(db.Statement.Context)
		slog.Warn("db: slow query",
			"trace_id", traceID,
			"duration_ms", durationMs,
			"table", table,
			"operation", operation,
			"source", source,
			"error", db.Error != nil,
		)
	}

	// Attach trace_id to GORM log output when available
	if traceID := TraceIDFromContext(db.Statement.Context); traceID != "" {
		_ = traceID // GORM doesn't support per-statement log context directly;
		// trace_id is attached via the context chain to slog output
	}

	_ = operation
	_ = table
	_ = source
}

// InjectSource is a helper to set db_source for GORM metrics.
func InjectSource(ctx context.Context, source string) context.Context {
	return WithDBSource(ctx, source)
}
