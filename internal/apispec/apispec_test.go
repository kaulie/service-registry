package apispec

import (
	"strings"
	"testing"

	"github.com/kaulie/service-registry/internal/model"
)

const jsonSpec = `{
  "openapi": "3.0.3",
  "info": {"title": "demo", "version": "1.2.3"},
  "paths": {
    "/v1/items/{id}": {
      "get": {"summary": "取一件", "operationId": "getItem", "tags": ["items"]},
      "delete": {"description": "删一件"}
    },
    "/v1/items": {"post": {"summary": "新建", "security": [{"bearer": []}]}}
  }
}`

const yamlSpec = `
openapi: 3.0.3
info:
  title: demo-yaml
  version: 0.9.0
security:
  - apiKey: []
paths:
  /health:
    get:
      summary: 健康检查
`

func TestParseJSONSpec(t *testing.T) {
	res, err := Parse([]byte(jsonSpec))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if res.Format != "json" {
		t.Errorf("格式应为 json，实际 %q", res.Format)
	}
	if res.Title != "demo" || res.Version != "1.2.3" {
		t.Errorf("info 提取错误：%q %q", res.Title, res.Version)
	}
	if len(res.Endpoints) != 3 {
		t.Fatalf("端点数应为 3，实际 %d", len(res.Endpoints))
	}
	// 端点按 path 升序，同一 path 内按方法字典序。
	if res.Endpoints[0].Path != "/v1/items" || res.Endpoints[0].Method != "POST" {
		t.Errorf("排序不符合预期：%+v", res.Endpoints[0])
	}
	// 操作级 security 应被提取为鉴权方案名。
	var withAuth *model.Endpoint
	for i := range res.Endpoints {
		if res.Endpoints[i].Path == "/v1/items" && res.Endpoints[i].Method == "POST" {
			withAuth = &res.Endpoints[i]
		}
	}
	if withAuth == nil || len(withAuth.Auth) != 1 || withAuth.Auth[0] != "bearer" {
		t.Errorf("security 提取错误：%+v", withAuth)
	}
	if res.Hash != Hash([]byte(jsonSpec)) || res.Bytes != len(jsonSpec) {
		t.Errorf("hash/bytes 不正确：%s %d", res.Hash, res.Bytes)
	}
}

func TestParseYAMLSpecWithGlobalSecurity(t *testing.T) {
	res, err := Parse([]byte(yamlSpec))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if res.Format != "yaml" {
		t.Errorf("格式应为 yaml，实际 %q", res.Format)
	}
	if len(res.Endpoints) != 1 {
		t.Fatalf("端点数应为 1，实际 %d", len(res.Endpoints))
	}
	ep := res.Endpoints[0]
	if ep.Method != "GET" || ep.Path != "/health" || ep.Summary != "健康检查" {
		t.Errorf("端点解析错误：%+v", ep)
	}
	if len(ep.Auth) != 1 || ep.Auth[0] != "apiKey" {
		t.Errorf("全局 security 应下推到操作：%+v", ep.Auth)
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"空内容":       "   ",
		"非规格文档":     `{"hello":"world"}`,
		"没有 paths":  `{"openapi":"3.0.0","info":{"title":"x"}}`,
		"路径不以斜杠开头":  `{"openapi":"3.0.0","paths":{"v1/x":{"get":{}}}}`,
		"没有可识别的操作":  `{"openapi":"3.0.0","paths":{"/x":{"parameters":[]}}}`,
		"YAML 语法错误": "openapi: 3.0.0\npaths: [this is: broken",
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s：应当报错但成功了", name)
		}
	}
}

func TestNormalizeExplicitEndpoints(t *testing.T) {
	got, err := Normalize([]model.Endpoint{
		{Method: "get", Path: "/b"},
		{Method: "POST", Path: "/a"},
	})
	if err != nil {
		t.Fatalf("规范化失败：%v", err)
	}
	if len(got) != 2 || got[0].Path != "/a" || got[1].Method != "GET" {
		t.Errorf("规范化/排序错误：%+v", got)
	}

	if _, err := Normalize([]model.Endpoint{{Method: "GET", Path: "nope"}}); err == nil {
		t.Error("路径必须以 / 开头")
	}
	if _, err := Normalize([]model.Endpoint{{Method: "FETCH", Path: "/x"}}); err == nil {
		t.Error("非法方法应被拒绝")
	}
	if _, err := Normalize([]model.Endpoint{{Path: "/x"}}); err == nil {
		t.Error("缺少 method 应被拒绝")
	}
	if _, err := Normalize([]model.Endpoint{
		{Method: "GET", Path: "/x"}, {Method: "GET", Path: "/x"},
	}); err == nil {
		t.Error("重复端点应被拒绝")
	}
}

func TestParseTruncatesLongSummary(t *testing.T) {
	long := strings.Repeat("很", 300)
	raw := `{"openapi":"3.0.0","paths":{"/x":{"get":{"summary":"` + long + `"}}}}`
	res, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got := len([]rune(res.Endpoints[0].Summary)); got > 161 {
		t.Errorf("摘要应被截断，实际 %d 个字符", got)
	}
	if !strings.HasSuffix(res.Endpoints[0].Summary, "…") {
		t.Errorf("截断后应有省略号：%q", res.Endpoints[0].Summary)
	}
}
