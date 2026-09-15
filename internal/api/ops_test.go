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
	if js := e.getRaw(t, "/panel/app.js"); !strings.Contains(js, "/v1/snapshot") {
		t.Error("面板 JS 未包含拉取用法示例")
	}
	if css := e.getRaw(t, "/panel/styles.css"); len(css) < 100 {
		t.Errorf("面板 CSS 异常：%d 字节", len(css))
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
