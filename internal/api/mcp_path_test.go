package api

import (
	"net/http"
	"strings"
	"testing"
)

// TestMCPPathDefaultAndRoundTrip 覆盖契约上的 MCP 地址：
// 不写时收成 /mcp，自定义路径能读写回来，非法路径被拒绝。
func TestMCPPathDefaultAndRoundTrip(t *testing.T) {
	e := newEnv(t, nil)
	e.ok(t, http.MethodPost, "/v1/namespaces", map[string]any{"name": "team-a"})
	path := "/v1/namespaces/team-a/services/event-center"

	// 请求里不带 mcpPath → 详情、列表、快照、API 反查都是默认 /mcp。
	e.ok(t, http.MethodPut, path, serviceBody(testSpec))
	svc := e.ok(t, http.MethodGet, path, nil)["service"].(map[string]any)
	if svc["mcpPath"] != "/mcp" {
		t.Fatalf("没写 mcpPath 时应为 /mcp，实际 %v", svc["mcpPath"])
	}
	list := e.ok(t, http.MethodGet, "/v1/services?namespace=team-a", nil)["services"].([]any)
	if got := list[0].(map[string]any)["mcpPath"]; got != "/mcp" {
		t.Fatalf("服务列表里的 mcpPath 应为 /mcp，实际 %v", got)
	}
	snap := e.ok(t, http.MethodGet, "/v1/snapshot", nil)["services"].([]any)
	if got := snap[0].(map[string]any)["mcpPath"]; got != "/mcp" {
		t.Fatalf("快照里的 mcpPath 应为 /mcp，实际 %v", got)
	}
	match := e.ok(t, http.MethodGet, "/v1/search/apis?method=GET&path=/health", nil)["matches"].([]any)[0].(map[string]any)
	if got := match["mcpPath"]; got != "/mcp" {
		t.Fatalf("API 反查结果里的 mcpPath 应为 /mcp，实际 %v", got)
	}

	// 显式写成别的路径，整条链路都要带回。
	body := serviceBody(testSpec)
	body["mcpPath"] = "  /agents/mcp  "
	e.ok(t, http.MethodPut, path, body)
	svc = e.ok(t, http.MethodGet, path, nil)["service"].(map[string]any)
	if svc["mcpPath"] != "/agents/mcp" {
		t.Fatalf("应去掉两端空白并保存自定义路径，实际 %v", svc["mcpPath"])
	}
	match = e.ok(t, http.MethodGet, "/v1/search/apis?method=GET&path=/health", nil)["matches"].([]any)[0].(map[string]any)
	if got := match["mcpPath"]; got != "/agents/mcp" {
		t.Fatalf("API 反查应带回自定义 mcpPath，实际 %v", got)
	}

	// 再次留空（面板清空）→ 回到默认 /mcp，而不是把字段清掉。
	e.ok(t, http.MethodPut, path, serviceBody(testSpec))
	svc = e.ok(t, http.MethodGet, path, nil)["service"].(map[string]any)
	if svc["mcpPath"] != "/mcp" {
		t.Fatalf("留空后应回到 /mcp，实际 %v", svc["mcpPath"])
	}

	bad := []string{"mcp", "http://127.0.0.1:4220/mcp", "/mcp?x=1", "/mcp path", strings.Repeat("/m", 200)}
	for _, v := range bad {
		out := e.expectStatus(t, http.MethodPut, path, testAdminToken, func() map[string]any {
			b := serviceBody(testSpec)
			b["mcpPath"] = v
			return b
		}(), http.StatusBadRequest)
		msg := out["error"].(map[string]any)["message"].(string)
		if !strings.Contains(msg, "mcpPath") {
			t.Fatalf("错误信息应点名 mcpPath，实际：%s", msg)
		}
	}
}
