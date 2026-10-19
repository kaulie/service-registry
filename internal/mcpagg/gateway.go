package mcpagg

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	catalogToolName     = "registry_mcp_servers"
	defaultProbeTimeout = 2 * time.Second
	defaultCallTimeout  = 30 * time.Second
	defaultCacheTTL     = 5 * time.Second
)

// CatalogFunc 读取当前登记目录。聚合入口用它找后端地址，目录接口也用它。
type CatalogFunc func(ctx context.Context) ([]Server, error)

// Gateway 是注册中心上的 MCP 聚合入口。
// 它自己的工具只有 registry_mcp_servers；其余工具从各服务的 MCP 拉来，
// 名字加上 namespace__service__ 前缀后原样转发。
type Gateway struct {
	Catalog      CatalogFunc
	Version      string
	ProbeTimeout time.Duration
	CallTimeout  time.Duration
	CacheTTL     time.Duration
	Log          *slog.Logger
	// IdentityRole / IdentityID 来自这次聚合调用的请求头，转发时原样带上。
	IdentityRole string
	IdentityID   string

	mu    sync.Mutex
	cache toolCache
}

type toolCache struct {
	key   string
	at    time.Time
	tools []exposedTool
}

type exposedTool struct {
	name        string
	description string
	inputSchema any
	upstream    string
	endpoints   []string
}

// Handler 返回 Streamable HTTP 处理器。requestHost 用来跳过聚合入口自己的地址。
// catalogOnly 为 true 时只暴露目录工具，不再向后端扇出（防止自己调自己）。
func (g *Gateway) Handler(requestHost string, catalogOnly bool) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return g.server(requestHost, catalogOnly)
	}, &mcp.StreamableHTTPOptions{Stateless: true})
}

func (g *Gateway) server(requestHost string, catalogOnly bool) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "service-registry", Version: g.version()}, &mcp.ServerOptions{
		Instructions: "这是服务注册中心的 MCP 聚合入口。registry_mcp_servers 列出每个已登记服务的 MCP 地址。" +
			"其它工具名形如 namespace__service__tool，调用会转发到该服务自己的 MCP。",
		Logger: g.logger(),
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        catalogToolName,
		Description: "列出服务注册中心里每个服务的 MCP 地址（契约 mcpPath 接到实例 scheme://host:port 上）。只读目录，不访问那些 MCP。",
	}, g.listServers)
	if catalogOnly {
		return srv
	}
	for _, tool := range g.tools(requestHost) {
		exposed := tool
		srv.AddTool(&mcp.Tool{
			Name:        exposed.name,
			Description: exposed.description,
			InputSchema: exposed.inputSchema,
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return g.forward(ctx, exposed, req)
		})
	}
	return srv
}

func (g *Gateway) listServers(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, map[string]any, error) {
	servers, err := g.catalog(ctx)
	if err != nil {
		return nil, nil, err
	}
	if servers == nil {
		servers = []Server{}
	}
	return nil, map[string]any{"servers": servers}, nil
}

func (g *Gateway) tools(requestHost string) []exposedTool {
	servers, err := g.catalog(context.Background())
	if err != nil {
		g.logger().Warn("读取 MCP 目录失败", "err", err)
		return nil
	}
	key := cacheKey(servers, requestHost)
	ttl := g.cacheTTL()
	g.mu.Lock()
	if g.cache.key == key && time.Since(g.cache.at) < ttl {
		tools := g.cache.tools
		g.mu.Unlock()
		return tools
	}
	g.mu.Unlock()
	tools := g.probe(servers, requestHost)
	g.mu.Lock()
	g.cache = toolCache{key: key, at: time.Now(), tools: tools}
	g.mu.Unlock()
	return tools
}

func (g *Gateway) probe(servers []Server, requestHost string) []exposedTool {
	type hit struct {
		server Server
		url    string
		tools  []*mcp.Tool
	}
	hits := make([]hit, len(servers))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, server := range servers {
		urls := make([]string, 0, len(server.Endpoints))
		for _, ep := range server.Endpoints {
			if ep.URL == "" || IsSelf(requestHost, ep.URL) {
				continue
			}
			urls = append(urls, ep.URL)
		}
		if len(urls) == 0 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, server Server, urls []string) {
			defer wg.Done()
			defer func() { <-sem }()
			for _, endpoint := range urls {
				tools, err := g.listUpstream(endpoint)
				if err != nil {
					g.logger().Info("MCP 后端未列入聚合", "service", server.Namespace+"/"+server.Service, "url", endpoint, "err", err)
					continue
				}
				hits[i] = hit{server: server, url: endpoint, tools: tools}
				return
			}
		}(i, server, urls)
	}
	wg.Wait()

	var out []exposedTool
	for _, hit := range hits {
		if hit.url == "" {
			continue
		}
		urls := []string{hit.url}
		for _, ep := range hit.server.Endpoints {
			if ep.URL != "" && ep.URL != hit.url && !IsSelf(requestHost, ep.URL) {
				urls = append(urls, ep.URL)
			}
		}
		for _, tool := range hit.tools {
			if tool == nil {
				continue
			}
			name, ok := ToolName(hit.server.Namespace, hit.server.Service, tool.Name)
			if !ok {
				continue
			}
			schema := tool.InputSchema
			if !objectSchema(schema) {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			desc := hit.server.Namespace + "/" + hit.server.Service + ": " + tool.Description
			out = append(out, exposedTool{
				name:        name,
				description: desc,
				inputSchema: schema,
				upstream:    tool.Name,
				endpoints:   urls,
			})
		}
	}
	return out
}

