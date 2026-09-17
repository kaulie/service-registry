package api

import (
	"net/http"
	"strings"

	"github.com/kaulie/service-registry/internal/model"
	"github.com/kaulie/service-registry/internal/store"
)

const (
	defaultListLimit = 200
	maxListLimit     = 2000
)

// handleListServices 列表服务契约。
// 支持 /v1/services（全局，可用 ?namespace= 过滤）与 /v1/namespaces/{ns}/services。
func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	ns := strings.TrimSpace(r.PathValue("ns"))
	if ns == "" {
		ns = strings.TrimSpace(r.URL.Query().Get("namespace"))
	}
	if _, ok := s.requireRead(w, r, ns); !ok {
		return
	}
	limit := clampLimit(queryInt(r, "limit", defaultListLimit))
	f := store.ServiceFilter{
		Namespace:  ns,
		Tag:        strings.TrimSpace(r.URL.Query().Get("tag")),
		Owner:      strings.TrimSpace(r.URL.Query().Get("owner")),
		Protocol:   strings.TrimSpace(r.URL.Query().Get("protocol")),
		Department: strings.TrimSpace(r.URL.Query().Get("department")),
		Query:      strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:      limit,
		Offset:     queryInt(r, "offset", 0),
	}
	services, total, err := s.store.ListServices(r.Context(), f)
	if err != nil {
		s.mapStoreError(w, r, err, "服务列表")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"services": services, "total": total, "limit": limit, "offset": f.Offset,
	})
}

// handleListInstances 列出某服务的实例；?pick=random 直接返回一个随机实例
// （便于调用方"拿到一个地址就能用"；语义上等价于自己从列表里随机挑）。
// 支持 ?meta=key=value（可重复）按实例元信息过滤。
func (s *Server) handleListInstances(w http.ResponseWriter, r *http.Request) {
	nsName, svcName := r.PathValue("ns"), r.PathValue("svc")
	if _, ok := s.requireRead(w, r, nsName); !ok {
		return
	}
	svc, err := s.store.GetService(r.Context(), nsName, svcName)
	if err != nil {
		s.mapStoreError(w, r, err, "服务 "+svcName)
		return
	}
	instances, err := s.store.ListInstances(r.Context(), nsName, svcName)
	if err != nil {
		s.mapStoreError(w, r, err, "实例列表")
		return
	}

	filters, badFilter := parseMetaFilters(r)
	if badFilter != "" {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", badFilter)
		return
	}
	if len(filters) > 0 {
		filtered := make([]model.Instance, 0, len(instances))
		for _, inst := range instances {
			if matchMeta(inst.Metadata, filters) {
				filtered = append(filtered, inst)
			}
		}
		instances = filtered
	}

	if pick := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("pick"))); pick != "" {
		if pick != "random" {
			s.writeError(w, r, http.StatusBadRequest, "invalid_request",
				"pick 只支持 random（本中心不做轮询/权重：那是消费方的选择，共享轮次在一个多消费方共用的注册中心里语义不成立）")
			return
		}
		if len(instances) == 0 {
			s.writeError(w, r, http.StatusNotFound, "not_found", "该服务当前没有登记任何实例")
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{
			"service":  svc,
			"instance": instances[randIndex(len(instances))],
		})
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"namespace": nsName, "service": svcName, "instances": instances, "total": len(instances),
	})
}

// handleGetInstance 按 ns/svc/id 读取实例。
func (s *Server) handleGetInstance(w http.ResponseWriter, r *http.Request) {
	nsName, svcName, id := r.PathValue("ns"), r.PathValue("svc"), r.PathValue("id")
	if _, ok := s.requireRead(w, r, nsName); !ok {
		return
	}
	inst, err := s.store.GetInstance(r.Context(), id)
	if err != nil {
		s.mapStoreError(w, r, err, "实例 "+id)
		return
	}
	if inst.Namespace != nsName || inst.Service != svcName {
		s.writeError(w, r, http.StatusNotFound, "not_found", "实例 "+id+" 不属于该服务")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"instance": inst})
}

// handleGetInstanceByID 按 ID 全局读取实例（不需要知道它属于哪个服务）。
func (s *Server) handleGetInstanceByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.requireRead(w, r, ""); !ok {
		return
	}
	inst, err := s.store.GetInstance(r.Context(), id)
	if err != nil {
		s.mapStoreError(w, r, err, "实例 "+id)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"instance": inst})
}

// handleSearchAPIs 反向查找："这个接口谁提供？"
//
// path 支持三种写法（详见 docs/DESIGN.md）：
//   - 具体路径 /v1/streams/abc/events → 命中登记的模板 /v1/streams/{stream}/events
//   - 登记的模板原样 /v1/streams/{stream}/events
//   - 通配 /v1/streams/** → * 匹配单段，** 跨段
//
// ?match=exact|template|glob 可强制指定匹配模式。
func (s *Server) handleSearchAPIs(w http.ResponseWriter, r *http.Request) {
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if _, ok := s.requireRead(w, r, ns); !ok {
		return
	}
	match := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("match")))
	if match != "" && match != "exact" && match != "template" && match != "glob" {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", "match 只能是 exact/template/glob")
		return
	}
	f := store.EndpointFilter{
		Method:     strings.TrimSpace(r.URL.Query().Get("method")),
		Path:       strings.TrimSpace(r.URL.Query().Get("path")),
		Match:      match,
		Namespace:  ns,
		Tag:        strings.TrimSpace(r.URL.Query().Get("tag")),
		Department: strings.TrimSpace(r.URL.Query().Get("department")),
		Query:      strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:      clampLimit(queryInt(r, "limit", defaultListLimit)),
		Offset:     queryInt(r, "offset", 0),
	}
	matches, hasMore, err := s.store.SearchEndpoints(r.Context(), f)
	if err != nil {
		s.mapStoreError(w, r, err, "API 检索")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"matches": matches, "total": len(matches), "hasMore": hasMore,
		"query": map[string]any{"method": f.Method, "path": f.Path, "match": match},
	})
}

func clampLimit(n int) int {
	if n <= 0 {
		return defaultListLimit
	}
	if n > maxListLimit {
		return maxListLimit
	}
	return n
}
