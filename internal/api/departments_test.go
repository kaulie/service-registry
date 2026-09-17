package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kaulie/service-registry/internal/config"
)

// 一个"组织架构服务"的替身：GET /api/v1/departments → {items:[…],types:[…]}。
func fakeOrg(t *testing.T, items string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/departments" {
			t.Errorf("应请求 /api/v1/departments，实际 %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[` + items + `],"types":["研发","测试","产品","管理"]}`))
	}))
	t.Cleanup(ts.Close)
	return ts
}

const orgItems = `{"id":"D0001","name":"SRE部门","type":"研发"},
  {"id":"D0002","name":"工程效能部门","type":"研发"},
  {"id":"D0003","name":"AI架构部门","type":"研发"}`

// withOrg 让测试环境指向一个"活着的"组织接口。
func withOrg(ts *httptest.Server) func(*config.Config) {
	return func(c *config.Config) {
		c.OrgURL = ts.URL
		c.OrgTimeout = time.Second
		c.OrgCacheTTL = time.Second
	}
}

// TestDepartmentsCatalogEndpoint 覆盖 GET /v1/departments：
// 本中心不拥有部门数据，它把组织接口的目录取回来透出，并带上"数据成色"。
func TestDepartmentsCatalogEndpoint(t *testing.T) {
	ts := fakeOrg(t, orgItems)
	e := newEnv(t, withOrg(ts))

	out := e.ok(t, http.MethodGet, "/v1/departments", nil)
	if out["source"] != "organization" {
		t.Fatalf("source 应标明数据来自组织接口，实际 %v", out["source"])
	}
	if out["enabled"] != true || out["available"] != true || out["stale"] == true {
		t.Fatalf("首次应是「已配置 + 拿到新鲜数据」：%v", out)
	}
	deps := out["departments"].([]any)
	if len(deps) != 3 {
		t.Fatalf("应透出 3 个部门，实际 %d", len(deps))
	}
	first := deps[0].(map[string]any)
	if first["id"] != "D0001" || first["name"] != "SRE部门" || first["type"] != "研发" {
		t.Fatalf("部门字段应原样透出（id/name/type）：%v", first)
	}
	if types := out["types"].([]any); len(types) != 4 {
		t.Fatalf("部门类型候选也应透出，实际 %v", types)
	}

	// TTL 内再取一次：说明是缓存（未重复出网）。
	cached := e.ok(t, http.MethodGet, "/v1/departments", nil)
	if cached["cached"] != true || cached["available"] != true {
		t.Fatalf("TTL 内应命中缓存：%v", cached)
	}

	// refresh=1 强制出网。
	forced := e.ok(t, http.MethodGet, "/v1/departments?refresh=1", nil)
	if forced["cached"] == true {
		t.Fatalf("refresh=1 应跳过缓存：%v", forced)
	}
}

// TestDepartmentsCatalogUnavailable 覆盖：组织接口不可达时**不报错**，而是说明数据成色。
// （面板要能显示"暂时不可达、这是上次的数据"，而不是整页转圈。）
func TestDepartmentsCatalogUnavailable(t *testing.T) {
	ts := fakeOrg(t, orgItems)
	url := ts.URL
	ts.Close() // 关掉：连不上
	e := newEnv(t, func(c *config.Config) {
		c.OrgURL = url
		c.OrgTimeout = 300 * time.Millisecond
		c.OrgCacheTTL = 0
	})

	out := e.ok(t, http.MethodGet, "/v1/departments", nil) // 依然 200
	if out["available"] != false || out["enabled"] != true {
		t.Fatalf("应是「已配置但这次取不到」：%v", out)
	}
	if msg, _ := out["error"].(string); msg == "" {
		t.Fatalf("必须给出失败原因，实际 %v", out)
	}
	if deps, ok := out["departments"].([]any); !ok || len(deps) != 0 {
		t.Fatalf("没有任何成功数据时 departments 应是空数组：%v", out["departments"])
	}
}

