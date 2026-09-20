package apispec

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 路由注册的两种写法（只在 internal/api/api.go 里出现；新增写法时这里要跟着改）：
//
//	add(http.MethodGet, "/v1/services", …)   —— 业务路由（JSON API，统一走 add 以拿到 405 兜底）
//	mux.HandleFunc("GET /metrics", …)        —— 指标端点（它带自己的 Content-Type，不走 add）
//
// 面板（/panel/，HTML UI）**不**算 JSON API，所以不在自述契约里、也不在这里比对。
var (
	addRouteRe = regexp.MustCompile(`add\(http\.Method([A-Za-z]+),\s*"([^"]+)"`)
	muxRouteRe = regexp.MustCompile(`mux\.HandleFunc\("(GET|POST|PUT|PATCH|DELETE) ([^"]+)"`)
)

// TestSelfContractCoversAllRoutes 是"路由 ↔ 自述契约"的**双向**一致性检查：
//
//	代码里注册的每个 JSON 接口都必须在 api/openapi.yaml 里（否则文档漏了）；
//	自述契约里的每个端点也必须有对应路由（否则文档过期了 —— 比"漏写"更坑，
//	因为消费方会照着一条不存在的接口去调）。
//
// 为什么要有它：apispec/selfcontract_test.go 只抽查了十来个已知端点，
// 新增接口忘了写进自述契约时它照样绿；这条检查把"忘了改文档"变成 CI 红灯。
func TestSelfContractCoversAllRoutes(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "api", "api.go"))
	if err != nil {
		t.Fatalf("读取 internal/api/api.go 失败：%v", err)
	}
	routes := map[string]bool{}
	for _, m := range addRouteRe.FindAllStringSubmatch(string(src), -1) {
		routes[strings.ToUpper(m[1])+" "+m[2]] = true
	}
	for _, m := range muxRouteRe.FindAllStringSubmatch(string(src), -1) {
		routes[m[1]+" "+m[2]] = true
	}
	// 正则在源码上没匹配到 = 检查本身失效（而不是"代码很干净"）→ 明确失败。
	// 阈值取 20：当前有 30 条左右，写法一改就会掉到 0，从而立刻被发现。
	if len(routes) < 20 {
		t.Fatalf("只从 api.go 里解析出 %d 条路由，检查本身可能已失效（正则要跟着路由写法更新）", len(routes))
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("读取 api/openapi.yaml 失败：%v", err)
	}
	res, err := Parse(raw)
	if err != nil {
		t.Fatalf("自述契约无法解析：%v", err)
	}
	documented := map[string]bool{}
	for _, ep := range res.Endpoints {
		documented[strings.ToUpper(ep.Method)+" "+ep.Path] = true
	}

	for _, r := range sortedNames(routes) {
		if !documented[r] {
			t.Errorf("接口 %s 没有写进自述契约 api/openapi.yaml", r)
		}
	}
	for _, d := range sortedNames(documented) {
		if !routes[d] {
			t.Errorf("自述契约里的 %s 在代码里没有对应路由（文档过期了）", d)
		}
	}
}

// sortedNames 返回 map 的键并按字典序排序（错误信息/断言顺序稳定，便于读）。
// （apispec.go 里的 sortedKeys 是给 map[string]any 用的，这里键是字符串，另起一个名字。）
func sortedNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
