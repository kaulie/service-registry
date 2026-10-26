package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaulie/service-registry/internal/config"
)

const testSpec = `{
  "openapi": "3.0.3",
  "info": {"title": "event-center", "version": "1.0.0"},
  "paths": {
    "/health": {"get": {"summary": "健康检查"}},
    "/v1/ingest/{source}": {"post": {"summary": "通用注入"}},
    "/v1/streams/{stream}/events": {"get": {"summary": "拉取事件"}}
  }
}`

func serviceBody(spec string) map[string]any {
	api := map[string]any{
		"protocols":   []string{"http"},
		"authSchemes": []map[string]string{{"scheme": "bearer", "in": "header", "name": "Authorization"}},
		"docsUrl":     "https://example.com/docs",
	}
	if spec != "" {
		api["spec"] = spec
	} else {
		api["endpoints"] = []map[string]any{{"method": "GET", "path": "/manual"}}
	}
	return map[string]any{
		"version": "1.0.0", "owner": "tester", "description": "测试服务",
		"tags": []string{"events"}, "basePath": "/", "healthPath": "/health",
		"api": api,
	}
}

func seedService(t *testing.T, e *testEnv) {
	t.Helper()
	e.ok(t, http.MethodPost, "/v1/namespaces", map[string]any{"name": "team-a", "description": "A 组"})
	e.ok(t, http.MethodPut, "/v1/namespaces/team-a/services/event-center", serviceBody(testSpec))
}

func TestAuthEnforcement(t *testing.T) {
	e := newEnv(t, nil)

	// 无令牌写入 → 401；错误令牌 → 403；正确令牌 → 2xx。
	e.expectStatus(t, http.MethodPost, "/v1/namespaces", "", map[string]any{"name": "x"}, http.StatusUnauthorized)
	e.expectStatus(t, http.MethodPost, "/v1/namespaces", "wrong", map[string]any{"name": "x"}, http.StatusForbidden)
	e.expectStatus(t, http.MethodPost, "/v1/namespaces", testAdminToken, map[string]any{"name": "x"}, http.StatusCreated)

	// 读接口默认开放（便于各平台拉取）。
	e.expectStatus(t, http.MethodGet, "/v1/services", "", nil, http.StatusOK)

	// 命名空间令牌：只能写自己的命名空间，且不能做管理操作。
	created := e.ok(t, http.MethodPost, "/v1/namespaces", map[string]any{"name": "team-b"})
	nsToken, _ := created["token"].(string)
	if nsToken == "" {
		t.Fatal("创建命名空间应一次性返回令牌")
	}
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/team-b/services/svc", nsToken, serviceBody(""), http.StatusCreated)
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/default/services/svc", nsToken, serviceBody(""), http.StatusForbidden)
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/team-b/services/svc2", "wrong", serviceBody(""), http.StatusForbidden)
	e.expectStatus(t, http.MethodPost, "/v1/namespaces", nsToken, map[string]any{"name": "team-c"}, http.StatusForbidden)
}

func TestReadAuthCanBeRequired(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.ReadAuthRequired = true })
	e.expectStatus(t, http.MethodGet, "/v1/services", "", nil, http.StatusUnauthorized)
	e.expectStatus(t, http.MethodGet, "/v1/services", testAdminToken, nil, http.StatusOK)
	// 运维端点始终开放：平台探针不该依赖令牌。
	e.expectStatus(t, http.MethodGet, "/health", "", nil, http.StatusOK)
}

// TestOpenWriteAuth 覆盖"写接口默认开放"（REGISTRY_WRITE_AUTH=open）的语义：
// 不带令牌能写（actor=anonymous，审计仍可追），带合法令牌按身份记账，带错令牌照样 403。
func TestOpenWriteAuth(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.WriteAuthOpen = true })

	// 无令牌写：放行，且连管理接口（建命名空间）也放行。
	out := e.expectStatus(t, http.MethodPost, "/v1/namespaces", "", map[string]any{"name": "open-ns"}, http.StatusCreated)
	if ns, _ := out["namespace"].(map[string]any); ns == nil {
		t.Fatalf("响应缺少 namespace：%v", out)
	}
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/open-ns/services/svc", "", serviceBody(""), http.StatusCreated)

	// 审计：开放模式下的写入记为 anonymous。
	changes := e.ok(t, http.MethodGet, "/v1/changes?since=0", nil)["changes"].([]any)
	if actor := changes[0].(map[string]any)["actor"]; actor != "anonymous" {
		t.Fatalf("开放模式下无令牌写入应记 anonymous，实际 %v", actor)
	}

	// 带合法令牌 → 按身份记账（审计里能看出是谁改的）。
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/open-ns/services/svc", testAdminToken, serviceBody(""), http.StatusOK)
	changes = e.ok(t, http.MethodGet, "/v1/changes?since=0", nil)["changes"].([]any)
	last := changes[len(changes)-1].(map[string]any)
	if last["actor"] != "admin" {
		t.Fatalf("带 tokens 的写入应记 admin，实际 %v", last["actor"])
	}

	// 带错令牌：不静默忽略，直接 403。
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/open-ns/services/svc", "wrong", serviceBody(""), http.StatusForbidden)

	// /v1/meta 暴露当前写权限状态，便于运维与面板显示。
	if meta := e.ok(t, http.MethodGet, "/v1/meta", nil); meta["writeAuth"] != "open" {
		t.Fatalf("meta.writeAuth 应为 open：%v", meta["writeAuth"])
	}
}

