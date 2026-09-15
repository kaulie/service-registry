// Package api 实现注册中心对外的 HTTP 接口。
//
// 路由分四组（详见 README / api/openapi.yaml）：
//   - 注册/维护：写接口，需要令牌（namespace token 或 admin token）
//   - 发现/查询：读接口，默认开放（便于各平台拉取），可配置为需令牌
//   - 拉取/订阅：快照、增量游标（long-poll）、SSE
//   - 运维：健康检查与指标
package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/kaulie/service-registry/internal/config"
	"github.com/kaulie/service-registry/internal/metrics"
	"github.com/kaulie/service-registry/internal/store"
)

// maxBodyBytes 限制请求体大小（内联 OpenAPI 上限 256KB + 余量）。
const maxBodyBytes = 1 << 20

// Server 持有全部依赖。
type Server struct {
	store     *store.Store
	cfg       config.Config
	metrics   *metrics.Registry
	log       *slog.Logger
	version   string
	startedAt time.Time
}

// NewServer 构造 HTTP 服务。
func NewServer(st *store.Store, cfg config.Config, m *metrics.Registry, log *slog.Logger, version string) *Server {
	return &Server{store: st, cfg: cfg, metrics: m, log: log, version: version, startedAt: time.Now().UTC()}
}

// Handler 返回完整的 http.Handler（含路由与中间件）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// add 注册一条路由，并额外注册"同路径、任意方法"的兜底处理器：
	// 这样"路径对但方法不对"会得到 405（而不是被根路径兜成 404），
	// 而"路径也不对"则由根处理器给出 404 JSON。
	seenPath := map[string]bool{}
	add := func(method, pattern string, h http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, h)
		if !seenPath[pattern] {
			seenPath[pattern] = true
			mux.HandleFunc(pattern, s.methodNotAllowed)
		}
	}

	// ---- 运维 ----
	add(http.MethodGet, "/health", s.handleHealth)
	add(http.MethodGet, "/healthz", s.handleHealth)
	add(http.MethodGet, "/readyz", s.handleReady)
	mux.HandleFunc("GET /metrics", s.metrics.Handler(s.writeRuntimeMetrics))
	add(http.MethodGet, "/v1/meta", s.handleMeta)

	// ---- 拉取/订阅（给其他平台） ----
	add(http.MethodGet, "/v1/snapshot", s.handleSnapshot)
	add(http.MethodGet, "/v1/changes", s.handleChanges)
	add(http.MethodGet, "/v1/events", s.handleEvents)
	add(http.MethodGet, "/v1/audit", s.handleAudit)

	// ---- 发现/查询 ----
	add(http.MethodGet, "/v1/services", s.handleListServices)
	add(http.MethodGet, "/v1/instances/{id}", s.handleGetInstanceByID)
	add(http.MethodGet, "/v1/search/apis", s.handleSearchAPIs)

	// ---- 命名空间 ----
	add(http.MethodGet, "/v1/namespaces", s.handleListNamespaces)
	add(http.MethodPost, "/v1/namespaces", s.handleCreateNamespace)
	add(http.MethodGet, "/v1/namespaces/{ns}", s.handleGetNamespace)
	add(http.MethodPut, "/v1/namespaces/{ns}", s.handleUpdateNamespace)
	add(http.MethodDelete, "/v1/namespaces/{ns}", s.handleDeleteNamespace)
	add(http.MethodPost, "/v1/namespaces/{ns}/token", s.handleRotateToken)
	add(http.MethodDelete, "/v1/namespaces/{ns}/token", s.handleClearToken)

	// ---- 服务契约 ----
	add(http.MethodGet, "/v1/namespaces/{ns}/services", s.handleListServices)
	add(http.MethodPut, "/v1/namespaces/{ns}/services/{svc}", s.handlePutService)
	add(http.MethodGet, "/v1/namespaces/{ns}/services/{svc}", s.handleGetService)
	add(http.MethodDelete, "/v1/namespaces/{ns}/services/{svc}", s.handleDeleteService)
	add(http.MethodGet, "/v1/namespaces/{ns}/services/{svc}/spec", s.handleGetSpec)

	// ---- 实例 ----
	add(http.MethodGet, "/v1/namespaces/{ns}/services/{svc}/instances", s.handleListInstances)
	add(http.MethodPut, "/v1/namespaces/{ns}/services/{svc}/instances", s.handleSyncInstances)
	add(http.MethodPost, "/v1/namespaces/{ns}/services/{svc}/instances", s.handleCreateInstance)
	add(http.MethodGet, "/v1/namespaces/{ns}/services/{svc}/instances/{id}", s.handleGetInstance)
	add(http.MethodPatch, "/v1/namespaces/{ns}/services/{svc}/instances/{id}", s.handlePatchInstance)
	add(http.MethodDelete, "/v1/namespaces/{ns}/services/{svc}/instances/{id}", s.handleDeleteInstance)

	// ---- 控制面板 ----
	mux.Handle("/panel/", s.panelHandler())
	// 根路径：跳面板；其它未匹配路径统一回 JSON 404。
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/panel/", http.StatusFound)
			return
		}
		s.writeError(w, r, http.StatusNotFound, "not_found", "未知路径 "+r.URL.Path)
	})

	return s.withMiddleware(mux)
}

// methodNotAllowed 是"路径存在但没有这个方法"的响应。
func (s *Server) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "GET, POST, PUT, PATCH, DELETE")
	s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed",
		r.Method+" 不被 "+r.URL.Path+" 支持")
}

// withMiddleware 串联 request-id / 访问日志 / 指标。
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = r.Header.Get("X-Correlation-ID")
		}
		if reqID == "" {
			reqID = fmt.Sprintf("r-%d", start.UnixNano())
		}
		w.Header().Set("X-Request-ID", reqID)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		s.metrics.Inc("registry_http_requests_total", map[string]string{
			"route": route, "method": r.Method, "code": fmt.Sprint(rec.status),
		})
		s.log.Info("http",
			"request_id", reqID, "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "bytes", rec.bytes, "duration_ms", time.Since(start).Milliseconds())
	})
}

// statusRecorder 记录状态码与字节数，并透传 Flush（SSE 需要）。
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush 让 SSE 能逐条推送。
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
