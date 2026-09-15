package idgen

import (
	"strings"
	"testing"
)

func TestNewIsPrefixedUniqueAndOrdered(t *testing.T) {
	const n = 5000
	ids := make([]string, 0, n)
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		id := New(PrefixInstance)
		if !strings.HasPrefix(id, PrefixInstance) {
			t.Fatalf("前缀丢失：%q", id)
		}
		if len(id) != len(PrefixInstance)+26 {
			t.Fatalf("ID 长度应为前缀 + 26 个 Base32 字符，实际 %d：%q", len(id), id)
		}
		if seen[id] {
			t.Fatalf("出现重复 ID：%q", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	// 同一毫秒内也必须单调递增：字典序即时间序（便于按 ID 排序/分页）。
	for i := 1; i < len(ids); i++ {
		if ids[i-1] >= ids[i] {
			t.Fatalf("ID 未保持递增：%q >= %q（第 %d 个）", ids[i-1], ids[i], i)
		}
	}
	// 字符集必须是 Crockford Base32（不含 I/L/O/U）。
	for _, id := range ids {
		for _, c := range strings.TrimPrefix(id, PrefixInstance) {
			if !strings.ContainsRune(crockford, c) {
				t.Fatalf("出现非法字符 %q：%q", c, id)
			}
		}
	}
}

func TestTokenIsRandomAndPrefixed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok := Token()
		if !strings.HasPrefix(tok, PrefixToken) {
			t.Fatalf("令牌前缀错误：%q", tok)
		}
		if len(tok) != len(PrefixToken)+48 {
			t.Fatalf("令牌应为 24 字节的十六进制（48 字符）：%q", tok)
		}
		if seen[tok] {
			t.Fatalf("令牌重复：%q", tok)
		}
		seen[tok] = true
	}
}
