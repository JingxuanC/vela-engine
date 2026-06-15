package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// SpeechHandler handles speech-to-text and text-to-speech via DashScope.
type SpeechHandler struct {
	router *service.LLMRouter
}

// NewSpeechHandler creates a new speech handler.
func NewSpeechHandler(router *service.LLMRouter) *SpeechHandler {
	return &SpeechHandler{router: router}
}

// Transcribe handles POST /api/speech/transcribe — speech audio to text.
func (h *SpeechHandler) Transcribe(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		httputil.WriteError(w, http.StatusBadRequest, "empty body")
		return
	}
	audioB64 := string(body)

	// Get the DashScope provider from the router
	p, _, err := h.router.GetProvider(r.Context(), parseUUID(r))
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "no speech provider: "+err.Error())
		return
	}
	ds, ok := p.(*service.DashScopeProvider)
	if !ok {
		httputil.WriteError(w, http.StatusInternalServerError, "speech requires dashscope provider")
		return
	}

	text, err := ds.Transcribe(r.Context(), audioB64)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "transcribe failed: "+err.Error())
		return
	}
	httputil.WriteOK(w, map[string]string{"text": text})
}

// Synthesize handles POST /api/speech/synthesize — text to speech audio.
func (h *SpeechHandler) Synthesize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Text == "" {
		httputil.WriteError(w, http.StatusBadRequest, "text required")
		return
	}

	p, _, err := h.router.GetProvider(r.Context(), parseUUID(r))
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "no speech provider: "+err.Error())
		return
	}
	ds, ok := p.(*service.DashScopeProvider)
	if !ok {
		httputil.WriteError(w, http.StatusInternalServerError, "speech requires dashscope provider")
		return
	}

	audio, err := ds.Synthesize(r.Context(), req.Text)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "synthesize failed: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Write(audio)
}

func parseUUID(r *http.Request) uuid.UUID {
	sid := r.URL.Query().Get("shop_id")
	if sid == "" {
		return uuid.Nil
	}
	id, _ := uuid.Parse(sid)
	return id
}

func decodeJSON(r *http.Request, target interface{}) error {
	return json.NewDecoder(r.Body).Decode(target)
}
