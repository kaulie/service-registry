package apispec

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOwnOpenAPIContractParses 是"狗粮测试"：本仓库自己的 api/openapi.yaml
// 必须能被本包解析出端点 —— 这既验证了 YAML 合法，也验证了我们的解析器
// 能处理真实的、写得比较完整的规范（而不是只在小样例上跑通）。
func TestOwnOpenAPIContractParses(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("读取 api/openapi.yaml 失败：%v", err)
	}
	res, err := Parse(raw)
	if err != nil {
		t.Fatalf("本仓库的 api/openapi.yaml 无法被解析：%v", err)
	}
	if res.Format != "yaml" {
		t.Errorf("应为 YAML 格式，实际 %q", res.Format)
	}
	if res.Title != "service-registry" {
		t.Errorf("info.title 应为 service-registry，实际 %q", res.Title)
	}
	if len(res.Endpoints) < 20 {
		t.Fatalf("端点数异常少（%d），规范可能被截断", len(res.Endpoints))
	}

	want := []struct{ method, path string }{
		{"GET", "/health"},
		{"GET", "/v1/snapshot"},
		{"GET", "/v1/changes"},
		{"GET", "/v1/events"},
		{"GET", "/v1/services"},
		{"GET", "/v1/departments"},
		{"GET", "/v1/search/apis"},
		{"POST", "/v1/namespaces"},
		{"PUT", "/v1/namespaces/{ns}/services/{svc}"},
		{"GET", "/v1/namespaces/{ns}/services/{svc}/spec"},
		{"PUT", "/v1/namespaces/{ns}/services/{svc}/instances"},
		{"POST", "/v1/namespaces/{ns}/services/{svc}/instances"},
		{"PATCH", "/v1/namespaces/{ns}/services/{svc}/instances/{id}"},
		{"DELETE", "/v1/namespaces/{ns}/services/{svc}/instances/{id}"},
	}
	got := map[string]bool{}
	for _, ep := range res.Endpoints {
		got[ep.Method+" "+ep.Path] = true
	}
	for _, w := range want {
		if !got[w.method+" "+w.path] {
			t.Errorf("自述契约里缺少端点：%s %s", w.method, w.path)
		}
	}
}
