package api

import (
	"errors"
	"net/http"

	"github.com/kaulie/service-registry/internal/model"
	"github.com/kaulie/service-registry/internal/store"
)

// instanceWrite 是实例的写入结构（id 可省略，由服务端生成）。
type instanceWrite struct {
	ID       string            `json:"id"`
	Scheme   string            `json:"scheme"`
	Host     string            `json:"host"`
	Port     int               `json:"port"`
	Metadata map[string]string `json:"metadata"`
}

func (iw instanceWrite) toModel() (model.Instance, error) {
	scheme, err := validateScheme(iw.Scheme)
	if err != nil {
		return model.Instance{}, err
	}
	if err := validateHost(iw.Host); err != nil {
		return model.Instance{}, err
	}
	if err := validatePort(iw.Port); err != nil {
		return model.Instance{}, err
	}
	if err := validateMetadata(iw.Metadata); err != nil {
		return model.Instance{}, err
	}
	return model.Instance{
		ID: iw.ID, Scheme: scheme, Host: iw.Host, Port: iw.Port, Metadata: iw.Metadata,
	}, nil
}

// maxSyncInstances 限制一次声明式同步的实例数（防畸形输入）。
const maxSyncInstances = 500

// handleCreateInstance 新增一个实例声明。
func (s *Server) handleCreateInstance(w http.ResponseWriter, r *http.Request) {
	nsName, svcName := r.PathValue("ns"), r.PathValue("svc")
	rl, ok := s.requireWrite(w, r, nsName)
	if !ok {
		return
	}
	var body instanceWrite
	if !s.decodeJSON(w, r, &body) {
		return
	}
	inst, err := body.toModel()
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	inst.Namespace, inst.Service = nsName, svcName

	created, err := s.store.CreateInstance(r.Context(), inst, rl.actor)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "not_found",
				"服务 "+nsName+"/"+svcName+" 尚未登记契约；请先 PUT /v1/namespaces/"+nsName+"/services/"+svcName)
			return
		}
		if errors.Is(err, store.ErrConflict) {
			s.writeError(w, r, http.StatusConflict, "conflict",
				"该地址已被登记为同一服务的另一个实例；请改用 PATCH 修改该实例或先 DELETE")
			return
		}
		s.mapStoreError(w, r, err, "实例")
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"instance": created})
}

// handleSyncInstances 声明式同步：把服务的实例集合整体对齐到请求体里的集合。
// 这是给 CI / 部署流水线的推荐入口（一次性调用，无需任何常驻进程）。
func (s *Server) handleSyncInstances(w http.ResponseWriter, r *http.Request) {
	nsName, svcName := r.PathValue("ns"), r.PathValue("svc")
	rl, ok := s.requireWrite(w, r, nsName)
	if !ok {
		return
	}
	var body struct {
		Instances []instanceWrite `json:"instances"`
	}
	if !s.decodeJSON(w, r, &body) {
		return
	}
	if len(body.Instances) > maxSyncInstances {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request",
			"一次同步的实例数过多（上限 "+itoa(maxSyncInstances)+"）；可拆分为多次调用")
		return
	}
	want := make([]model.Instance, 0, len(body.Instances))
	for i, iw := range body.Instances {
		inst, err := iw.toModel()
		if err != nil {
			s.writeError(w, r, http.StatusBadRequest, "invalid_request",
				"instances["+itoa(i)+"] 不合法："+err.Error())
			return
		}
		want = append(want, inst)
	}

	res, err := s.store.SyncInstances(r.Context(), nsName, svcName, want, rl.actor)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "not_found",
				"服务 "+nsName+"/"+svcName+" 尚未登记契约；请先 PUT /v1/namespaces/"+nsName+"/services/"+svcName)
			return
		}
		s.mapStoreError(w, r, err, "实例集合")
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}

// handlePatchInstance 部分更新实例（scheme/host/port/metadata）。
func (s *Server) handlePatchInstance(w http.ResponseWriter, r *http.Request) {
	nsName, svcName, id := r.PathValue("ns"), r.PathValue("svc"), r.PathValue("id")
	rl, ok := s.requireWrite(w, r, nsName)
	if !ok {
		return
	}
	var body struct {
		Scheme   *string            `json:"scheme"`
		Host     *string            `json:"host"`
		Port     *int               `json:"port"`
		Metadata *map[string]string `json:"metadata"`
	}
	if !s.decodeJSON(w, r, &body) {
		return
	}
	patch := store.InstancePatch{}
	if body.Scheme != nil {
		scheme, err := validateScheme(*body.Scheme)
		if err != nil {
			s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		patch.Scheme = &scheme
	}
	if body.Host != nil {
		if err := validateHost(*body.Host); err != nil {
			s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		patch.Host = body.Host
	}
	if body.Port != nil {
		if err := validatePort(*body.Port); err != nil {
			s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		patch.Port = body.Port
	}
	if body.Metadata != nil {
		if err := validateMetadata(*body.Metadata); err != nil {
			s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		patch.Metadata = body.Metadata
	}

	inst, err := s.store.UpdateInstance(r.Context(), nsName, svcName, id, patch, rl.actor)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			s.writeError(w, r, http.StatusConflict, "conflict", "该地址已被同一服务的另一个实例占用")
			return
		}
		s.mapStoreError(w, r, err, "实例 "+id)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"instance": inst})
}

// handleDeleteInstance 注销实例。
func (s *Server) handleDeleteInstance(w http.ResponseWriter, r *http.Request) {
	nsName, svcName, id := r.PathValue("ns"), r.PathValue("svc"), r.PathValue("id")
	rl, ok := s.requireWrite(w, r, nsName)
	if !ok {
		return
	}
	if err := s.store.DeleteInstance(r.Context(), nsName, svcName, id, rl.actor); err != nil {
		s.mapStoreError(w, r, err, "实例 "+id)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
