// Package sse provides helpers for Server-Sent Events streaming.
package sse

import (
	"fmt"
	"io"
	"net/http"
)

// WriteEvent writes an SSE event frame with an optional event type.
// Format: "event: {eventType}\ndata: {data}\n\n"
func WriteEvent(w io.Writer, eventType string, data string) {
	if eventType != "" {
		fmt.Fprintf(w, "event: %s\n", eventType)
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
}

// WriteData writes a simple SSE data frame without an event type.
func WriteData(w io.Writer, data string) {
	fmt.Fprintf(w, "data: %s\n\n", data)
}

// WriteComment writes an SSE comment line (useful for keep-alive).
func WriteComment(w io.Writer, comment string) {
	fmt.Fprintf(w, ": %s\n\n", comment)
}

// SetSSEHeaders sets the standard SSE response headers.
func SetSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

// FlushWriter is an interface that combines io.Writer with http.Flusher.
type FlushWriter interface {
	io.Writer
	http.Flusher
}

// WriteAndFlush writes an SSE data frame and flushes it immediately.
func WriteAndFlush(w FlushWriter, data string) {
	WriteData(w, data)
	w.Flush()
}

// WriteEventAndFlush writes an SSE event frame and flushes it immediately.
func WriteEventAndFlush(w FlushWriter, eventType string, data string) {
	WriteEvent(w, eventType, data)
	w.Flush()
}