func (g *Gateway) listUpstream(endpoint string) ([]*mcp.Tool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), g.probeTimeout())
	defer cancel()
	session, err := g.connect(ctx, endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	return res.Tools, nil
}

func (g *Gateway) forward(ctx context.Context, tool exposedTool, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args any
	if req != nil && len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "参数不是 JSON：" + err.Error()}},
			}, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, g.callTimeout())
	defer cancel()
	var last error
	extra := identityHeaders(ctx)
	if extra.Get("identity_role") == "" && g != nil && g.IdentityRole != "" {
		extra.Set("identity_role", g.IdentityRole)
	}
	if extra.Get("identity_id") == "" && g != nil && g.IdentityID != "" {
		extra.Set("identity_id", g.IdentityID)
	}
	for _, endpoint := range tool.endpoints {
		session, err := g.connect(ctx, endpoint, extra)
		if err != nil {
			last = err
			continue
		}
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool.upstream, Arguments: args})
		session.Close()
		if err != nil {
			last = err
			continue
		}
		return res, nil
	}
	msg := "没有可用的 MCP 地址"
	if last != nil {
		msg = last.Error()
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: tool.name + " 转发失败：" + msg}},
	}, nil
}

func (g *Gateway) connect(ctx context.Context, endpoint string, extra http.Header) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "service-registry-aggregator", Version: g.version()}, nil)
	return client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
		HTTPClient: &http.Client{
			Timeout: g.callTimeout(),
			Transport: roundTripper{set: func(r *http.Request) {
				r.Header.Set(HopHeader, HopValue)
				for k, vs := range extra {
					for _, v := range vs {
						r.Header.Add(k, v)
					}
				}
			}},
		},
	}, nil)
}

type identityKey struct{}

// WithIdentity 把这次聚合调用的身份放进 context，转发时写成请求头。
func WithIdentity(ctx context.Context, role, id string) context.Context {
	return context.WithValue(ctx, identityKey{}, http.Header{
		"identity_role": {role},
		"identity_id":   {id},
	})
}

func identityHeaders(ctx context.Context) http.Header {
	h, _ := ctx.Value(identityKey{}).(http.Header)
	out := http.Header{}
	if role := h.Get("identity_role"); role != "" {
		out.Set("identity_role", role)
	}
	if id := h.Get("identity_id"); id != "" {
		out.Set("identity_id", id)
	}
	return out
}

type roundTripper struct {
	set func(*http.Request)
}

func (rt roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if rt.set != nil {
		rt.set(r)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func (g *Gateway) catalog(ctx context.Context) ([]Server, error) {
	if g == nil || g.Catalog == nil {
		return nil, nil
	}
	return g.Catalog(ctx)
}

func (g *Gateway) version() string {
	if g != nil && g.Version != "" {
		return g.Version
	}
	return "0"
}

func (g *Gateway) probeTimeout() time.Duration {
	if g != nil && g.ProbeTimeout > 0 {
		return g.ProbeTimeout
	}
	return defaultProbeTimeout
}

func (g *Gateway) callTimeout() time.Duration {
	if g != nil && g.CallTimeout > 0 {
		return g.CallTimeout
	}
	return defaultCallTimeout
}

func (g *Gateway) cacheTTL() time.Duration {
	if g != nil && g.CacheTTL > 0 {
		return g.CacheTTL
	}
	return defaultCacheTTL
}

func (g *Gateway) logger() *slog.Logger {
	if g != nil && g.Log != nil {
		return g.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func cacheKey(servers []Server, requestHost string) string {
	raw, _ := json.Marshal(struct {
		Host    string   `json:"host"`
		Servers []Server `json:"servers"`
	}{requestHost, servers})
	return string(raw)
}

func objectSchema(schema any) bool {
	if schema == nil {
		return false
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	typ, _ := m["type"].(string)
	return typ == "object"
}