// TestOpenWriteAuthRejectsPublicClients 覆盖「开放写入只对回环/内网生效」：
// 部署脚本把进程绑到 0.0.0.0 之后，公网来源不带令牌不能改元信息，读接口仍然开放。
func TestOpenWriteAuthRejectsPublicClients(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.WriteAuthOpen = true })

	public := func(method, path, token string, body any) (int, map[string]any) {
		t.Helper()
		var reader *bytes.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("序列化请求体失败：%v", err)
			}
			reader = bytes.NewReader(raw)
		}
		var req *http.Request
		var err error
		if reader != nil {
			req, err = http.NewRequest(method, path, reader)
		} else {
			req, err = http.NewRequest(method, path, nil)
		}
		if err != nil {
			t.Fatalf("构造请求失败：%v", err)
		}
		req.RemoteAddr = "203.0.113.9:50000"
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("X-Registry-Token", token)
		}
		rr := httptest.NewRecorder()
		e.ts.Config.Handler.ServeHTTP(rr, req)
		out := map[string]any{}
		if len(bytes.TrimSpace(rr.Body.Bytes())) > 0 {
			if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
				out = map[string]any{"raw": rr.Body.String()}
			}
		}
		return rr.Code, out
	}

	if status, out := public(http.MethodPost, "/v1/namespaces", "", map[string]any{"name": "from-public"}); status != http.StatusUnauthorized {
		t.Fatalf("公网匿名写应 401，实际 %d：%v", status, out)
	}
	if status, out := public(http.MethodGet, "/v1/services", "", nil); status != http.StatusOK {
		t.Fatalf("公网读应仍然开放，实际 %d：%v", status, out)
	}
	if status, out := public(http.MethodGet, "/v1/meta", "", nil); status != http.StatusOK || out["writeAuth"] != "token" || out["writeAuthScope"] != "public" {
		t.Fatalf("公网 meta 应标明写入要令牌：%d %v", status, out)
	}
	if status, out := public(http.MethodPost, "/v1/namespaces", testAdminToken, map[string]any{"name": "from-public"}); status != http.StatusCreated {
		t.Fatalf("公网带 admin 令牌应能写，实际 %d：%v", status, out)
	}

	// 内网来源仍按 open 放行，面板徽标也保持「开放」。
	req := httptest.NewRequest(http.MethodGet, "/v1/meta", nil)
	req.RemoteAddr = "192.168.3.20:1234"
	rr := httptest.NewRecorder()
	e.ts.Config.Handler.ServeHTTP(rr, req)
	var meta map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &meta); err != nil {
		t.Fatalf("解析 meta：%v", err)
	}
	if meta["writeAuth"] != "open" || meta["writeAuthScope"] != "private" {
		t.Fatalf("内网 meta 应仍是开放写入：%v", meta)
	}
}

func TestTrustedWriteClient(t *testing.T) {
	cases := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:4240", true},
		{"[::1]:4240", true},
		{"[::ffff:127.0.0.1]:9", true},
		{"10.0.0.8:1", true},
		{"192.168.3.107:50000", true},
		{"172.17.0.1:40000", true},
		{"169.254.1.1:1", true},
		{"203.0.113.9:50000", false},
		{"8.8.8.8:53", false},
		{"[::ffff:8.8.8.8]:9", false},
		{"", false},
		{"not-an-ip", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = tc.addr
		if got := trustedWriteClient(req); got != tc.ok {
			t.Errorf("RemoteAddr %q：期望 trusted=%v，实际 %v", tc.addr, tc.ok, got)
		}
	}
}

// TestWriteAuthCanBeTightened 覆盖收紧后的行为（REGISTRY_WRITE_AUTH=token）。
func TestWriteAuthCanBeTightened(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.WriteAuthOpen = false })
	e.expectStatus(t, http.MethodPost, "/v1/namespaces", "", map[string]any{"name": "x"}, http.StatusUnauthorized)
	e.expectStatus(t, http.MethodPost, "/v1/namespaces", testAdminToken, map[string]any{"name": "x"}, http.StatusCreated)
	if meta := e.ok(t, http.MethodGet, "/v1/meta", nil); meta["writeAuth"] != "token" {
		t.Fatalf("meta.writeAuth 应为 token：%v", meta["writeAuth"])
	}
}

func TestTokenRotationAndClear(t *testing.T) {
	e := newEnv(t, nil)
	created := e.ok(t, http.MethodPost, "/v1/namespaces", map[string]any{"name": "team-r"})
	oldToken, _ := created["token"].(string)

	rotated := e.ok(t, http.MethodPost, "/v1/namespaces/team-r/token", nil)
	newToken, _ := rotated["token"].(string)
	if newToken == "" || newToken == oldToken {
		t.Fatalf("轮换应产生不同的新令牌：%q", newToken)
	}
	// 旧令牌立即失效。
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/team-r/services/s", oldToken, serviceBody(""), http.StatusForbidden)
	// 新令牌可用。
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/team-r/services/s", newToken, serviceBody(""), http.StatusCreated)

	// 清除令牌后只能由 admin 写入。
	e.ok(t, http.MethodDelete, "/v1/namespaces/team-r/token", nil)
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/team-r/services/s", newToken, serviceBody(""), http.StatusForbidden)
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/team-r/services/s", testAdminToken, serviceBody(""), http.StatusOK)
}

func TestNamespaceDeleteRequiresConfirm(t *testing.T) {
	e := newEnv(t, nil)
	seedService(t, e)
	e.expectStatus(t, http.MethodDelete, "/v1/namespaces/team-a", testAdminToken, nil, http.StatusBadRequest)
	e.expectStatus(t, http.MethodDelete, "/v1/namespaces/team-a?confirm=team-a", testAdminToken, nil, http.StatusNoContent)
	e.expectStatus(t, http.MethodGet, "/v1/namespaces/team-a", testAdminToken, nil, http.StatusNotFound)
}