// TestDepartmentsDisabled 覆盖：未配置组织接口（off）时的语义 —— 部门只当标签，
// 一切照常（写契约不因此失败）。
func TestDepartmentsDisabled(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.OrgURL = "" })

	out := e.ok(t, http.MethodGet, "/v1/departments", nil)
	if out["enabled"] != false {
		t.Fatalf("未配置时应 enabled=false：%v", out)
	}

	// 写契约照常成功，响应里说明"按声明值保存"。
	body := serviceBody(testSpec)
	body["departmentName"] = "手工写的部门"
	got := e.ok(t, http.MethodPut, "/v1/namespaces/default/services/event-center", body)
	if note, _ := got["departmentNote"].(string); !strings.Contains(note, "未配置") {
		t.Fatalf("应说明组织接口未配置，实际 %q", note)
	}
	svc := got["service"].(map[string]any)
	if svc["departmentName"] != "手工写的部门" {
		t.Fatalf("部门按声明值保存：%v", svc["departmentName"])
	}
}

// TestDepartmentSyncOnWrite 覆盖「数据从组织接口同步」的核心：
// 只给名字 → 补全成权威的 ID + 名称；只给 ID → 补全名称；不给 → 清空。
func TestDepartmentSyncOnWrite(t *testing.T) {
	ts := fakeOrg(t, orgItems)
	e := newEnv(t, withOrg(ts))
	path := "/v1/namespaces/default/services/event-center"

	// 1) 只给名称：对齐出 ID（大小写/写法不精确也能对上）。
	body := serviceBody(testSpec)
	body["departmentName"] = "sre部门"
	got := e.ok(t, http.MethodPut, path, body)
	svc := got["service"].(map[string]any)
	if svc["departmentId"] != "D0001" || svc["departmentName"] != "SRE部门" {
		t.Fatalf("只给名字时应补全成权威值：%v / %v", svc["departmentId"], svc["departmentName"])
	}
	if note, _ := got["departmentNote"].(string); !strings.Contains(note, "对齐") {
		t.Fatalf("响应应说明已按组织接口对齐，实际 %q", note)
	}

	// 2) 只给 ID：对齐出名称。
	body = serviceBody(testSpec)
	body["departmentId"] = "D0002"
	svc = e.ok(t, http.MethodPut, path, body)["service"].(map[string]any)
	if svc["departmentId"] != "D0002" || svc["departmentName"] != "工程效能部门" {
		t.Fatalf("只给 ID 时应补全名称：%v / %v", svc["departmentId"], svc["departmentName"])
	}

	// 3) 两端空白自动去掉（运维复制粘贴常带上）。
	body = serviceBody(testSpec)
	body["departmentId"] = "  D0003  "
	svc = e.ok(t, http.MethodPut, path, body)["service"].(map[string]any)
	if svc["departmentId"] != "D0003" || svc["departmentName"] != "AI架构部门" {
		t.Fatalf("应去掉两端空白再对齐：%v / %v", svc["departmentId"], svc["departmentName"])
	}

	// 4) 不给部门 → 清空（PUT 是整份覆盖）。
	svc = e.ok(t, http.MethodPut, path, serviceBody(testSpec))["service"].(map[string]any)
	if v, ok := svc["departmentId"]; ok && v != "" {
		t.Fatalf("不带部门时应清空，实际 %v", v)
	}
	if v, ok := svc["departmentName"]; ok && v != "" {
		t.Fatalf("不带部门时应清空，实际 %v", v)
	}
}

