package api

import (
	"net/http"
	"strings"

	"github.com/kaulie/service-registry/internal/idgen"
)

// handleCreateNamespace 创建命名空间，并**一次性**返回其注册令牌（只存哈希）。
func (s *Server) handleCreateNamespace(w http.ResponseWriter, r *http.Request) {
	rl, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !s.decodeJSON(w, r, &body) {
		return
	}
	if err := validateName("命名空间名", body.Name); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateText("命名空间描述", body.Description); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	token := idgen.Token()
	ns, err := s.store.CreateNamespace(r.Context(), body.Name, body.Description, hashToken(token), rl.actor)
	if err != nil {
		s.mapStoreError(w, r, err, "命名空间 "+body.Name)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"namespace": ns,
		"token":     token,
		"note":      "请立即保存该令牌：注册中心只保存其哈希，之后无法再次查看；可用 POST /v1/namespaces/{ns}/token 轮换。",
	})
}

// handleGetNamespace 读取命名空间。
func (s *Server) handleGetNamespace(w http.ResponseWriter, r *http.Request) {
	nsName := r.PathValue("ns")
	if _, ok := s.requireRead(w, r, nsName); !ok {
		return
	}
	ns, _, err := s.store.GetNamespace(r.Context(), nsName)
	if err != nil {
		s.mapStoreError(w, r, err, "命名空间 "+nsName)
		return
	}
	if list, lerr := s.store.ListNamespaces(r.Context()); lerr == nil {
		for _, item := range list {
			if item.Name == ns.Name {
				ns.ServiceCount, ns.InstanceCount = item.ServiceCount, item.InstanceCount
			}
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"namespace": ns})
}

// handleListNamespaces 列出全部命名空间。
func (s *Server) handleListNamespaces(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireRead(w, r, ""); !ok {
		return
	}
	list, err := s.store.ListNamespaces(r.Context())
	if err != nil {
		s.mapStoreError(w, r, err, "命名空间列表")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"namespaces": list, "total": len(list)})
}

// handleUpdateNamespace 更新描述。
func (s *Server) handleUpdateNamespace(w http.ResponseWriter, r *http.Request) {
	rl, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	nsName := r.PathValue("ns")
	var body struct {
		Description *string `json:"description"`
	}
	if !s.decodeJSON(w, r, &body) {
		return
	}
	if body.Description != nil {
		if err := validateText("命名空间描述", *body.Description); err != nil {
			s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	ns, err := s.store.UpdateNamespace(r.Context(), nsName, body.Description, rl.actor)
	if err != nil {
		s.mapStoreError(w, r, err, "命名空间 "+nsName)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"namespace": ns})
}

// handleDeleteNamespace 删除命名空间（含其全部服务与实例），需要 ?confirm=<ns> 防误删。
func (s *Server) handleDeleteNamespace(w http.ResponseWriter, r *http.Request) {
	rl, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	nsName := r.PathValue("ns")
	if strings.TrimSpace(r.URL.Query().Get("confirm")) != nsName {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request",
			"删除命名空间会连带删除其全部服务与实例；请显式确认：DELETE /v1/namespaces/"+nsName+"?confirm="+nsName)
		return
	}
	if err := s.store.DeleteNamespace(r.Context(), nsName, rl.actor); err != nil {
		s.mapStoreError(w, r, err, "命名空间 "+nsName)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRotateToken 轮换命名空间令牌（旧令牌立即失效）。
func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	rl, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	nsName := r.PathValue("ns")
	if _, _, err := s.store.GetNamespace(r.Context(), nsName); err != nil {
		s.mapStoreError(w, r, err, "命名空间 "+nsName)
		return
	}
	token := idgen.Token()
	if err := s.store.SetNamespaceToken(r.Context(), nsName, hashToken(token), rl.actor, "轮换命名空间令牌"); err != nil {
		s.mapStoreError(w, r, err, "命名空间 "+nsName)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"namespace": nsName, "token": token,
		"note": "旧令牌已立即失效；新令牌只显示这一次。",
	})
}

// handleClearToken 清除令牌：此后该命名空间只能由 admin 令牌写入。
func (s *Server) handleClearToken(w http.ResponseWriter, r *http.Request) {
	rl, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	nsName := r.PathValue("ns")
	if err := s.store.SetNamespaceToken(r.Context(), nsName, "", rl.actor, "清除命名空间令牌（之后仅 admin 可写）"); err != nil {
		s.mapStoreError(w, r, err, "命名空间 "+nsName)
		return
	}
	ns, _, err := s.store.GetNamespace(r.Context(), nsName)
	if err != nil {
		s.mapStoreError(w, r, err, "命名空间 "+nsName)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"namespace": ns})
}
