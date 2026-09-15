package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSnapshotETagAndChangesCursor(t *testing.T) {
	e := newEnv(t, nil)
	seedService(t, e)

	// 快照：全量 + ETag。
	req, _ := http.NewRequest(http.MethodGet, e.ts.URL+"/v1/snapshot", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求快照失败：%v", err)
	}
	etag := res.Header.Get("ETag")
	var snap map[string]any
	_ = json.NewDecoder(res.Body).Decode(&snap)
	_ = res.Body.Close()
	if etag == "" || !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("快照应带 ETag，实际 %q", etag)
	}
	if len(snap["services"].([]any)) != 1 {
		t.Fatalf("快照内容错误：%v", snap)
	}
	// 快照默认含全部命名空间（测试环境里有播种的 default 与 team-a）。
	if len(snap["namespaces"].([]any)) != 2 {
		t.Fatalf("快照应包含全部命名空间：%v", snap["namespaces"])
	}
	// 按命名空间过滤的快照只包含该命名空间。
	scoped := e.ok(t, http.MethodGet, "/v1/snapshot?namespace=team-a", nil)
	if len(scoped["namespaces"].([]any)) != 1 || len(scoped["services"].([]any)) != 1 {
		t.Fatalf("按命名空间过滤快照错误：%v", scoped)
	}

	// 带 If-None-Match → 304（省掉整个响应体）。
	req2, _ := http.NewRequest(http.MethodGet, e.ts.URL+"/v1/snapshot", nil)
	req2.Header.Set("If-None-Match", etag)
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("请求快照失败：%v", err)
	}
	_ = res2.Body.Close()
	if res2.StatusCode != http.StatusNotModified {
		t.Fatalf("ETag 未命中应 304，实际 %d", res2.StatusCode)
	}

	// 增量：since=0 能看到全部变更；游标推进且无变更时应 waited=true。
	all := e.ok(t, http.MethodGet, "/v1/changes?since=0", nil)
	changes := all["changes"].([]any)
	if len(changes) != 2 { // 创建命名空间 + 登记服务
		t.Fatalf("变更条数错误：%v", changes)
	}
	rev := int(all["revision"].(float64))
	if rev < 2 {
		t.Fatalf("revision 应为变更条数：%d", rev)
	}
	none := e.ok(t, http.MethodGet, fmt.Sprintf("/v1/changes?since=%d&wait=100ms", rev), nil)
	if len(none["changes"].([]any)) != 0 {
		t.Fatalf("游标已到顶时应无变更：%v", none)
	}
	if none["waited"] != true {
		t.Fatalf("带 wait 的请求应标记 waited=true：%v", none)
	}

	// long-poll：挂起期间有写入 → 立即被唤醒返回该变更。
	done := make(chan map[string]any, 1)
	go func() {
		done <- e.ok(t, http.MethodGet, fmt.Sprintf("/v1/changes?since=%d&wait=2s", rev), nil)
	}()
	time.Sleep(150 * time.Millisecond)
	e.ok(t, http.MethodPut, "/v1/namespaces/team-a/services/event-center", serviceBody(""))

	select {
	case out := <-done:
		got := out["changes"].([]any)
		if len(got) == 0 {
			t.Fatalf("long-poll 应被新变更唤醒：%v", out)
		}
		if actor := got[0].(map[string]any)["actor"]; actor != "admin" {
			t.Fatalf("审计应记录操作者，实际 %v", actor)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("long-poll 超时未返回")
	}

	// 审计可按实体过滤。
	audit := e.ok(t, http.MethodGet, "/v1/audit?entity=service", nil)
	entries := audit["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("审计条目错误：%v", entries)
	}
	for _, raw := range entries {
		if raw.(map[string]any)["entity"] != "service" {
			t.Fatalf("审计过滤失败：%v", raw)
		}
	}
}

func TestSSEStream(t *testing.T) {
	e := newEnv(t, nil)
	seedService(t, e)

	req, _ := http.NewRequest(http.MethodGet, e.ts.URL+"/v1/events", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("连接 SSE 失败：%v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("SSE 内容类型错误：%q", ct)
	}
	reader := bufio.NewReader(res.Body)

	// 首帧是 hello（带当前游标），客户端据此对齐。
	event, data := readSSE(t, reader)
	if event != "hello" {
		t.Fatalf("首帧应为 hello，实际 %q（data=%s）", event, data)
	}
	var hello map[string]any
	if err := json.Unmarshal([]byte(data), &hello); err != nil {
		t.Fatalf("hello 帧不是 JSON：%s", data)
	}

	// 触发一次写入 → 应收到对应的 change 帧。
	e.ok(t, http.MethodPut, "/v1/namespaces/team-a/services/event-center", serviceBody(""))
	event, data = readSSE(t, reader)
	if event != "change" {
		t.Fatalf("应收到 change 帧，实际 %q（data=%s）", event, data)
	}
	var change map[string]any
	if err := json.Unmarshal([]byte(data), &change); err != nil {
		t.Fatalf("change 帧不是 JSON：%s", data)
	}
	if change["entity"] != "service" || change["ref"] != "team-a/event-center" {
		t.Fatalf("change 帧内容错误：%v", change)
	}
}

// readSSE 读取一个 "event: x\ndata: {...}\n\n" 帧（跳过保活注释）。
func readSSE(t *testing.T, r *bufio.Reader) (string, string) {
	t.Helper()
	var event, data string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("读取 SSE 失败：%v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if data != "" {
				return event, data
			}
		case strings.HasPrefix(line, ":"):
			// 保活注释，忽略。
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	t.Fatal("等待 SSE 帧超时")
	return "", ""
}