// TestDepartmentUnknownIDRejected 覆盖：目录可用且非空时，明确给了查不到的 departmentId
// 就被挡住（挡住的是脚本/手写抄错的 ID；面板下拉按构造不会出错）。
func TestDepartmentUnknownIDRejected(t *testing.T) {
	ts := fakeOrg(t, orgItems)
	e := newEnv(t, withOrg(ts))

	body := serviceBody(testSpec)
	body["departmentId"] = "D9999"
	out := e.expectStatus(t, http.MethodPut, "/v1/namespaces/default/services/bad", testAdminToken, body, http.StatusBadRequest)
	msg := out["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "D9999") || !strings.Contains(msg, "SRE部门") {
		t.Fatalf("错误信息应点名非法 ID 且列出可用部门，实际：%s", msg)
	}

	// 目录里还没有任何部门时不该误判：按声明值保存（组织服务重启会清空它的内存数据）。
	empty := fakeOrg(t, "")
	e2 := newEnv(t, withOrg(empty))
	body = serviceBody(testSpec)
	body["departmentId"] = "D9999"
	svc := e2.ok(t, http.MethodPut, "/v1/namespaces/default/services/declared", body)["service"].(map[string]any)
	if svc["departmentId"] != "D9999" {
		t.Fatalf("目录为空时应按声明值保存，实际 %v", svc)
	}

	// 组织接口不可达时同样不该失败（别人的服务挂了不能拖垮本中心的写路径）。
	dead := fakeOrg(t, orgItems)
	url := dead.URL
	dead.Close()
	e3 := newEnv(t, func(c *config.Config) { c.OrgURL = url; c.OrgTimeout = 300 * time.Millisecond })
	body = serviceBody(testSpec)
	body["departmentId"] = "D9999"
	svc = e3.ok(t, http.MethodPut, "/v1/namespaces/default/services/offline", body)["service"].(map[string]any)
	if svc["departmentId"] != "D9999" {
		t.Fatalf("组织接口不可达时应按声明值保存，实际 %v", svc)
	}
}

// TestDepartmentValidation 覆盖形状校验：只保证"存进去的值是干净的"（不查存在性）。
func TestDepartmentValidation(t *testing.T) {
	e := newEnv(t, nil)
	path := "/v1/namespaces/default/services/event-center"

	withDept := func(id, name string) map[string]any {
		body := serviceBody(testSpec)
		if id != "" {
			body["departmentId"] = id
		}
		if name != "" {
			body["departmentName"] = name
		}
		return body
	}

	for _, bad := range [][2]string{
		{strings.Repeat("D", maxDeptIDLen+1), ""},
		{"", strings.Repeat("部", maxDeptNameLen+1)},
		{"D0001\nX", ""},  // 控制字符（换行）
		{"", "SRE部门\tAB"}, // 制表符
	} {
		out := e.expectStatus(t, http.MethodPut, path, testAdminToken, withDept(bad[0], bad[1]), http.StatusBadRequest)
		msg := out["error"].(map[string]any)["message"].(string)
		if !strings.Contains(msg, "department") {
			t.Fatalf("错误信息应点名字段，实际：%s", msg)
		}
	}

	// 中文部门名（ID 与名称同时给出）是合法的。
	svc := e.ok(t, http.MethodPut, path, withDept("D0001", "SRE部门"))["service"].(map[string]any)
	if svc["departmentName"] != "SRE部门" || svc["departmentId"] != "D0001" {
		t.Fatalf("合法部门应原样保存：%v", svc)
	}
}

