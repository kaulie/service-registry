package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestAgentMCPServerListShape(t *testing.T) {
	e := newEnv(t, nil)
	e.ok(t, http.MethodPost, "/v1/namespaces", map[string]any{"name": "team-a"})
	e.ok(t, http.MethodPost, "/v1/namespaces", map[string]any{"name": "team-b"})

	status, _ := e.do(t, http.MethodPut, "/v1/namespaces/team-a/agent/mcp-servers/git-via-proxy", map[string]any{
		"command": "node",
		"args":    []string{"/absolute/path/to/server.mjs"},
		"env":     map[string]string{"GIT_VIA_PROXY_URL": "http://127.0.0.1:7897"},
		"cwd":     "/absolute/path/to/server-dir",
	}, testAdminToken)
	if status != http.StatusCreated {
		t.Fatalf("首次登记应为 201，实际 %d", status)
	}
	e.ok(t, http.MethodPut, "/v1/namespaces/team-a/agent/mcp-servers/docs", map[string]any{
		"type":    "http",
		"url":     "https://example.com/mcp",
		"headers": map[string]string{"Authorization": "Bearer token"},
	})
	e.ok(t, http.MethodPut, "/v1/namespaces/team-b/agent/mcp-servers/events", map[string]any{
		"type": "sse",
		"url":  "https://example.com/sse",
	})

	// 读接口默认开放：agent 不带令牌也能拉列表。
	status, body := e.do(t, http.MethodGet, "/v1/agent/mcp-servers", nil, "")
	if status != http.StatusOK {
		t.Fatalf("列表应为 200，实际 %d：%v", status, body)
	}
	servers := body["mcpServers"].(map[string]any)
	if len(servers) != 3 {
		t.Fatalf("应有 3 个 MCP 服务，实际 %v", body)
	}

	git := servers["git-via-proxy"].(map[string]any)
	if _, ok := git["type"]; ok {
		t.Fatalf("stdio 配置不应带 type：%v", git)
	}
	if git["command"] != "node" || git["cwd"] != "/absolute/path/to/server-dir" {
		t.Fatalf("stdio 配置不对：%v", git)
	}
	if args := stringSlice(t, git["args"]); len(args) != 1 || args[0] != "/absolute/path/to/server.mjs" {
		t.Fatalf("args 不对：%v", git["args"])
	}
	if env := stringMap(t, git["env"]); env["GIT_VIA_PROXY_URL"] != "http://127.0.0.1:7897" {
		t.Fatalf("env 不对：%v", git["env"])
	}

	docs := servers["docs"].(map[string]any)
	if docs["type"] != "http" || docs["url"] != "https://example.com/mcp" {
		t.Fatalf("http 配置不对：%v", docs)
	}
	if headers := stringMap(t, docs["headers"]); headers["Authorization"] != "Bearer token" {
		t.Fatalf("headers 不对：%v", docs["headers"])
	}

	events := servers["events"].(map[string]any)
	if events["type"] != "sse" || events["url"] != "https://example.com/sse" {
		t.Fatalf("sse 配置不对：%v", events)
	}
	if _, ok := events["headers"]; ok {
		t.Fatalf("没登记 headers 时不应出现该字段：%v", events)
	}

	filtered := e.ok(t, http.MethodGet, "/v1/agent/mcp-servers?namespace=team-b", nil)
	only := filtered["mcpServers"].(map[string]any)
	if len(only) != 1 || only["events"] == nil {
		t.Fatalf("按命名空间过滤应只剩 events：%v", filtered)
	}

	// 同样的内容再登记一次：200，并且不新增审计（凭据也不进审计说明）。
	status, _ = e.do(t, http.MethodPut, "/v1/namespaces/team-a/agent/mcp-servers/git-via-proxy", map[string]any{
		"type":    "stdio",
		"command": "node",
		"args":    []string{"/absolute/path/to/server.mjs"},
		"env":     map[string]string{"GIT_VIA_PROXY_URL": "http://127.0.0.1:7897"},
		"cwd":     "/absolute/path/to/server-dir",
	}, testAdminToken)
	if status != http.StatusOK {
		t.Fatalf("内容不变应为 200，实际 %d", status)
	}
	audit := e.ok(t, http.MethodGet, "/v1/audit?entity=mcp_server&ref=team-a/git-via-proxy", nil)
	entries := audit["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("内容不变不应再写一条审计，实际 %d 条：%v", len(entries), entries)
	}
	detail, _ := entries[0].(map[string]any)["detail"].(string)
	if detail == "" || strings.Contains(detail, "Bearer") || strings.Contains(detail, "GIT_VIA_PROXY_URL") || strings.Contains(detail, "server.mjs") {
		t.Fatalf("审计说明不应带命令参数或凭据：%q", detail)
	}

	// 名字被别的命名空间占用。
	conflict := e.expectStatus(t, http.MethodPut, "/v1/namespaces/team-b/agent/mcp-servers/docs", testAdminToken, map[string]any{
		"type": "http",
		"url":  "https://other.example/mcp",
	}, http.StatusConflict)
	if conflict["error"].(map[string]any)["code"] != "conflict" {
		t.Fatalf("重名应为 conflict：%v", conflict)
	}

	// 换一种传输方式是更新，列表里不再残留 stdio 字段。
	e.ok(t, http.MethodPut, "/v1/namespaces/team-a/agent/mcp-servers/git-via-proxy", map[string]any{
		"type": "http",
		"url":  "http://127.0.0.1:7897/mcp",
	})
	_, after := e.do(t, http.MethodGet, "/v1/agent/mcp-servers?namespace=team-a", nil, "")
	updated := after["mcpServers"].(map[string]any)["git-via-proxy"].(map[string]any)
	if updated["type"] != "http" || updated["command"] != nil || updated["url"] != "http://127.0.0.1:7897/mcp" {
		t.Fatalf("更新后应只剩 http 配置：%v", updated)
	}

	e.expectStatus(t, http.MethodDelete, "/v1/namespaces/team-a/agent/mcp-servers/docs", testAdminToken, nil, http.StatusNoContent)
	_, left := e.do(t, http.MethodGet, "/v1/agent/mcp-servers?namespace=team-a", nil, "")
	if _, ok := left["mcpServers"].(map[string]any)["docs"]; ok {
		t.Fatalf("删除后 docs 仍在：%v", left)
	}

	e.expectStatus(t, http.MethodDelete, "/v1/namespaces/team-a?confirm=team-a", testAdminToken, nil, http.StatusNoContent)
	_, gone := e.do(t, http.MethodGet, "/v1/agent/mcp-servers", nil, "")
	if _, ok := gone["mcpServers"].(map[string]any)["git-via-proxy"]; ok {
		t.Fatalf("删除命名空间应带走它登记的 MCP 服务：%v", gone)
	}
	if _, ok := gone["mcpServers"].(map[string]any)["events"]; !ok {
		t.Fatalf("别的命名空间的 MCP 服务应还在：%v", gone)
	}
}

