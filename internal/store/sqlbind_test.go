package store

import "testing"

func TestRebindPlaceholders(t *testing.T) {
	if got := rebind(false, "SELECT * FROM t WHERE a = ?"); got != "SELECT * FROM t WHERE a = ?" {
		t.Fatalf("sqlite 不应改写：%s", got)
	}
	if got := rebind(true, "SELECT * FROM t WHERE a = ? AND b = ?"); got != "SELECT * FROM t WHERE a = $1 AND b = $2" {
		t.Fatalf("改写错误：%s", got)
	}
	if got := rebind(true, "SELECT '?' AS q, a FROM t WHERE a = ?"); got != "SELECT '?' AS q, a FROM t WHERE a = $1" {
		t.Fatalf("字面问号被改写了：%s", got)
	}
	if got := rebind(true, "SELECT * FROM t"); got != "SELECT * FROM t" {
		t.Fatalf("无占位符不应改写：%s", got)
	}
}
