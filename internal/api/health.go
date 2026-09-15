package api

import (
	"context"
	"io"
	"net/http"
	"time"
)

// handleHealth 是平台统一的存活探针（约定路径 /health，同时保留 /healthz）。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	rev, _ := s.store.CurrentRevision(r.Context())
	s.writeJSON(w, http.StatusOK, map[string]any{
		"status":        "ok",
		"service":       "service-registry",
		"version":       s.version,
		"uptimeSeconds": int(time.Since(s.startedAt).Seconds()),
		"revision":      rev,
	})
}

// handleReady 就绪探针：数据库可读写才算就绪。
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "degraded", "reason": "数据库不可用：" + err.Error(),
		})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

// writeRuntimeMetrics 在抓取时现算运行期读数（计数直接查库，永远和真实数据一致）。
func (s *Server) writeRuntimeMetrics(w io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	s.metrics.Set("registry_build_info", map[string]string{"version": s.version}, 1)
	s.metrics.Set("registry_uptime_seconds", nil, time.Since(s.startedAt).Seconds())

	counts, err := s.store.Counts(ctx)
	if err != nil {
		s.log.Warn("抓取指标时统计失败", "err", err)
		return
	}
	s.metrics.Set("registry_namespaces", nil, float64(counts.Namespaces))
	s.metrics.Set("registry_services", nil, float64(counts.Services))
	s.metrics.Set("registry_instances", nil, float64(counts.Instances))
	s.metrics.Set("registry_endpoints", nil, float64(counts.Endpoints))
	s.metrics.Set("registry_changes_total", nil, float64(counts.Changes))

	if rev, err := s.store.CurrentRevision(ctx); err == nil {
		s.metrics.Set("registry_revision", nil, float64(rev))
	}
}
