package api

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestGitRepoURLRoundTrip 覆盖 gitRepoUrl 的完整读写链路：
// 登记 → 详情 → 列表 → 关键字检索 → 快照 → API 反查，以及"编辑时清空"。
func TestGitRepoURLRoundTrip(t *testing.T) {
	e := newEnv(t, nil)
	e.ok(t, http.MethodPost, "/v1/namespaces", map[string]any{"name": "team-a"})

	const repo = "https://github.com/kaulie/event-center.git"
	body := serviceBody(testSpec)
	body["gitRepoUrl"] = repo
	e.ok(t, http.MethodPut, "/v1/namespaces/team-a/services/event-center", body)

	// 详情里能读回来。
	svc := e.ok(t, http.MethodGet, "/v1/namespaces/team-a/services/event-center", nil)["service"].(map[string]any)
	if svc["gitRepoUrl"] != repo {
		t.Fatalf("契约详情里的 gitRepoUrl 应为 %q，实际 %v", repo, svc["gitRepoUrl"])
	}

	// 列表里也能看到。
	list := e.ok(t, http.MethodGet, "/v1/services?namespace=team-a", nil)["services"].([]any)
	if got := list[0].(map[string]any)["gitRepoUrl"]; got != repo {
		t.Fatalf("服务列表里的 gitRepoUrl 应为 %q，实际 %v", repo, got)
	}

	// ?q= 也能按仓库地址搜到（服务端的 LIKE 匹配包含 git_repo_url）。
	hits := e.ok(t, http.MethodGet, "/v1/services?q="+url.QueryEscape("github.com/kaulie"), nil)["services"].([]any)
	if len(hits) != 1 {
		t.Fatalf("按仓库地址检索应命中 1 个服务，实际 %d 个", len(hits))
	}

	// 快照（给其他平台拉取用）也要带上。
	snap := e.ok(t, http.MethodGet, "/v1/snapshot", nil)["services"].([]any)
	if got := snap[0].(map[string]any)["gitRepoUrl"]; got != repo {
		t.Fatalf("快照里的 gitRepoUrl 应为 %q，实际 %v", repo, got)
	}

	// "这个接口谁提供"的命中里一并给出仓库地址，找源码不用再查一次。
	match := e.ok(t, http.MethodGet, "/v1/search/apis?method=GET&path=/health", nil)["matches"].([]any)[0].(map[string]any)
	if got := match["gitRepoUrl"]; got != repo {
		t.Fatalf("API 反查结果里的 gitRepoUrl 应为 %q，实际 %v", repo, got)
	}

	// 编辑时不带 gitRepoUrl（面板里清空）→ 字段被清掉（PUT 是整份覆盖）。
	e.ok(t, http.MethodPut, "/v1/namespaces/team-a/services/event-center", serviceBody(testSpec))
	svc = e.ok(t, http.MethodGet, "/v1/namespaces/team-a/services/event-center", nil)["service"].(map[string]any)
	if v, ok := svc["gitRepoUrl"]; ok && v != "" {
		t.Fatalf("清空后不应再有 gitRepoUrl，实际 %v", v)
	}
}

// TestGitRepoURLValidation 校验 gitRepoUrl 的形状把关（可留空；只认"仓库地址"的样子）。
func TestGitRepoURLValidation(t *testing.T) {
	e := newEnv(t, nil)
	e.ok(t, http.MethodPost, "/v1/namespaces", map[string]any{"name": "team-a"})
	path := "/v1/namespaces/team-a/services/event-center"

	withRepo := func(v string) map[string]any {
		body := serviceBody(testSpec)
		body["gitRepoUrl"] = v
		return body
	}

	good := []string{
		"https://github.com/kaulie/event-center.git",
		"https://github.com/kaulie/event-center", // 不加 .git 也常见
		"http://git.internal.corp/team/repo.git",
		"ssh://git@git.internal:2222/team/repo.git",
		"git@github.com:kaulie/event-center.git", // scp 风格：git remote -v 直接抄
		"file:///Users/gaolei/code/event-center",
	}
	for _, v := range good {
		// 第一次是 201（新建），之后是 200（更新），这里只要求 2xx。
		e.ok(t, http.MethodPut, path, withRepo(v))
	}

	bad := []string{
		"github.com/kaulie/event-center", // 没有协议头，也不是 scp 风格
		"/Users/gaolei/code/event-center",
		"event-center",
		"https://github.com/kaulie/event center.git", // 含空格
		strings.Repeat("https://github.com/", 40),    // 超长
	}
	for _, v := range bad {
		out := e.expectStatus(t, http.MethodPut, path, testAdminToken, withRepo(v), http.StatusBadRequest)
		msg := out["error"].(map[string]any)["message"].(string)
		if !strings.Contains(msg, "gitRepoUrl") {
			t.Fatalf("错误信息应点名 gitRepoUrl，实际：%s", msg)
		}
	}

	// 前后空白自动去掉（运维从别处复制粘贴很容易带上）。
	e.ok(t, http.MethodPut, path, withRepo("  git@github.com:kaulie/event-center.git  "))
	svc := e.ok(t, http.MethodGet, path, nil)["service"].(map[string]any)
	if svc["gitRepoUrl"] != "git@github.com:kaulie/event-center.git" {
		t.Fatalf("应去掉两端空白，实际 %v", svc["gitRepoUrl"])
	}

	// 留空 = 合法（这个字段是可选的）。
	e.expectStatus(t, http.MethodPut, path, testAdminToken, withRepo(""), http.StatusOK)
}
