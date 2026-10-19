package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/kaulie/service-registry/internal/mcpagg"
)

// handleMCPServers 返回每个已登记服务的 MCP 地址。只拼契约和实例，不访问那些地址。
func (s *Server) handleMCPServers(w http.ResponseWriter, r *http.Request) {
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if _, ok := s.requireRead(w, r, ns); !ok {
		return
	}
	servers, err := s.mcpCatalog(r.Context(), ns)
	if err != nil {
		s.mapStoreError(w, r, err, "MCP 目录")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"servers":      servers,
		"mcpPath":      mcpagg.AggregatorPath,
		"mcpTransport": "streamable-http",
	})
}

// handleMCP 是给 agent 的聚合入口（Streamable HTTP）。
// 工具名是 namespace__service__tool，调用转发到该服务自己的 MCP。
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireRead(w, r, ""); !ok {
		return
	}
	ctx := mcpagg.WithIdentity(r.Context(),
		strings.TrimSpace(r.Header.Get("identity_role")),
		strings.TrimSpace(r.Header.Get("identity_id")))
	r = r.WithContext(ctx)
	catalogOnly := strings.TrimSpace(r.Header.Get(mcpagg.HopHeader)) == mcpagg.HopValue
	gw := s.mcpGateway()
	gw.IdentityRole = strings.TrimSpace(r.Header.Get("identity_role"))
	gw.IdentityID = strings.TrimSpace(r.Header.Get("identity_id"))
	gw.Handler(r.Host, catalogOnly).ServeHTTP(w, r)
}

func (s *Server) mcpCatalog(ctx context.Context, namespace string) ([]mcpagg.Server, error) {
	snap, err := s.store.Snapshot(ctx, namespace)
	if err != nil {
		return nil, err
	}
	return mcpagg.Compose(snap.Services, snap.Instances), nil
}

func (s *Server) mcpGateway() *mcpagg.Gateway {
	return &mcpagg.Gateway{
		Catalog: func(ctx context.Context) ([]mcpagg.Server, error) {
			return s.mcpCatalog(ctx, "")
		},
		Version: s.version,
		Log:     s.log,
	}
}
