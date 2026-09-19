package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaulie/service-registry/internal/config"
)

// switchableOrg 是一个「可以先挂掉、之后再修好」的组织接口替身：
// 用它构造真实会发生的顺序 —— 登记时组织服务正在重启（部门只能按声明值存），
// 之后组织服务恢复（目录里已经没有那个 ID 了）。
func switchableOrg(t *testing.T, items string) (ts *httptest.Server, setUp func(bool)) {
	t.Helper()
	var mu sync.Mutex
	up := false
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		alive := up
		mu.Unlock()
		if !alive {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"组织服务正在重启"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[` + items + `],"types":["研发","测试"]}`))
	}))
	t.Cleanup(ts.Close)
	return ts, func(v bool) { mu.Lock(); up = v; mu.Unlock() }
}

// serviceNames 取出响应里 services 的名字（列表按 namespace, name 排序）。
func serviceNames(t *testing.T, out map[string]any) []string {
	t.Helper()
	raw, ok := out["services"].([]any)
	if !ok {
		t.Fatalf("services 应是 JSON 数组（空也要是 []）：%v", out["services"])
	}
	names := make([]string, 0, len(raw))
	for _, s := range raw {
		names = append(names, s.(map[string]any)["name"].(string))
	}
	return names
}

// TestListServicesByOrg 覆盖「按组织（部门）ID 查服务列表」这条封装接口：
// 过滤只认 ID（大小写不敏感）、响应带组织信息与目录成色、分页语义与 /v1/services 一致，
// 以及 404 只在「事实清楚」时才出现（目录可用且非空、ID 不在目录里、本中心也没有服务用它）。
func TestListServicesByOrg(t *testing.T) {
	ts := fakeOrg(t, orgItems)
	e := newEnv(t, withOrg(ts))

	ec := serviceBody(testSpec) // tags: [events]
	ec["departmentId"] = "D0001"
	e.ok(t, http.MethodPut, "/v1/namespaces/default/services/event-center", ec)

	billing := serviceBody(testSpec)
	billing["departmentId"] = "D0001"
	billing["owner"] = "payments"
	billing["tags"] = []string{"billing"}
	e.ok(t, http.MethodPut, "/v1/namespaces/default/services/billing", billing)

	other := serviceBody(testSpec)
	other["departmentId"] = "D0002"
	e.ok(t, http.MethodPut, "/v1/namespaces/default/services/watchdog", other)

	e.ok(t, http.MethodPost, "/v1/namespaces", map[string]any{"name": "team-a"})
	ta := serviceBody(testSpec)
	ta["departmentId"] = "D0001"
	e.ok(t, http.MethodPut, "/v1/namespaces/team-a/services/infra-agent", ta)

	// 组织视角：这个组织里有什么服务（默认全部命名空间）。
	out := e.ok(t, http.MethodGet, "/v1/orgs/D0001/services", nil)
	if got := out["total"].(float64); got != 3 {
		t.Fatalf("D0001 下应有 3 个服务，实际 %v：%v", got, out)
	}
	if names := serviceNames(t, out); strings.Join(names, ",") != "billing,event-center,infra-agent" {
		t.Fatalf("应按 namespace,name 排序返回：%v", names)
	}
	// 每个服务自带部门字段（消费方不用再联表）、且都属于这个组织。
	for _, s := range out["services"].([]any) {
		if m := s.(map[string]any); m["departmentId"] != "D0001" || m["departmentName"] != "SRE部门" {
			t.Fatalf("服务应带归属部门：%v", m)
		}
	}
	// 这个 org_id 是谁 + 目录成色。
	org, ok := out["org"].(map[string]any)
	if !ok {
		t.Fatalf("响应应带 org 段：%v", out)
	}
	if org["id"] != "D0001" || org["name"] != "SRE部门" || org["type"] != "研发" || org["resolved"] != true {
		t.Fatalf("org 段应带上组织接口里的部门信息：%v", org)
	}
	dir := out["organization"].(map[string]any)
	if dir["enabled"] != true || dir["available"] != true || dir["stale"] == true {
		t.Fatalf("应报出目录数据成色：%v", dir)
	}

	// ID 比较忽略大小写：d0001 与 D0001 是同一个组织（ID 常常是手敲/复制进来的）。
	if lower := e.ok(t, http.MethodGet, "/v1/orgs/d0001/services", nil); lower["total"].(float64) != 3 {
		t.Fatalf("小写 org_id 应命中同一个组织：%v", lower)
	}

	// 过滤与分页：total 始终是"该组织下满足条件的总数"，limit/offset 只作用于当页。
	tagged := e.ok(t, http.MethodGet, "/v1/orgs/D0001/services?tag=billing", nil)
	if tagged["total"].(float64) != 1 || serviceNames(t, tagged)[0] != "billing" {
		t.Fatalf("按 tag 过滤应剩 billing：%v", tagged)
	}
	if ns := e.ok(t, http.MethodGet, "/v1/orgs/D0001/services?namespace=team-a", nil); ns["total"].(float64) != 1 {
		t.Fatalf("按命名空间过滤应剩 1 个：%v", ns)
	}
	if q := e.ok(t, http.MethodGet, "/v1/orgs/D0001/services?q=payments", nil); q["total"].(float64) != 1 {
		t.Fatalf("关键字过滤（owner）应剩 1 个：%v", q)
	}
	page := e.ok(t, http.MethodGet, "/v1/orgs/D0001/services?limit=1&offset=1", nil)
	if page["total"].(float64) != 3 || page["limit"].(float64) != 1 || page["offset"].(float64) != 1 {
		t.Fatalf("分页字段不对：%v", page)
	}
	if names := serviceNames(t, page); len(names) != 1 || names[0] != "event-center" {
		t.Fatalf("第二页应是 event-center：%v", names)
	}

	// 目录里有、但还没有服务挂上去 → 200 + 空列表（组织确实存在，空不是错）；
	// 组织接口给了 parentId 就原样带出（本中心不做层级加工）。
	empty := e.ok(t, http.MethodGet, "/v1/orgs/D0003/services", nil)
	if empty["total"].(float64) != 0 || len(serviceNames(t, empty)) != 0 {
		t.Fatalf("D0003 下应没有服务：%v", empty)
	}
	if o := empty["org"].(map[string]any); o["resolved"] != true || o["parentId"] != "D0001" {
		t.Fatalf("D0003 应能确认，并带出 parentId：%v", o)
	}

	// 目录可用且非空 + ID 不在目录里 + 本中心也没有服务用它 → 事实清楚，404 并把可用部门报出来。
	missing := e.expectStatus(t, http.MethodGet, "/v1/orgs/D9999/services", testAdminToken, nil, http.StatusNotFound)
	errObj := missing["error"].(map[string]any)
	if errObj["code"] != "not_found" {
		t.Fatalf("应为 not_found：%v", errObj)
	}
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "D9999") || !strings.Contains(msg, "SRE部门") {
		t.Fatalf("404 应说明原因并列出可用部门：%v", msg)
	}

	// 只支持 GET（其它方法 405）。
	e.expectStatus(t, http.MethodPost, "/v1/orgs/D0001/services", testAdminToken, map[string]any{}, http.StatusMethodNotAllowed)
}

// TestListServicesByOrgUnresolvedButListed 覆盖「ID 不在目录里、但服务确实登记在它下面」：
// 必须 200 并说明原因 —— 报 404 会把真实存在的登记数据藏起来。
func TestListServicesByOrgUnresolvedButListed(t *testing.T) {
	ts, setUp := switchableOrg(t, orgItems) // 一开始是挂的
	e := newEnv(t, func(c *config.Config) {
		c.OrgURL = ts.URL
		c.OrgTimeout = 300 * time.Millisecond
		c.OrgCacheTTL = 0 // 每次都出网：免得把"组织服务挂了"这次的结果缓存住
	})

	// 登记时组织服务不可达 → 按声明值保存（本中心的写路径不因为别人挂了而失败）。
	ghost := serviceBody(testSpec)
	ghost["departmentId"] = "D0042"
	out := e.ok(t, http.MethodPut, "/v1/namespaces/default/services/ghost", ghost)
	if note, _ := out["departmentNote"].(string); !strings.Contains(note, "D0042") {
		t.Fatalf("登记时应提示部门没能对齐：%v", out)
	}

	// 组织服务恢复：目录里没有 D0042，但服务登记在它下面 → 200（不是 404）。
	setUp(true)
	got := e.ok(t, http.MethodGet, "/v1/orgs/D0042/services", nil)
	if got["total"].(float64) != 1 || serviceNames(t, got)[0] != "ghost" {
		t.Fatalf("已登记的契约照常列出：%v", got)
	}
	org := got["org"].(map[string]any)
	if org["resolved"] != false {
		t.Fatalf("不在目录里就不该说 resolved：%v", org)
	}
	note, _ := org["note"].(string)
	if !strings.Contains(note, "不在组织接口的部门目录里") || !strings.Contains(note, "1 个服务") {
		t.Fatalf("应说明「目录里没有它，但仍有 1 个服务登记在下面」：%v", note)
	}
	if dir := got["organization"].(map[string]any); dir["available"] != true {
		t.Fatalf("此时目录是好的：%v", dir)
	}

	// 目录恢复之后，这个 ID 不再能被**新登记**进来（写路径 400，挡的是抄错的 ID）……
	reject := serviceBody(testSpec)
	reject["departmentId"] = "D0042"
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/default/services/ghost2", testAdminToken, reject, http.StatusBadRequest)
	// ……但已经登记的契约不会被改写（本中心不做"事后追认"，只如实报告）。
	after := e.ok(t, http.MethodGet, "/v1/namespaces/default/services/ghost", nil)["service"].(map[string]any)
	if after["departmentId"] != "D0042" {
		t.Fatalf("已登记的服务不该被目录的现状改写：%v", after)
	}
}

// TestListServicesByOrgDirectoryDown 覆盖失败方向：组织接口不可达时，
// **按组织查服务照常工作**（200 + 照常列出服务），只把"这份组织信息没能确认"讲清楚；
// 也正因为无法确认，这时连"不存在的组织"都不能报 404。
func TestListServicesByOrgDirectoryDown(t *testing.T) {
	ts := fakeOrg(t, orgItems)
	url := ts.URL
	ts.Close() // 组织服务挂了
	e := newEnv(t, func(c *config.Config) {
		c.OrgURL = url
		c.OrgTimeout = 300 * time.Millisecond
		c.OrgCacheTTL = 0
	})

	body := serviceBody(testSpec)
	body["departmentId"] = "D0001"
	e.ok(t, http.MethodPut, "/v1/namespaces/default/services/event-center", body)

	out := e.ok(t, http.MethodGet, "/v1/orgs/D0001/services", nil) // 依然 200
	if out["total"].(float64) != 1 {
		t.Fatalf("组织接口挂了也要能按 ID 查到服务：%v", out)
	}
	dir := out["organization"].(map[string]any)
	if dir["enabled"] != true || dir["available"] != false {
		t.Fatalf("应如实报告目录这次取不到：%v", dir)
	}
	if msg, _ := dir["error"].(string); msg == "" {
		t.Fatalf("应带上失败原因：%v", dir)
	}
	org := out["org"].(map[string]any)
	if org["resolved"] != false {
		t.Fatalf("没能确认就不该说 resolved：%v", org)
	}
	if note, _ := org["note"].(string); !strings.Contains(note, "不可达") {
		t.Fatalf("应说明是组织接口不可达：%v", note)
	}

	missing := e.ok(t, http.MethodGet, "/v1/orgs/D9999/services", nil)
	if missing["total"].(float64) != 0 {
		t.Fatalf("D9999 下应没有服务：%v", missing)
	}
}

// TestListServicesByOrgWithoutOrgInterface 覆盖未配置组织接口（REGISTRY_ORG_URL=off）：
// 部门只当标签，本接口照常按 ID 查（只是无法确认组织是谁）。
func TestListServicesByOrgWithoutOrgInterface(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.OrgURL = "" })

	body := serviceBody(testSpec)
	body["departmentId"] = "D0001"
	e.ok(t, http.MethodPut, "/v1/namespaces/default/services/event-center", body)

	out := e.ok(t, http.MethodGet, "/v1/orgs/D0001/services", nil)
	if out["total"].(float64) != 1 {
		t.Fatalf("未配置组织接口时也应能按 ID 查：%v", out)
	}
	if dir := out["organization"].(map[string]any); dir["enabled"] != false {
		t.Fatalf("应标明组织接口未配置：%v", dir)
	}
	if note, _ := out["org"].(map[string]any)["note"].(string); !strings.Contains(note, "未配置") {
		t.Fatalf("应说明未经目录校验：%v", note)
	}
}

// TestListServicesByOrgReadAuth 覆盖读权限收紧时这条读路径同样要令牌（与 /v1/services 一致）。
func TestListServicesByOrgReadAuth(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.OrgURL = ""; c.ReadAuthRequired = true })
	e.expectStatus(t, http.MethodGet, "/v1/orgs/D0001/services", "", nil, http.StatusUnauthorized)
	e.expectStatus(t, http.MethodGet, "/v1/orgs/D0001/services", testAdminToken, nil, http.StatusOK)
}
