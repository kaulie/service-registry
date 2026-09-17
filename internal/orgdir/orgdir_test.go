package orgdir

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const goodPayload = `{"items":[
  {"id":"D0001","name":"SRE部门","type":"研发","createdAt":"2026-09-17T05:05:36Z"},
  {"id":"D0002","name":"工程效能部门","type":"研发"}
],"types":["研发","测试","产品","管理"]}`

// TestSnapshotFetchAndCache 覆盖：正常取回、TTL 内不出网（只打一次）、force 强制刷新。
func TestSnapshotFetchAndCache(t *testing.T) {
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != DefaultPath {
			t.Errorf("应请求 %s，实际 %s", DefaultPath, r.URL.Path)
		}
		hits++
		_, _ = w.Write([]byte(goodPayload))
	}))
	defer ts.Close()

	c := NewClient(ts.URL+"/", time.Second, time.Minute) // 末尾斜杠也应被规整掉
	snap := c.Snapshot(context.Background(), false)
	if !snap.Fresh() || snap.Enabled != true || snap.Cached || snap.Stale {
		t.Fatalf("首次应是新鲜数据：%+v", snap)
	}
	if len(snap.Departments) != 2 || snap.Departments[0].Label() != "SRE部门（D0001）" {
		t.Fatalf("部门解析不对：%+v", snap.Departments)
	}
	if snap.Source != Source || snap.URL != ts.URL {
		t.Fatalf("来源信息应带出来：source=%q url=%q", snap.Source, snap.URL)
	}

	cached := c.Snapshot(context.Background(), false)
	if !cached.Cached || !cached.Available {
		t.Fatalf("TTL 内应命中缓存：%+v", cached)
	}
	if hits != 1 {
		t.Fatalf("TTL 内不应重复出网，实际请求 %d 次", hits)
	}

	forced := c.Snapshot(context.Background(), true)
	if forced.Cached || hits != 2 {
		t.Fatalf("refresh 应强制出网：cached=%v hits=%d", forced.Cached, hits)
	}
}

// TestSnapshotDegradeToLastGood 覆盖：组织接口挂掉时用上次成功的快照并标 stale。
func TestSnapshotDegradeToLastGood(t *testing.T) {
	broken := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
			return
		}
		_, _ = w.Write([]byte(goodPayload))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, time.Second, 0) // ttl=0：每次都出网
	if snap := c.Snapshot(context.Background(), false); !snap.Fresh() {
		t.Fatalf("第一次应成功：%+v", snap)
	}

	broken = true
	snap := c.Snapshot(context.Background(), false)
	if snap.Available || !snap.Stale || !snap.Cached {
		t.Fatalf("组织接口不可达时应降级为 stale：%+v", snap)
	}
	if len(snap.Departments) != 2 {
		t.Fatalf("降级时仍应带上上次成功的数据：%+v", snap.Departments)
	}
	if !strings.Contains(snap.Error, "HTTP 500") || !strings.Contains(snap.Error, "boom") {
		t.Fatalf("错误信息应说明原因：%q", snap.Error)
	}
	if snap.FetchedAt.IsZero() {
		t.Fatalf("降级时 FetchedAt 应保留上次成功的时间，实际 %v", snap.FetchedAt)
	}
}

// TestSnapshotUnreachableWithoutCache 覆盖：从没成功过 + 连不上 → 明确报错、不 panic。
func TestSnapshotUnreachableWithoutCache(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := ts.URL
	ts.Close() // 关掉，保证连不上

	c := NewClient(url, 500*time.Millisecond, 0)
	snap := c.Snapshot(context.Background(), false)
	if snap.Available || snap.Stale || !snap.Enabled {
		t.Fatalf("应是「不可达且无缓存」：%+v", snap)
	}
	if snap.Error == "" || len(snap.Departments) != 0 {
		t.Fatalf("应给出失败原因且部门列表为空：%+v", snap)
	}
}

// TestSnapshotBadPayload 覆盖：非 JSON / 非 200 都要变成可读的 error，而不是 panic。
func TestSnapshotBadPayload(t *testing.T) {
	payload := "<html>not json</html>"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer ts.Close()

	snap := NewClient(ts.URL, time.Second, 0).Snapshot(context.Background(), false)
	if snap.Available || !strings.Contains(snap.Error, "无法解析") {
		t.Fatalf("非 JSON 应被拦住：%+v", snap)
	}

	// 条目缺 ID/名称的会被丢掉，不影响其余条目。
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"","name":""},{"id":" D0009 ","name":" 测试部 "}]}`))
	}))
	defer ts2.Close()
	snap2 := NewClient(ts2.URL, time.Second, 0).Snapshot(context.Background(), false)
	if len(snap2.Departments) != 1 || snap2.Departments[0].ID != "D0009" || snap2.Departments[0].Name != "测试部" {
		t.Fatalf("空条目应被丢弃、值应去空白：%+v", snap2.Departments)
	}
}

// TestDisabledClient 覆盖：没配组织接口时的行为（面板/写契约都依赖它）。
func TestDisabledClient(t *testing.T) {
	c := NewClient("  ", time.Second, time.Minute)
	if c.Enabled() {
		t.Fatal("空白地址应视为未配置")
	}
	snap := c.Snapshot(context.Background(), false)
	if snap.Enabled || snap.Available || !strings.Contains(snap.Error, "未配置") {
		t.Fatalf("未配置时应明确说明：%+v", snap)
	}
	if snap.Departments == nil {
		t.Fatal("Departments 应为空数组而不是 null（JSON 里 [] 更好用）")
	}
}

// TestFind 覆盖按 ID / 按名称（大小写、空白不敏感）的命中优先级。
func TestFind(t *testing.T) {
	snap := Snapshot{Departments: []Department{
		{ID: "D0001", Name: "SRE部门"},
		{ID: "D0002", Name: "工程效能部门"},
	}}

	if d, ok := Find(snap, "d0001", ""); !ok || d.Name != "SRE部门" {
		t.Fatalf("按 ID 应忽略大小写命中：%v %v", d, ok)
	}
	if d, ok := Find(snap, "", " 工程效能部门 "); !ok || d.ID != "D0002" {
		t.Fatalf("按名称应忽略首尾空白命中：%v %v", d, ok)
	}
	// ID 优先：即使名称命中了另一个部门，也以 ID 为准。
	if d, ok := Find(snap, "D0001", "工程效能部门"); !ok || d.ID != "D0001" {
		t.Fatalf("ID 应优先：%v %v", d, ok)
	}
	if _, ok := Find(snap, "D9999", "不存在"); ok {
		t.Fatal("不存在的部门不应命中")
	}
}

func TestSnippet(t *testing.T) {
	if got := snippet(nil); got != "(空响应)" {
		t.Fatalf("空响应应可读：%q", got)
	}
	long := strings.Repeat("a", 300)
	if got := snippet([]byte(long)); len(got) > 170 || !strings.HasSuffix(got, "…") {
		t.Fatalf("超长响应应截断：len=%d %q", len(got), got)
	}
}
