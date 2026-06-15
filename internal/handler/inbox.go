package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// ctxKey for ShopID — synced with gateway middleware.
type inboxCtxKey string

const ctxKeyInboxShopID inboxCtxKey = "shop_id"

// InboxHandler handles merchant inbox — manual customer conversation replies.
type InboxHandler struct {
	db *gorm.DB
}

// NewInboxHandler creates an InboxHandler with a DB connection.
func NewInboxHandler(db *gorm.DB) *InboxHandler {
	return &InboxHandler{db: db}
}

// --- DTOs ---

// ConversationSummary is the list-view item for GET /api/inbox/conversations.
type ConversationSummary struct {
	ID            uuid.UUID `json:"id"`
	CustomerEmail string    `json:"customer_email"`
	CustomerName  string    `json:"customer_name"`
	Status        string    `json:"status"`
	MessageCount  int       `json:"message_count"`
	LastMessage   string    `json:"last_message"`
	Source        string    `json:"source"`
	CreatedAt     string    `json:"created_at"`
	UpdatedAt     string    `json:"updated_at"`
}

// ConversationDetail is the full response for GET /api/inbox/conversations/{id}.
type ConversationDetail struct {
	ID            uuid.UUID       `json:"id"`
	CustomerEmail string          `json:"customer_email"`
	CustomerName  string          `json:"customer_name"`
	Status        string          `json:"status"`
	Messages      json.RawMessage `json:"messages"`
	Source        string          `json:"source"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
}

// ReplyRequest is the body for POST /api/inbox/conversations/{id}/reply.
type ReplyRequest struct {
	Message string `json:"message"`
}

// ReplyResponse is returned after a successful reply.
type ReplyResponse struct {
	Success        bool   `json:"success"`
	ConversationID string `json:"conversation_id"`
	Message        string `json:"message"`
}

// --- Handlers ---

// ListConversations handles GET /api/inbox/conversations?status=active.
func (h *InboxHandler) ListConversations(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	shopID, err := h.resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusForbidden, err.Error())
		return
	}

	status := r.URL.Query().Get("status")
	if status == "" {
		status = "active"
	}

	var conversations []model.CustomerConversation
	if err := h.db.WithContext(r.Context()).
		Where("shop_id = ? AND status = ?", shopID, status).
		Order("updated_at DESC").
		Find(&conversations).Error; err != nil {
		slog.Error("inbox: failed to list conversations", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to list conversations")
		return
	}

	summaries := make([]ConversationSummary, 0, len(conversations))
	for _, c := range conversations {
		msgCount, lastMsg := extractMessageSummary(c.Messages)
		summaries = append(summaries, ConversationSummary{
			ID:            c.ID,
			CustomerEmail: c.CustomerEmail,
			CustomerName:  c.CustomerName,
			Status:        c.Status,
			MessageCount:  msgCount,
			LastMessage:   lastMsg,
			Source:        c.Source,
			CreatedAt:     c.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt:     c.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success":       true,
		"conversations": summaries,
		"total":         len(summaries),
	})
}

// GetConversation handles GET /api/inbox/conversations/{id}.
func (h *InboxHandler) GetConversation(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid conversation ID")
		return
	}

	shopID, err := h.resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusForbidden, err.Error())
		return
	}

	var conv model.CustomerConversation
	if err := h.db.WithContext(r.Context()).
		Where("id = ? AND shop_id = ?", id, shopID).
		First(&conv).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "Conversation not found")
			return
		}
		slog.Error("inbox: failed to get conversation", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to get conversation")
		return
	}

	httputil.WriteOK(w, ConversationDetail{
		ID:            conv.ID,
		CustomerEmail: conv.CustomerEmail,
		CustomerName:  conv.CustomerName,
		Status:        conv.Status,
		Messages:      json.RawMessage(conv.Messages),
		Source:        conv.Source,
		CreatedAt:     conv.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:     conv.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// Reply handles POST /api/inbox/conversations/{id}/reply.
func (h *InboxHandler) Reply(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid conversation ID")
		return
	}

	shopID, err := h.resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusForbidden, err.Error())
		return
	}

	var req ReplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.Message == "" {
		httputil.WriteError(w, http.StatusBadRequest, "message is required")
		return
	}

	var conv model.CustomerConversation
	if err := h.db.WithContext(r.Context()).
		Where("id = ? AND shop_id = ?", id, shopID).
		First(&conv).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "Conversation not found")
			return
		}
		slog.Error("inbox: failed to get conversation for reply", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to get conversation")
		return
	}

	// Append the merchant reply message to the JSONB messages array.
	newMessages, err := appendReplyMessage(conv.Messages, req.Message, r.Context().Value(ctxKeyInboxShopID))
	if err != nil {
		slog.Error("inbox: failed to append reply message", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to process message")
		return
	}

	if err := h.db.WithContext(r.Context()).Model(&conv).Updates(map[string]interface{}{
		"messages":   newMessages,
		"updated_at": gorm.Expr("NOW()"),
	}).Error; err != nil {
		slog.Error("inbox: failed to save reply", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to save reply")
		return
	}

	httputil.WriteOK(w, ReplyResponse{
		Success:        true,
		ConversationID: id.String(),
		Message:        req.Message,
	})
}

// Resolve handles POST /api/inbox/conversations/{id}/resolve.
func (h *InboxHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid conversation ID")
		return
	}

	shopID, err := h.resolveShopID(r)
	if err != nil {
		httputil.WriteError(w, http.StatusForbidden, err.Error())
		return
	}

	var conv model.CustomerConversation
	if err := h.db.WithContext(r.Context()).
		Where("id = ? AND shop_id = ?", id, shopID).
		First(&conv).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "Conversation not found")
			return
		}
		slog.Error("inbox: failed to get conversation for resolve", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to get conversation")
		return
	}

	if conv.Status == "resolved" {
		httputil.WriteOK(w, map[string]interface{}{
			"success":         true,
			"conversation_id": id.String(),
			"status":          "resolved",
			"message":         "Conversation already resolved",
		})
		return
	}

	// Append a system "resolved" note to messages before closing.
	resolvedMessages, _ := appendReplyMessage(conv.Messages, "【系统】此对话已标记为已解决", "system")

	if err := h.db.WithContext(r.Context()).Model(&conv).Updates(map[string]interface{}{
		"status":     "resolved",
		"messages":   resolvedMessages,
		"updated_at": gorm.Expr("NOW()"),
	}).Error; err != nil {
		slog.Error("inbox: failed to resolve conversation", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to resolve conversation")
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success":         true,
		"conversation_id": id.String(),
		"status":          "resolved",
	})
}

// --- Helpers ---

// resolveShopID extracts and validates the ShopID from the request context.
// Priority: context (set by gateway middleware) > X-Shop-ID header > shop_id query param.
func (h *InboxHandler) resolveShopID(r *http.Request) (uuid.UUID, error) {
	// 1. Try context (set by gateway middleware)
	if ctxShopID := r.Context().Value(ctxKeyInboxShopID); ctxShopID != nil {
		if s, ok := ctxShopID.(string); ok && s != "" {
			if id, err := uuid.Parse(s); err == nil {
				return id, nil
			}
		}
	}

	// 2. Try X-Shop-ID header
	if shopFromHeader := r.Header.Get("X-Shop-ID"); shopFromHeader != "" {
		if id, err := uuid.Parse(shopFromHeader); err == nil {
			return id, nil
		}
	}

	// 3. Try shop_id query param
	if shopFromQuery := r.URL.Query().Get("shop_id"); shopFromQuery != "" {
		if id, err := uuid.Parse(shopFromQuery); err == nil {
			return id, nil
		}
	}

	return uuid.Nil, errors.New("shop_id is required (set X-Shop-ID header or shop_id query param)")
}

// InboxMessage models a single message in the messages JSONB array.
type InboxMessage struct {
	Role      string `json:"role"`      // "customer", "merchant", "ai", "system"
	Content   string `json:"content"`   // message text
	Timestamp string `json:"timestamp"` // ISO 8601
}

// extractMessageSummary parses the JSONB messages and returns count + last message content.
func extractMessageSummary(msgs datatypes.JSON) (int, string) {
	if len(msgs) == 0 {
		return 0, ""
	}
	var parsed []InboxMessage
	if err := json.Unmarshal(msgs, &parsed); err != nil {
		return 0, ""
	}
	if len(parsed) == 0 {
		return 0, ""
	}
	last := parsed[len(parsed)-1].Content
	return len(parsed), last
}

// appendReplyMessage appends a merchant reply to the JSONB messages array.
func appendReplyMessage(msgs datatypes.JSON, content string, role interface{}) (datatypes.JSON, error) {
	var parsed []InboxMessage
	if len(msgs) > 0 {
		if err := json.Unmarshal(msgs, &parsed); err != nil {
			parsed = []InboxMessage{}
		}
	}

	roleStr := "merchant"
	if role != nil {
		if s, ok := role.(string); ok && s != "" {
			roleStr = s
		}
	}

	parsed = append(parsed, InboxMessage{
		Role:      roleStr,
		Content:   content,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	raw, err := json.Marshal(parsed)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(raw), nil
}
