package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaulie/service-registry/internal/config"
	"github.com/kaulie/service-registry/internal/metrics"
	"github.com/kaulie/service-registry/internal/store"
)

const testAdminToken = "adm-token-for-test"

type testEnv struct {
	ts  *httptest.Server
	st  *store.Store
	cfg config.Config
}

// newEnv 起一个真实的 HTTP 服务（httptest）+ 真实的 SQLite（临时文件）。
func newEnv(t *testing.T, mutate func(*config.Config)) *testEnv {
	t.Helper()
	cfg := config.Config{
		HTTPAddr:         "127.0.0.1:0",
		DBPath:           filepath.Join(t.TempDir(), "test.db"),
		AdminToken:       testAdminToken,
		WriteAuthOpen:    false, // 测试基线取最严格：写接口必须带令牌
		DefaultNamespace: "default",
		PullDefaultLimit: 100,
		PullMaxLimit:     1000,
		PullWaitMax:      2 * time.Second,
		SSEKeepAlive:     100 * time.Millisecond,
		MaxSpecBytes:     256 * 1024,
		ShutdownTimeout:  2 * time.Second,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	if _, err := st.EnsureNamespace(t.Context(), cfg.DefaultNamespace, "默认"); err != nil {
		t.Fatalf("播种命名空间失败：%v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := NewServer(st, cfg, metrics.New(), log, "test-version")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = st.Close()
	})
	return &testEnv{ts: ts, st: st, cfg: cfg}
}

func (e *testEnv) do(t *testing.T, method, path string, body any, token string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败：%v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, reader)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Registry-Token", token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			out = map[string]any{"raw": string(raw)}
		}
	}
	return res.StatusCode, out
}

// ok 以 admin 令牌发起请求并要求 2xx。
func (e *testEnv) ok(t *testing.T, method, path string, body any) map[string]any {
	t.Helper()
	status, out := e.do(t, method, path, body, testAdminToken)
	if status < 200 || status >= 300 {
		t.Fatalf("%s %s 期望 2xx，实际 %d：%v", method, path, status, out)
	}
	return out
}

func (e *testEnv) expectStatus(t *testing.T, method, path, token string, body any, want int) map[string]any {
	t.Helper()
	status, out := e.do(t, method, path, body, token)
	if status != want {
		t.Fatalf("%s %s 期望 %d，实际 %d：%v", method, path, want, status, out)
	}
	return out
}
