package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOpsEndpointsAndPanel(t *testing.T) {
	e := newEnv(t, nil)
	seedService(t, e)

	health := e.ok(t, http.MethodGet, "/health", nil)
	if health["status"] != "ok" || health["version"] != "test-version" {
		t.Fatalf("health 响应错误：%v", health)
	}
	e.expectStatus(t, http.MethodGet, "/healthz", "", nil, http.StatusOK)
	if ready := e.ok(t, http.MethodGet, "/readyz", nil); ready["status"] != "ready" {
		t.Fatalf("readyz 响应错误：%v", ready)
	}

	meta := e.ok(t, http.MethodGet, "/v1/meta", nil)
	counts := meta["counts"].(map[string]any)
	if counts["services"].(float64) != 1 || counts["endpoints"].(float64) != 3 {
		t.Fatalf("meta 统计错误：%v", counts)
	}
	if !strings.Contains(meta["semantics"].(string), "不探活") {
		t.Fatalf("meta 应显式声明语义边界：%v", meta["semantics"])
	}

	// /metrics 的读数是抓取时现算的，应立刻反映真实数据。
	body := e.getRaw(t, "/metrics")
	for _, want := range []string{
		"registry_services 1",
		"registry_instances 0",
		"registry_endpoints 3",
		"registry_revision",
		"registry_http_requests_total{",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics 缺少 %q", want)
		}
	}

	// 面板：根路径 302 到 /panel/，静态资源可访问。
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get(e.ts.URL + "/")
	if err != nil {
		t.Fatalf("请求 / 失败：%v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/panel/" {
		t.Fatalf("根路径应 302 到 /panel/，实际 %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	html := e.getRaw(t, "/panel/")
	if !strings.Contains(html, "Service Registry") || !strings.Contains(html, "服务目录") {
		t.Fatalf("面板 HTML 异常：%s", trunc(html, 200))
	}
	// 面板必须能用图形界面**登记服务**与**新增实例**（曾漏掉，回归护栏）。
	// 契约的登记/编辑在**独立页面** contract.html：面板这边只是指过去的链接（可新页签打开）。
	for _, want := range []string{
		"＋ 登记服务契约", "＋ 新增实例", "声明式批量同步",
		`id="svc-new"`, `href="contract.html"`, `target="_blank"`, `src="shared.js"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("面板 HTML 缺少登记服务/实例的入口：%q", want)
		}
	}
	if strings.Contains(html, `id="svc-form-card"`) {
		t.Errorf("面板 HTML 里不该再有内联的契约表单（已挪到独立页面 contract.html）")
	}

	form := e.getRaw(t, "/panel/contract.html")
	for _, want := range []string{
		"登记服务契约", "openapi: 3.0.3", "预填最小模板", "重新同步部门",
		`id="svc-form-submit" type="button"`, `src="shared.js"`, `src="contract.js"`,
		`href="./#services"`, // 保存后回面板（页签写进 URL，落回服务目录）
		// 注册对象类型：service/app，app 时额外编辑 APP_ID 与操作系统。
		`id="svc-form-type"`, `id="svc-form-appid"`, `id="svc-form-os"`,
	} {
		if !strings.Contains(form, want) {
			t.Errorf("契约编辑页 HTML 缺少 %q", want)
		}
	}

	shared := e.getRaw(t, "/panel/shared.js")
	for _, want := range []string{
		"unhandledrejection", "field-error", "formFail", "hint--", "mountTokenInput", "loadDepartments",
	} {
		if !strings.Contains(shared, want) {
			t.Errorf("shared.js 缺少 %q", want)
		}
	}

	js := e.getRaw(t, "/panel/app.js")
	for _, want := range []string{
		"/v1/snapshot", "inst-batch-submit", "contract.html?", // 卡片上的「编辑契约」是独立页面的链接
		"APP_ID:", "OS:", "title=\"注册对象类型\"", // 服务卡片显示注册对象类型，app 额外显示 APP_ID 与操作系统
	} {
		if !strings.Contains(js, want) {
			t.Errorf("面板 JS 缺少 %q", want)
		}
	}
	if strings.Contains(js, "openServiceForm") {
		t.Errorf("面板 JS 里不该再有内联的契约表单逻辑（已挪到 contract.js）")
	}

	formJS := e.getRaw(t, "/panel/contract.js")
	for _, want := range []string{
		"submitContract", "svc-form-submit", "initContractPage",
		// 反馈永不静默：校验失败要 toast + 标红字段
		"formFail", "prefillSpecTemplate",
		// 注册对象类型在编辑页可编辑：service/app 切换与 app 专属字段校验。
		"syncTypeFields", "type 为 app 时必须填写 APP_ID",
	} {
		if !strings.Contains(formJS, want) {
			t.Errorf("contract.js 缺少 %q", want)
		}
	}

	css := e.getRaw(t, "/panel/styles.css")
	for _, want := range []string{".hint--error", "input.field-error", "button[disabled]"} {
		if !strings.Contains(css, want) {
			t.Errorf("面板 CSS 缺少醒目的错误样式 %q", want)
		}
	}
	if !strings.Contains(css, ".hint--ok") {
		t.Errorf("面板 CSS 缺少成功样式 .hint--ok")
	}
	// 配色：只有一套令牌 + 一个强调色，深/浅两套取值（不再到处是深浅不一的蓝）
	for _, want := range []string{"--accent:", `html[data-theme="light"]`, "--bad:", "--ok:"} {
		if !strings.Contains(css, want) {
			t.Errorf("面板 CSS 缺少配色令牌 %q", want)
		}
	}
}

func (e *testEnv) getRaw(t *testing.T, path string) string {
	t.Helper()
	res, err := http.Get(e.ts.URL + path)
	if err != nil {
		t.Fatalf("请求 %s 失败：%v", path, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("请求 %s 期望 200，实际 %d", path, res.StatusCode)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	return string(raw)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
