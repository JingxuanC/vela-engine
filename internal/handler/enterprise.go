package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	chi "github.com/go-chi/chi/v5"
	"github.com/JingxuanC/vela-engine/internal/service/enterprise"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"gorm.io/gorm"
)

type EnterpriseHandler struct {
	db *gorm.DB
	km *enterprise.KeyManager
}

func NewEnterpriseHandler(db *gorm.DB, km *enterprise.KeyManager) *EnterpriseHandler {
	return &EnterpriseHandler{db, km}
}
func (h *EnterpriseHandler) ListKeys(w http.ResponseWriter, r *http.Request) {
	sid := shopIDFromRequest(r)
	if sid == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id required")
		return
	}
	keys, _ := h.km.ListKeys(sid)
	items := make([]map[string]interface{}, len(keys))
	for i, k := range keys {
		items[i] = map[string]interface{}{"id": k.ID, "label": k.Label, "prefix": k.Prefix, "key_last_four": k.KeyLastFour, "rate_limit": k.RateLimit, "is_active": k.IsActive}
	}
	httputil.WriteOK(w, map[string]interface{}{"success": true, "keys": items})
}
func (h *EnterpriseHandler) CreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShopID    string   `json:"shop_id"`
		Label     string   `json:"label"`
		Scopes    []string `json:"scopes"`
		RateLimit int      `json:"rate_limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	s, _ := json.Marshal(req.Scopes)
	rl := req.RateLimit
	if rl <= 0 {
		rl = 60
	}
	result, err := h.km.GenerateKey(req.ShopID, req.Label, s, rl)
	if err != nil {
		httputil.WriteError(w, 500, err.Error())
		return
	}
	httputil.WriteJSON(w, 201, map[string]interface{}{"success": true, "full_key": result.FullKey, "prefix": result.Prefix, "id": result.ApiKey.ID})
}
func (h *EnterpriseHandler) RevokeKey(w http.ResponseWriter, r *http.Request) {
	kid := chi.URLParam(r, "id")
	sid := shopIDFromRequest(r)
	if sid == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id required")
		return
	}
	if err := h.km.RevokeKey(kid, sid); err != nil {
		slog.Error("enterprise: revoke key failed", "key_id", kid, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to revoke key")
		return
	}
	httputil.WriteOK(w, map[string]string{"success": "true"})
}
func (h *EnterpriseHandler) KeyUsage(w http.ResponseWriter, r *http.Request) {
	kid := chi.URLParam(r, "id")
	sid := shopIDFromRequest(r)
	if sid == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id required")
		return
	}
	k, err := h.km.GetKey(kid, sid)
	if err != nil {
		httputil.WriteError(w, 404, "not found")
		return
	}
	httputil.WriteOK(w, map[string]interface{}{"success": true, "id": k.ID, "label": k.Label, "last_used_at": k.LastUsedAt})
}
func (h *EnterpriseHandler) AuditLog(w http.ResponseWriter, r *http.Request) {
	httputil.WriteOK(w, map[string]interface{}{"success": true, "logs": []interface{}{}})
}
