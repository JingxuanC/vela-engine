// Package httputil provides HTTP response helpers.
package httputil

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ErrorResponse is the standard error body for all API errors.
type ErrorResponse struct {
	Detail string `json:"detail"`
}

// WriteJSON writes v as JSON with the given HTTP status code.
func WriteJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// WriteError writes a standard error response.
func WriteError(w http.ResponseWriter, status int, detail string) {
	WriteJSON(w, status, ErrorResponse{Detail: detail})
}

// WriteOK writes a 200 JSON response.
func WriteOK(w http.ResponseWriter, v interface{}) {
	WriteJSON(w, http.StatusOK, v)
}

// WriteCreated writes a 201 JSON response.
func WriteCreated(w http.ResponseWriter, v interface{}) {
	WriteJSON(w, http.StatusCreated, v)
}

// WriteNoContent writes a 204 response with no body.
func WriteNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// ── SSE Streaming ────────────────────────────────────────────────────────────

// SSEWriter wraps http.ResponseWriter for Server-Sent Events.
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewSSEWriter sets SSE headers and returns a writer.
func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("http: ResponseWriter does not support flushing")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &SSEWriter{w: w, flusher: flusher}, nil
}

// WriteToken sends a single token via SSE.
func (s *SSEWriter) WriteToken(token string) error {
	data, _ := json.Marshal(map[string]string{"token": token})
	_, err := fmt.Fprintf(s.w, "data: %s\n\n", data)
	if err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// WriteDone signals the end of the SSE stream.
func (s *SSEWriter) WriteDone(meta map[string]interface{}) error {
	if meta == nil {
		meta = map[string]interface{}{"done": true}
	} else {
		meta["done"] = true
	}
	data, _ := json.Marshal(meta)
	_, err := fmt.Fprintf(s.w, "data: %s\n\ndata: [DONE]\n\n", data)
	if err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}
