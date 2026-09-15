package api

import (
	"net/http"
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

func TestDevModeWithoutAdminToken(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.AdminToken = "" })
	out := e.expectStatus(t, http.MethodPost, "/v1/namespaces", "", map[string]any{"name": "dev"}, http.StatusCreated)
	if ns, _ := out["namespace"].(map[string]any); ns == nil {
		t.Fatalf("响应缺少 namespace：%v", out)
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
