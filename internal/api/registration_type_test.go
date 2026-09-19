package api

import (
	"net/http"
	"strings"
	"testing"
)

// TestRegistrationTypeDefaultsToService 校验 type 缺省为 service，
// 且 service 类型不会落 appId/os。
func TestRegistrationTypeDefaultsToService(t *testing.T) {
	e := newEnv(t, nil)
	e.ok(t, http.MethodPost, "/v1/namespaces", map[string]any{"name": "team-a"})
	path := "/v1/namespaces/team-a/services/event-center"

	body := serviceBody(testSpec)
	body["appId"] = "ignored-app"
	body["os"] = "iOS"
	e.ok(t, http.MethodPut, path, body)

	svc := e.ok(t, http.MethodGet, path, nil)["service"].(map[string]any)
	if svc["type"] != "service" {
		t.Fatalf("缺省 type 应为 service，实际 %v", svc["type"])
	}
	if v, ok := svc["appId"]; ok && v != "" {
		t.Fatalf("service 类型不应保存 appId，实际 %v", v)
	}
	if v, ok := svc["os"]; ok && v != "" {
		t.Fatalf("service 类型不应保存 os，实际 %v", v)
	}
}

// TestAppRegistrationRoundTrip 校验 app 类型：appId 必填、os 必选并可读回。
func TestAppRegistrationRoundTrip(t *testing.T) {
	e := newEnv(t, nil)
	e.ok(t, http.MethodPost, "/v1/namespaces", map[string]any{"name": "team-a"})
	path := "/v1/namespaces/team-a/services/mobile-app"

	body := serviceBody(testSpec)
	body["type"] = "app"
	body["appId"] = "com.example.mobile"
	body["os"] = "iOS"
	e.ok(t, http.MethodPut, path, body)

	svc := e.ok(t, http.MethodGet, path, nil)["service"].(map[string]any)
	if svc["type"] != "app" || svc["appId"] != "com.example.mobile" || svc["os"] != "iOS" {
		t.Fatalf("app 登记结果错误：%v", svc)
	}

	// 大小写/空白应收敛到统一展示值。
	body["os"] = "  macos "
	e.ok(t, http.MethodPut, path, body)
	svc = e.ok(t, http.MethodGet, path, nil)["service"].(map[string]any)
	if svc["os"] != "MacOS" {
		t.Fatalf("os 应归一化为 MacOS，实际 %v", svc["os"])
	}
}

// TestAppRegistrationValidation 校验 app 类型缺少 appId/os、非法 type/os 时返回 400。
func TestAppRegistrationValidation(t *testing.T) {
	e := newEnv(t, nil)
	e.ok(t, http.MethodPost, "/v1/namespaces", map[string]any{"name": "team-a"})
	path := "/v1/namespaces/team-a/services/mobile-app"

	with := func(mut func(map[string]any)) map[string]any {
		body := serviceBody(testSpec)
		body["type"] = "app"
		mut(body)
		return body
	}

	out := e.expectStatus(t, http.MethodPut, path, testAdminToken, with(func(b map[string]any) {
		b["os"] = "iOS"
	}), http.StatusBadRequest)
	if msg := errMsg(out); !strings.Contains(msg, "appId") {
		t.Fatalf("缺少 appId 的错误应点名 appId，实际：%s", msg)
	}

	out = e.expectStatus(t, http.MethodPut, path, testAdminToken, with(func(b map[string]any) {
		b["appId"] = "com.example.mobile"
	}), http.StatusBadRequest)
	if msg := errMsg(out); !strings.Contains(msg, "os") {
		t.Fatalf("缺少 os 的错误应点名 os，实际：%s", msg)
	}

	out = e.expectStatus(t, http.MethodPut, path, testAdminToken, with(func(b map[string]any) {
		b["appId"] = "com.example.mobile"
		b["os"] = "HarmonyOS"
	}), http.StatusBadRequest)
	if msg := errMsg(out); !strings.Contains(msg, "os") {
		t.Fatalf("非法 os 的错误应点名 os，实际：%s", msg)
	}

	badType := serviceBody(testSpec)
	badType["type"] = "container"
	out = e.expectStatus(t, http.MethodPut, path, testAdminToken, badType, http.StatusBadRequest)
	if msg := errMsg(out); !strings.Contains(msg, "type") {
		t.Fatalf("非法 type 的错误应点名 type，实际：%s", msg)
	}
}

// TestAppRegistrationAcceptsAppIDSnakeCase 兼容题面里的 app_id 字段名。
func TestAppRegistrationAcceptsAppIDSnakeCase(t *testing.T) {
	e := newEnv(t, nil)
	e.ok(t, http.MethodPost, "/v1/namespaces", map[string]any{"name": "team-a"})
	path := "/v1/namespaces/team-a/services/mobile-app"

	body := serviceBody(testSpec)
	body["type"] = "app"
	body["app_id"] = "com.example.snake"
	body["os"] = "Android"
	e.ok(t, http.MethodPut, path, body)

	svc := e.ok(t, http.MethodGet, path, nil)["service"].(map[string]any)
	if svc["appId"] != "com.example.snake" || svc["os"] != "Android" {
		t.Fatalf("app_id 别名登记结果错误：%v", svc)
	}
}

func errMsg(out map[string]any) string {
	if errObj, _ := out["error"].(map[string]any); errObj != nil {
		if msg, _ := errObj["message"].(string); msg != "" {
			return msg
		}
	}
	return ""
}
