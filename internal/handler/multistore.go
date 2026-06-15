package handler

import (
	"encoding/json"
	"net/http"

	chi "github.com/go-chi/chi/v5"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service/multistore"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"gorm.io/gorm"
)

type MultiStoreHandler struct {
	db  *gorm.DB
	mgr *multistore.GroupManager
}

func NewMultiStoreHandler(db *gorm.DB, mgr *multistore.GroupManager) *MultiStoreHandler {
	return &MultiStoreHandler{db, mgr}
}
func (h *MultiStoreHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("shop_id")
	gs, _ := h.mgr.GetGroupsForShop(sid)
	items := make([]map[string]interface{}, len(gs))
	for i, g := range gs {
		items[i] = map[string]interface{}{"id": g.ID, "name": g.Name, "member_count": g.MemberCount, "owner_shop_id": g.OwnerShopID}
	}
	httputil.WriteOK(w, map[string]interface{}{"success": true, "groups": items})
}
func (h *MultiStoreHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShopID string `json:"shop_id"`
		Name   string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.ShopID == "" || req.Name == "" {
		httputil.WriteError(w, 400, "shop_id and name are required")
		return
	}
	g, err := h.mgr.CreateGroup(req.ShopID, req.Name)
	if err != nil {
		httputil.WriteError(w, 500, err.Error())
		return
	}
	httputil.WriteJSON(w, 201, map[string]interface{}{"success": true, "id": g.ID, "name": g.Name})
}
func (h *MultiStoreHandler) GetGroup(w http.ResponseWriter, r *http.Request) {
	gid := chi.URLParam(r, "id")
	g, err := h.mgr.GetGroupWithMembers(gid)
	if err != nil {
		httputil.WriteError(w, 404, "not found")
		return
	}
	ms := make([]map[string]interface{}, len(g.Members))
	for i, m := range g.Members {
		ms[i] = map[string]interface{}{"id": m.ID, "shop_id": m.ShopID, "role": m.Role}
	}
	httputil.WriteOK(w, map[string]interface{}{"success": true, "id": g.ID, "name": g.Name, "members": ms})
}
func (h *MultiStoreHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	gid := chi.URLParam(r, "id")
	var req struct {
		ShopID       string `json:"shop_id"`
		MemberShopID string `json:"member_shop_id"`
		Role         string `json:"role"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.ShopID == "" || req.MemberShopID == "" {
		httputil.WriteError(w, 400, "shop_id and member_shop_id are required")
		return
	}
	if err := h.mgr.AddStore(gid, req.ShopID, req.MemberShopID, req.Role); err != nil {
		httputil.WriteError(w, 500, err.Error())
		return
	}
	httputil.WriteOK(w, map[string]string{"success": "true"})
}
func (h *MultiStoreHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	gid := chi.URLParam(r, "id")
	mid := chi.URLParam(r, "shopID")
	sid := r.URL.Query().Get("shop_id")
	if err := h.mgr.RemoveStore(gid, sid, mid); err != nil {
		httputil.WriteError(w, 500, err.Error())
		return
	}
	httputil.WriteOK(w, map[string]string{"success": "true"})
}
func (h *MultiStoreHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	gid := chi.URLParam(r, "id")
	sid := r.URL.Query().Get("shop_id")
	h.db.Delete(&model.StoreGroup{}, "id=? AND owner_shop_id=?", gid, sid)
	httputil.WriteOK(w, map[string]string{"success": "true"})
}
func (h *MultiStoreHandler) SwitchContext(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShopID       string `json:"shop_id"`
		TargetShopID string `json:"target_shop_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.ShopID == "" || req.TargetShopID == "" {
		httputil.WriteError(w, 400, "shop_id and target_shop_id are required")
		return
	}
	gid, ok, _ := h.mgr.VerifyMembership(req.ShopID, req.TargetShopID)
	if !ok {
		httputil.WriteError(w, 403, "not in same group")
		return
	}
	httputil.WriteOK(w, map[string]interface{}{"success": true, "group_id": gid})
}
