package api

import (
	"net/http"
	"testing"

	"github.com/kaulie/service-registry/internal/config"
)

func TestValidationAndConflictErrors(t *testing.T) {
	e := newEnv(t, nil)
	seedService(t, e)

	// 名称不合法 / 命名空间不存在 / 缺少 API 来源 / 规格无法解析。
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/team-a/services/Bad_Name", testAdminToken, serviceBody(""), http.StatusBadRequest)
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/ghost/services/svc", testAdminToken, serviceBody(""), http.StatusNotFound)
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/team-a/services/svc", testAdminToken,
		map[string]any{"api": map[string]any{}}, http.StatusBadRequest)
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/team-a/services/svc", testAdminToken,
		map[string]any{"api": map[string]any{"spec": "not-a-spec"}}, http.StatusBadRequest)
	// healthPath 必须是路径。
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/team-a/services/svc", testAdminToken,
		map[string]any{"healthPath": "health", "api": map[string]any{"spec": testSpec}}, http.StatusBadRequest)

	base := "/v1/namespaces/team-a/services/event-center/instances"
	// host 带协议 / 端口越界 / 未登记服务。
	e.expectStatus(t, http.MethodPost, base, testAdminToken,
		map[string]any{"scheme": "http", "host": "http://10.0.0.1", "port": 80}, http.StatusBadRequest)
	e.expectStatus(t, http.MethodPost, base, testAdminToken,
		map[string]any{"scheme": "http", "host": "10.0.0.1", "port": 99999}, http.StatusBadRequest)
	e.expectStatus(t, http.MethodPost, base, testAdminToken,
		map[string]any{"scheme": "ftp", "host": "10.0.0.1", "port": 80}, http.StatusBadRequest)
	e.expectStatus(t, http.MethodPost, "/v1/namespaces/team-a/services/ghost/instances", testAdminToken,
		map[string]any{"scheme": "http", "host": "10.0.0.1", "port": 80}, http.StatusNotFound)

	// 同地址重复登记 → 409。
	e.expectStatus(t, http.MethodPost, base, testAdminToken,
		map[string]any{"scheme": "http", "host": "10.0.0.1", "port": 80}, http.StatusCreated)
	e.expectStatus(t, http.MethodPost, base, testAdminToken,
		map[string]any{"scheme": "http", "host": "10.0.0.1", "port": 80}, http.StatusConflict)

	// 同步的实例数上限。
	tooMany := make([]map[string]any, 0, maxSyncInstances+1)
	for i := 0; i <= maxSyncInstances; i++ {
		tooMany = append(tooMany, map[string]any{"scheme": "http", "host": "10.0.1.1", "port": 1000 + i})
	}
	e.expectStatus(t, http.MethodPut, base, testAdminToken, map[string]any{"instances": tooMany}, http.StatusBadRequest)

	// 内联 spec 超过上限 → 400。
	small := newEnv(t, func(c *config.Config) { c.MaxSpecBytes = 32 })
	small.expectStatus(t, http.MethodPut, "/v1/namespaces/default/services/big", testAdminToken, serviceBody(testSpec), http.StatusBadRequest)
}

func TestUnknownRoutesAndMethods(t *testing.T) {
	e := newEnv(t, nil)
	e.expectStatus(t, http.MethodGet, "/nope", "", nil, http.StatusNotFound)
	e.expectStatus(t, http.MethodPost, "/v1/services", testAdminToken, map[string]any{}, http.StatusMethodNotAllowed)
}

func TestPickRejectsUnsupportedMode(t *testing.T) {
	e := newEnv(t, nil)
	seedService(t, e)
	e.ok(t, http.MethodPost, "/v1/namespaces/team-a/services/event-center/instances",
		map[string]any{"scheme": "http", "host": "10.0.0.1", "port": 80})
	// 只保留无状态的 random：round_robin/weighted 在多消费方共用的注册中心里语义不成立。
	e.expectStatus(t, http.MethodGet,
		"/v1/namespaces/team-a/services/event-center/instances?pick=round_robin",
		"", nil, http.StatusBadRequest)
}

func TestEmptyServiceHasNoInstances(t *testing.T) {
	e := newEnv(t, nil)
	seedService(t, e)
	// 没有实例时 pick 应 404，并给出可操作提示。
	out := e.expectStatus(t, http.MethodGet,
		"/v1/namespaces/team-a/services/event-center/instances?pick=random", "", nil, http.StatusNotFound)
	if errObj, _ := out["error"].(map[string]any); errObj == nil {
		t.Fatalf("错误响应应为统一错误结构：%v", out)
	}
}
