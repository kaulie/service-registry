package api

import (
	"net/http"
	"testing"
)

func TestServiceLifecycleAndDiscovery(t *testing.T) {
	e := newEnv(t, nil)
	seedService(t, e)

	// 契约：端点由内联 spec 解析而来，spec 摘要等元信息齐备。
	got := e.ok(t, http.MethodGet, "/v1/namespaces/team-a/services/event-center", nil)
	svc := got["service"].(map[string]any)
	apiPart := svc["api"].(map[string]any)
	if apiPart["hasSpec"] != true {
		t.Fatalf("应记录内联 spec：%v", apiPart)
	}
	eps := apiPart["endpoints"].([]any)
	if len(eps) != 3 {
		t.Fatalf("应从 spec 解析出 3 个端点，实际 %d：%v", len(eps), eps)
	}

	// 实例：单个登记 + 声明式同步。
	e.expectStatus(t, http.MethodPost, "/v1/namespaces/team-a/services/event-center/instances",
		testAdminToken, map[string]any{"scheme": "http", "host": "10.0.0.1", "port": 9099}, http.StatusCreated)
	sync := e.ok(t, http.MethodPut, "/v1/namespaces/team-a/services/event-center/instances", map[string]any{
		"instances": []map[string]any{
			{"scheme": "http", "host": "10.0.0.1", "port": 9099, "metadata": map[string]string{"zone": "a"}},
			{"scheme": "http", "host": "10.0.0.2", "port": 9099, "metadata": map[string]string{"zone": "b"}},
		},
	})
	if sync["updated"].(float64) != 1 || sync["created"].(float64) != 1 {
		t.Fatalf("同步结果不符合预期：%v", sync)
	}

	// 发现：列表 / 实例数 / 实例列表 / 随机取一个 / 全局按 ID 查 / 元数据过滤。
	listed := e.ok(t, http.MethodGet, "/v1/services", nil)
	services := listed["services"].([]any)
	if len(services) != 1 || listed["total"].(float64) != 1 {
		t.Fatalf("服务列表错误：%v", listed)
	}
	if services[0].(map[string]any)["instanceCount"].(float64) != 2 {
		t.Fatalf("实例计数错误：%v", services[0])
	}

	insts := e.ok(t, http.MethodGet, "/v1/namespaces/team-a/services/event-center/instances", nil)
	if len(insts["instances"].([]any)) != 2 {
		t.Fatalf("实例列表错误：%v", insts)
	}

	picked := e.ok(t, http.MethodGet, "/v1/namespaces/team-a/services/event-center/instances?pick=random", nil)
	inst := picked["instance"].(map[string]any)
	if inst["host"] == nil || inst["port"] == nil {
		t.Fatalf("pick=random 未返回实例：%v", picked)
	}
	byID := e.ok(t, http.MethodGet, "/v1/instances/"+inst["id"].(string), nil)
	if byID["instance"].(map[string]any)["service"] != "event-center" {
		t.Fatalf("按 ID 查询错误：%v", byID)
	}

	filtered := e.ok(t, http.MethodGet, "/v1/namespaces/team-a/services/event-center/instances?meta=zone=b", nil)
	if filtered["total"].(float64) != 1 {
		t.Fatalf("meta 过滤错误：%v", filtered)
	}

	// API 反查：具体路径 → 命中登记模板。
	search := e.ok(t, http.MethodGet, "/v1/search/apis?method=GET&path=/v1/streams/abc/events", nil)
	matches := search["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("API 反查失败：%v", search)
	}
	m0 := matches[0].(map[string]any)
	if m0["matchType"] != "template" || m0["service"] != "event-center" {
		t.Fatalf("命中项不正确：%v", m0)
	}

	// 内联 spec 原文可直接取回。
	if status, _ := e.do(t, http.MethodGet, "/v1/namespaces/team-a/services/event-center/spec", nil, ""); status != http.StatusOK {
		t.Fatalf("读取 spec 失败：%d", status)
	}

	// 标签过滤。
	if tagged := e.ok(t, http.MethodGet, "/v1/services?tag=events", nil); tagged["total"].(float64) != 1 {
		t.Fatalf("标签过滤错误：%v", tagged)
	}
	if empty := e.ok(t, http.MethodGet, "/v1/services?tag=nope", nil); empty["total"].(float64) != 0 {
		t.Fatalf("空标签过滤错误：%v", empty)
	}

	// 清理：删除契约后实例与端点索引一并消失（服务不存在 → 404 是诚实答案）。
	e.expectStatus(t, http.MethodDelete, "/v1/namespaces/team-a/services/event-center", testAdminToken, nil, http.StatusNoContent)
	if after := e.ok(t, http.MethodGet, "/v1/services", nil); after["total"].(float64) != 0 {
		t.Fatalf("删除后仍有服务：%v", after)
	}
	e.expectStatus(t, http.MethodGet, "/v1/namespaces/team-a/services/event-center/instances", "", nil, http.StatusNotFound)
	if search := e.ok(t, http.MethodGet, "/v1/search/apis?path=/**", nil); len(search["matches"].([]any)) != 0 {
		t.Fatalf("删除后端点索引仍在：%v", search)
	}
}

// TestExplicitEndpointsWithoutSpec 覆盖"只给 endpoint 列表、不给 spec"的登记方式。
func TestExplicitEndpointsWithoutSpec(t *testing.T) {
	e := newEnv(t, nil)
	out := e.ok(t, http.MethodPut, "/v1/namespaces/default/services/manual", serviceBody(""))
	svc := out["service"].(map[string]any)
	apiPart := svc["api"].(map[string]any)
	if apiPart["hasSpec"] != false {
		t.Fatalf("未提供 spec 时 hasSpec 应为 false：%v", apiPart)
	}
	if v, ok := apiPart["specBytes"]; ok && v.(float64) != 0 {
		t.Fatalf("未提供 spec 时不应有 specBytes：%v", apiPart)
	}
	if len(apiPart["endpoints"].([]any)) != 1 {
		t.Fatalf("显式端点应被登记：%v", apiPart["endpoints"])
	}
	if out["created"] != true {
		t.Fatalf("首次登记 created 应为 true：%v", out["created"])
	}
	// 再次 PUT 同一服务 → created=false（幂等）。
	again := e.ok(t, http.MethodPut, "/v1/namespaces/default/services/manual", serviceBody(""))
	if again["created"] != false {
		t.Fatalf("重复登记 created 应为 false：%v", again["created"])
	}
}
