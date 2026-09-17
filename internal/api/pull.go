package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaulie/service-registry/internal/orgdir"
	"github.com/kaulie/service-registry/internal/store"
)

// handleSnapshot 全量快照：给平台**冷启动一次性拉全量**用。
// 带 ETag（与 revision 绑定），之后可以只做 304 判断，省下整个响应体。
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if _, ok := s.requireRead(w, r, ns); !ok {
		return
	}
	snap, err := s.store.Snapshot(r.Context(), ns)
	if err != nil {
		s.mapStoreError(w, r, err, "快照")
		return
	}
	etag := `W/"` + strconv.FormatInt(snap.Revision, 10) + "-" + allOr(ns) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if match := strings.TrimSpace(r.Header.Get("If-None-Match")); match != "" && match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"revision":    snap.Revision,
		"generatedAt": time.Now().UTC(),
		"namespaces":  snap.Namespaces,
		"services":    snap.Services,
		"instances":   snap.Instances,
	})
}

// handleChanges 增量拉取：按 revision 游标取"自 since 以来发生了什么"。
// ?wait=30s 时若暂无变更则挂起等待（long-poll），有新变更立即返回。
func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if _, ok := s.requireRead(w, r, ns); !ok {
		return
	}
	f := s.changeFilter(r, ns)
	wait := parseWait(r, s.cfg.PullWaitMax)

	changes, hasMore, err := s.store.Changes(r.Context(), f)
	if err != nil {
		s.mapStoreError(w, r, err, "变更列表")
		return
	}
	waited := false
	if len(changes) == 0 && wait > 0 {
		deadline := time.Now().Add(wait)
		for {
			s.waitFor(r.Context(), time.Until(deadline))
			if r.Context().Err() != nil {
				return
			}
			changes, hasMore, err = s.store.Changes(r.Context(), f)
			if err != nil {
				s.mapStoreError(w, r, err, "变更列表")
				return
			}
			if len(changes) > 0 || !time.Now().Before(deadline) {
				waited = true
				break
			}
		}
	}

	current, err := s.store.CurrentRevision(r.Context())
	if err != nil {
		s.mapStoreError(w, r, err, "当前 revision")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"revision": current,
		"changes":  changes,
		"hasMore":  hasMore,
		"waited":   waited,
	})
}

// handleAudit 审计查询：同一张变更表，按实体/引用/操作过滤，回答"谁何时改了什么"。
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if _, ok := s.requireRead(w, r, ns); !ok {
		return
	}
	f := s.changeFilter(r, ns)
	f.Entity = strings.TrimSpace(r.URL.Query().Get("entity"))
	f.Ref = strings.TrimSpace(r.URL.Query().Get("ref"))
	f.Op = strings.TrimSpace(r.URL.Query().Get("op"))

	entries, hasMore, err := s.store.Changes(r.Context(), f)
	if err != nil {
		s.mapStoreError(w, r, err, "审计记录")
		return
	}
	current, err := s.store.CurrentRevision(r.Context())
	if err != nil {
		s.mapStoreError(w, r, err, "当前 revision")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"revision": current, "entries": entries, "hasMore": hasMore,
	})
}

// handleMeta 返回本服务的运行期元信息（面板与运维都用它）。
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireRead(w, r, ""); !ok {
		return
	}
	counts, err := s.store.Counts(r.Context())
	if err != nil {
		s.mapStoreError(w, r, err, "统计")
		return
	}
	rev, err := s.store.CurrentRevision(r.Context())
	if err != nil {
		s.mapStoreError(w, r, err, "当前 revision")
		return
	}
	writeAuth := "token"
	if s.cfg.WriteAuthOpen {
		writeAuth = "open"
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"service":          "service-registry",
		"version":          s.version,
		"startedAt":        s.startedAt,
		"uptimeSeconds":    int(time.Since(s.startedAt).Seconds()),
		"revision":         rev,
		"counts":           counts,
		"writeAuth":        writeAuth,
		"readAuthRequired": s.cfg.ReadAuthRequired,
		"defaultNamespace": s.cfg.DefaultNamespace,
		// 部门属性的数据来源（组织接口）。面板/运维靠它判断"部门下拉为什么是空的"。
		"organization": map[string]any{
			"url":             s.org.BaseURL(),
			"enabled":         s.org.Enabled(),
			"departmentsPath": orgdir.DefaultPath,
			"cacheTtlSeconds": int(s.org.CacheTTL().Seconds()),
		},
		"semantics": "本中心是**元信息存储中心**：只记录登记了什么，" +
			"不探活、不心跳、不保证实例可达；可达性由消费方自行校验。",
	})
}

func (s *Server) changeFilter(r *http.Request, ns string) store.ChangeFilter {
	return store.ChangeFilter{
		Since:     queryInt64(r, "since", 0),
		Namespace: ns,
		Limit:     clampPullLimit(queryInt(r, "limit", s.cfg.PullDefaultLimit), s.cfg.PullMaxLimit),
	}
}

func clampPullLimit(n, max int) int {
	if n <= 0 {
		return max
	}
	if n > max {
		return max
	}
	return n
}

// parseWait 解析挂起时长：接受 "30s" 这样的时长，也接受纯秒数。
// 上限由 REGISTRY_PULL_WAIT_MAX 决定（waitFor 内会再次收敛）。
func parseWait(r *http.Request, max time.Duration) time.Duration {
	raw := strings.TrimSpace(r.URL.Query().Get("wait"))
	if raw == "" || raw == "0" {
		return 0
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return clampDuration(d, max)
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return clampDuration(time.Duration(secs)*time.Second, max)
	}
	return 0
}

func clampDuration(d, max time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	if d > max {
		return max
	}
	return d
}

func allOr(ns string) string {
	if ns == "" {
		return "all"
	}
	return ns
}
