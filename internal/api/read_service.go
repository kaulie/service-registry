package api

import (
	"errors"
	"net/http"

	"github.com/kaulie/service-registry/internal/store"
)

// handleGetService 读取服务契约（含端点索引与实例数）。
func (s *Server) handleGetService(w http.ResponseWriter, r *http.Request) {
	nsName, svcName := r.PathValue("ns"), r.PathValue("svc")
	if _, ok := s.requireRead(w, r, nsName); !ok {
		return
	}
	svc, err := s.store.GetService(r.Context(), nsName, svcName)
	if err != nil {
		s.mapStoreError(w, r, err, "服务 "+svcName)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"service": svc})
}

// handleGetSpec 返回内联 OpenAPI 原文（可直接喂给工具链；?download=1 触发下载）。
func (s *Server) handleGetSpec(w http.ResponseWriter, r *http.Request) {
	nsName, svcName := r.PathValue("ns"), r.PathValue("svc")
	if _, ok := s.requireRead(w, r, nsName); !ok {
		return
	}
	raw, format, err := s.store.ServiceSpec(r.Context(), nsName, svcName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "not_found",
				"服务 "+nsName+"/"+svcName+" 没有登记内联 API 规范（登记时未提供 api.spec）")
			return
		}
		s.mapStoreError(w, r, err, "服务 "+svcName+" 的内联 API 规范")
		return
	}
	ct := "application/yaml; charset=utf-8"
	if format == "json" {
		ct = "application/json; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	if queryBool(r, "download") {
		w.Header().Set("Content-Disposition",
			`attachment; filename="`+nsName+`-`+svcName+`.openapi.yaml"`)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// handleDeleteService 删除服务契约（连带实例与端点索引）。
func (s *Server) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	nsName, svcName := r.PathValue("ns"), r.PathValue("svc")
	rl, ok := s.requireWrite(w, r, nsName)
	if !ok {
		return
	}
	if err := s.store.DeleteService(r.Context(), nsName, svcName, rl.actor); err != nil {
		s.mapStoreError(w, r, err, "服务 "+svcName)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
