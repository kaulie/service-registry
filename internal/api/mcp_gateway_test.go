package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/kaulie/service-registry/internal/mcpagg"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPCatalogAndForward(t *testing.T) {
	var gotRole, gotID, gotHop string
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		srv := mcp.NewServer(&mcp.Implementation{Name: "billing", Version: "1"}, nil)
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "ping",
			Description: "回答一声",
		}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
			Name string `json:"name"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			return nil, map[string]any{"pong": in.Name}, nil
		})
		return srv
	}, &mcp.StreamableHTTPOptions{Stateless: true})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(mcpagg.HopHeader) != "" {
			gotHop = r.Header.Get(mcpagg.HopHeader)
		}
		if role := r.Header.Get("identity_role"); role != "" {
			gotRole = role
			gotID = r.Header.Get("identity_id")
		}
		inner.ServeHTTP(w, r)
	}))
	defer backend.Close()
	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(backendURL.Port())
	if err != nil {
		t.Fatal(err)
	}

	e := newEnv(t, nil)
	seedService(t, e)
	e.ok(t, http.MethodPost, "/v1/namespaces/team-a/services/event-center/instances", map[string]any{
		"scheme": "http", "host": backendURL.Hostname(), "port": port, "os": "linux",
	})
	// tcp 实例没有 HTTP MCP，目录里不应出现。
	e.ok(t, http.MethodPost, "/v1/namespaces/team-a/services/event-center/instances", map[string]any{
		"scheme": "tcp", "host": "10.0.0.9", "port": 9, "os": "linux",
	})

	body := e.ok(t, http.MethodGet, "/v1/mcp/servers?namespace=team-a", nil)
	servers := body["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("目录应只有 event-center，实际 %v", body)
	}
	svc := servers[0].(map[string]any)
	if svc["mcpPath"] != "/mcp" || svc["service"] != "event-center" {
		t.Fatalf("目录项不对：%v", svc)
	}
	eps := svc["endpoints"].([]any)
	if len(eps) != 1 || eps[0].(map[string]any)["url"] != backend.URL+"/mcp" {
		t.Fatalf("MCP 地址应为 %s/mcp，实际 %v", backend.URL, svc["endpoints"])
	}
	if body["mcpPath"] != "/mcp" {
		t.Fatalf("聚合入口应是 /mcp：%v", body["mcpPath"])
	}

	ctx := context.Background()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "0"}, nil).Connect(ctx,
		&mcp.StreamableClientTransport{
			Endpoint:             e.ts.URL + "/mcp",
			DisableStandaloneSSE: true,
			MaxRetries:           -1,
			HTTPClient: &http.Client{Transport: headerRoundTripper{http.Header{
				"identity_role": {"agent"},
				"identity_id":   {"cursor-agent"},
			}}},
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	if !containsStr(names, "registry_mcp_servers") || !containsStr(names, "team-a__event-center__ping") {
		t.Fatalf("聚合工具列表不对：%v", names)
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "team-a__event-center__ping",
		Arguments: map[string]any{"name": "world"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("转发失败：%+v", res)
	}
	if gotRole != "agent" || gotID != "cursor-agent" || gotHop != mcpagg.HopValue {
		t.Fatalf("转发头 role=%q id=%q hop=%q", gotRole, gotID, gotHop)
	}
	if pong, _ := res.StructuredContent.(map[string]any)["pong"]; pong != "world" {
		t.Fatalf("应带回后端的 pong，实际 %#v", res.StructuredContent)
	}
}

type headerRoundTripper struct{ h http.Header }

func (rt headerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, vs := range rt.h {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	return http.DefaultTransport.RoundTrip(r)
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