// TestDepartmentReadBack 覆盖部门的读取链路：详情 / 列表 / 按部门过滤 /
// 关键字检索 / 快照 / API 反查都要带上部门。
func TestDepartmentReadBack(t *testing.T) {
	ts := fakeOrg(t, orgItems)
	e := newEnv(t, withOrg(ts))

	body := serviceBody(testSpec)
	body["departmentId"] = "D0001"
	e.ok(t, http.MethodPut, "/v1/namespaces/default/services/event-center", body)

	other := serviceBody(testSpec)
	other["departmentId"] = "D0002"
	e.ok(t, http.MethodPut, "/v1/namespaces/default/services/billing", other)

	// 详情
	svc := e.ok(t, http.MethodGet, "/v1/namespaces/default/services/event-center", nil)["service"].(map[string]any)
	if svc["departmentId"] != "D0001" || svc["departmentName"] != "SRE部门" {
		t.Fatalf("详情里应带部门：%v", svc)
	}

	// 列表 + 按部门过滤（ID 或名称命中其一即可）
	list := e.ok(t, http.MethodGet, "/v1/services?department=D0001", nil)
	if n := len(list["services"].([]any)); n != 1 {
		t.Fatalf("按部门 ID 过滤应命中 1 个，实际 %d", n)
	}
	qs := url.QueryEscape("工程效能部门") // 中文要转义
	list = e.ok(t, http.MethodGet, "/v1/services?department="+qs, nil)
	if n := len(list["services"].([]any)); n != 1 {
		t.Fatalf("按部门名过滤应命中 1 个，实际 %d", n)
	}
	list = e.ok(t, http.MethodGet, "/v1/services?department="+url.QueryEscape("不存在的部门"), nil)
	if n := len(list["services"].([]any)); n != 0 {
		t.Fatalf("过滤不存在的部门应命中 0 个，实际 %d", n)
	}

	// 关键字检索（?q= 也覆盖部门）
	hits := e.ok(t, http.MethodGet, "/v1/services?q="+url.QueryEscape("SRE部门"), nil)
	if n := len(hits["services"].([]any)); n != 1 {
		t.Fatalf("按部门名检索应命中 1 个，实际 %d", n)
	}

	// 快照（给其他平台拉取）
	snap := e.ok(t, http.MethodGet, "/v1/snapshot", nil)["services"].([]any)
	if len(snap) != 2 {
		t.Fatalf("快照应有 2 个服务，实际 %d", len(snap))
	}
	var ec map[string]any
	for _, s := range snap {
		if m := s.(map[string]any); m["name"] == "event-center" {
			ec = m
		}
	}
	if ec == nil || ec["departmentName"] != "SRE部门" || ec["departmentId"] != "D0001" {
		t.Fatalf("快照里应带部门：%v", ec)
	}

	// API 反查：找接口时顺手就知道归属哪个部门，并支持 ?department= 过滤
	matches := e.ok(t, http.MethodGet, "/v1/search/apis?method=GET&path=/health", nil)["matches"].([]any)
	if len(matches) != 2 {
		t.Fatalf("两个服务都有 /health，应命中 2 条，实际 %d", len(matches))
	}
	if m := matches[0].(map[string]any); m["departmentName"] == nil {
		t.Fatalf("API 反查结果里应带部门：%v", m)
	}
	filtered := e.ok(t, http.MethodGet, "/v1/search/apis?method=GET&path=/health&department=D0002", nil)["matches"].([]any)
	if len(filtered) != 1 {
		t.Fatalf("按部门过滤 API 反查应剩 1 条，实际 %d", len(filtered))
	}
	if m := filtered[0].(map[string]any); m["service"] != "billing" {
		t.Fatalf("过滤后应是 billing，实际 %v", m["service"])
	}

	// 部门属性进审计说明（"这个服务归哪个部门"是审计时最常被问的事实）。
	changes := e.ok(t, http.MethodGet, "/v1/changes?limit=20", nil)["changes"].([]any)
	found := false
	for _, c := range changes {
		if detail, _ := c.(map[string]any)["detail"].(string); strings.Contains(detail, "部门 SRE部门（D0001）") {
			found = true
		}
	}
	if !found {
		t.Fatalf("变更/审计说明里应写明部门：%v", changes)
	}
}

// TestMetaExposesOrganization 覆盖 /v1/meta 里的组织接口信息（面板靠它解释"下拉为什么空"）。
func TestMetaExposesOrganization(t *testing.T) {
	ts := fakeOrg(t, orgItems)
	e := newEnv(t, withOrg(ts))

	meta := e.ok(t, http.MethodGet, "/v1/meta", nil)
	org, ok := meta["organization"].(map[string]any)
	if !ok {
		t.Fatalf("/v1/meta 应带 organization 段：%v", meta)
	}
	if org["enabled"] != true || org["url"] != ts.URL || org["departmentsPath"] != "/api/v1/departments" {
		t.Fatalf("organization 段内容不对：%v", org)
	}
	if org["cacheTtlSeconds"].(float64) <= 0 {
		t.Fatalf("应报出缓存时长：%v", org)
	}
}
