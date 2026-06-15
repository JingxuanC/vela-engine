package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"gorm.io/gorm"
)

// ContactHandler handles contact form submissions.
type ContactHandler struct {
	db    *gorm.DB
	email *service.ResendClient
}

// NewContactHandler creates a new ContactHandler.
func NewContactHandler(db *gorm.DB, email *service.ResendClient) *ContactHandler {
	return &ContactHandler{db: db, email: email}
}

// ContactSubmitRequest is the contact form payload.
type ContactSubmitRequest struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Message string `json:"message"`
}

// Submit handles POST /api/contact.
func (h *ContactHandler) Submit(w http.ResponseWriter, r *http.Request) {
	var req ContactSubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.Name == "" || req.Email == "" || req.Message == "" {
		httputil.WriteError(w, http.StatusBadRequest, "name, email, and message are required")
		return
	}

	// Save to database
	msg := model.ContactMessage{
		Name:    req.Name,
		Email:   req.Email,
		Message: req.Message,
	}

	if err := h.db.WithContext(r.Context()).Create(&msg).Error; err != nil {
		slog.Error("contact: failed to save message", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to save message")
		return
	}

	// Send email asynchronously (don't block response)
	if h.email != nil {
		go func() {
		defer func(){if r:=recover();r!=nil{slog.Error("panic in goroutine","error",r)}}()
			if err := h.email.SendContactNotification(req.Name, req.Email, req.Message); err != nil {
				slog.Warn("contact: failed to send email notification", "error", err, "name", req.Name, "email", req.Email)
			}
		}()
	}

	slog.Info("contact: message submitted", "name", req.Name, "email", req.Email)
	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"message": "Thanks! We'll get back to you within 24h.",
	})
}
