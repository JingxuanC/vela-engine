package observability

import "context"

// Trace key for context injection.
type ctxKey string

const (
	ctxTraceID  ctxKey = "trace_id"
	ctxDBSource ctxKey = "db_source"
)

// WithTraceID adds a trace ID to the context.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, ctxTraceID, traceID)
}

// TraceIDFromContext extracts the trace ID from context.
func TraceIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxTraceID).(string); ok {
		return v
	}
	return ""
}

// WithDBSource annotates the context with the DB call source.
func WithDBSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, ctxDBSource, source)
}

// DBSourceFromContext extracts the DB source from context.
func DBSourceFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxDBSource).(string); ok {
		return v
	}
	return "unknown"
}