func TestAgentMCPServerValidation(t *testing.T) {
	e := newEnv(t, nil)
	e.ok(t, http.MethodPost, "/v1/namespaces", map[string]any{"name": "team-a"})

	cases := []struct {
		name string
		path string
		body map[string]any
	}{
		{"空进程", "/v1/namespaces/team-a/agent/mcp-servers/git-via-proxy", map[string]any{"command": "  "}},
		{"远程没写 type", "/v1/namespaces/team-a/agent/mcp-servers/docs", map[string]any{"url": "https://example.com/mcp"}},
		{"http 混了 command", "/v1/namespaces/team-a/agent/mcp-servers/docs", map[string]any{"type": "http", "url": "https://example.com/mcp", "command": "node"}},
		{"不是 http 地址", "/v1/namespaces/team-a/agent/mcp-servers/docs", map[string]any{"type": "http", "url": "file:///tmp/mcp"}},
		{"未知字段", "/v1/namespaces/team-a/agent/mcp-servers/docs", map[string]any{"type": "sse", "url": "https://example.com/sse", "auth": "nope"}},
		{"名字不合法", "/v1/namespaces/team-a/agent/mcp-servers/Git", map[string]any{"command": "node"}},
		{"命名空间不存在", "/v1/namespaces/missing/agent/mcp-servers/docs", map[string]any{"type": "sse", "url": "https://example.com/sse"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := http.StatusBadRequest
			if tc.name == "命名空间不存在" {
				want = http.StatusNotFound
			}
			e.expectStatus(t, http.MethodPut, tc.path, testAdminToken, tc.body, want)
		})
	}

	e.expectStatus(t, http.MethodPut, "/v1/namespaces/team-a/agent/mcp-servers/docs", "", map[string]any{
		"type": "http", "url": "https://example.com/mcp",
	}, http.StatusUnauthorized)
	e.expectStatus(t, http.MethodDelete, "/v1/namespaces/team-a/agent/mcp-servers/missing", testAdminToken, nil, http.StatusNotFound)

	empty := e.ok(t, http.MethodGet, "/v1/agent/mcp-servers", nil)
	if len(empty["mcpServers"].(map[string]any)) != 0 {
		t.Fatalf("校验失败不应留下登记：%v", empty)
	}
}

func stringSlice(t *testing.T, v any) []string {
	t.Helper()
	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("期望字符串数组，实际 %T %v", v, v)
	}
	out := make([]string, len(raw))
	for i, item := range raw {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("第 %d 项不是字符串：%v", i, item)
		}
		out[i] = s
	}
	return out
}

func stringMap(t *testing.T, v any) map[string]string {
	t.Helper()
	raw, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("期望对象，实际 %T %v", v, v)
	}
	out := make(map[string]string, len(raw))
	for k, item := range raw {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("%s 不是字符串：%v", k, item)
		}
		out[k] = s
	}
	return out
}
