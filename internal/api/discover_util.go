package api

import (
	"math/rand/v2"
	"net/http"
	"strings"
)

// randIndex 返回 [0,n) 的随机下标。
func randIndex(n int) int {
	if n <= 1 {
		return 0
	}
	return rand.IntN(n)
}

// parseMetaFilters 解析 ?meta=key=value（可重复）过滤条件。
// 返回错误消息（空字符串表示没问题）。
func parseMetaFilters(r *http.Request) (map[string]string, string) {
	values, ok := r.URL.Query()["meta"]
	if !ok {
		return nil, ""
	}
	out := map[string]string{}
	for _, raw := range values {
		key, value, found := strings.Cut(raw, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			return nil, "meta 过滤条件格式应为 meta=key=value，收到：" + raw
		}
		out[key] = strings.TrimSpace(value)
	}
	return out, ""
}

// matchMeta 判断实例元信息是否满足全部过滤条件。
func matchMeta(metadata map[string]string, filters map[string]string) bool {
	for k, v := range filters {
		if metadata[k] != v {
			return false
		}
	}
	return true
}
