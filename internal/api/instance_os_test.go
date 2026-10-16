package api

import (
	"net/http"
	"strings"
	"testing"
)

// TestInstanceMachineOSRequired 校验登记实例时必须带机器 os，且只接受 linux/mac/windows。
func TestInstanceMachineOSRequired(t *testing.T) {
	e := newEnv(t, nil)
	seedService(t, e)
	base := "/v1/namespaces/team-a/services/event-center/instances"

	missing := e.expectStatus(t, http.MethodPost, base, testAdminToken,
		map[string]any{"scheme": "http", "host": "10.0.0.1", "port": 80}, http.StatusBadRequest)
	if msg := errMessage(missing); !strings.Contains(msg, "linux/mac/windows") {
		t.Fatalf("缺 os 的提示应点明取值范围：%v", missing)
	}
	bad := e.expectStatus(t, http.MethodPost, base, testAdminToken,
		map[string]any{"scheme": "http", "host": "10.0.0.1", "port": 80, "os": "Android"}, http.StatusBadRequest)
	if msg := errMessage(bad); !strings.Contains(msg, "linux/mac/windows") {
		t.Fatalf("非法 os 的提示应点明取值范围：%v", bad)
	}

	created := e.ok(t, http.MethodPost, base, map[string]any{
		"scheme": "http", "host": "10.0.0.1", "port": 80, "os": "Linux",
	})
	inst := created["instance"].(map[string]any)
	if inst["os"] != "linux" {
		t.Fatalf("os 应收敛成小写 linux：%v", inst)
	}
	e.ok(t, http.MethodPost, base, map[string]any{
		"scheme": "http", "host": "10.0.0.2", "port": 80, "os": "mac",
	})
	win := e.ok(t, http.MethodPost, base, map[string]any{
		"scheme": "http", "host": "10.0.0.3", "port": 80, "os": "Windows",
	})
	if win["instance"].(map[string]any)["os"] != "windows" {
		t.Fatalf("Windows 应收敛成 windows：%v", win)
	}

	// 声明式同步里每一条都要有 os；缺了要指到下标，不能静默丢掉。
	syncBad := e.expectStatus(t, http.MethodPut, base, testAdminToken, map[string]any{
		"instances": []map[string]any{
			{"scheme": "http", "host": "10.0.0.1", "port": 80},
		},
	}, http.StatusBadRequest)
	if msg := errMessage(syncBad); !strings.Contains(msg, "instances[0]") || !strings.Contains(msg, "linux/mac/windows") {
		t.Fatalf("同步缺 os 应指出下标和取值：%v", syncBad)
	}

	// 只改 os 也是一次更新，并且要落库。
	synced := e.ok(t, http.MethodPut, base, map[string]any{
		"instances": []map[string]any{
			{"scheme": "http", "host": "10.0.0.1", "port": 80, "os": "mac"},
		},
	})
	if synced["updated"].(float64) != 1 || synced["deleted"].(float64) != 2 {
		t.Fatalf("只改 os 应记作更新并摘除其余实例：%v", synced)
	}
	listed := e.ok(t, http.MethodGet, base, nil)
	got := listed["instances"].([]any)
	if len(got) != 1 || got[0].(map[string]any)["os"] != "mac" {
		t.Fatalf("同步后的 os 不正确：%v", listed)
	}

	id := got[0].(map[string]any)["id"].(string)
	patched := e.ok(t, http.MethodPatch, base+"/"+id, map[string]any{"os": "windows"})
	if patched["instance"].(map[string]any)["os"] != "windows" {
		t.Fatalf("PATCH os 未落库：%v", patched)
	}
	e.expectStatus(t, http.MethodPatch, base+"/"+id, testAdminToken,
		map[string]any{"os": "ios"}, http.StatusBadRequest)
}

func errMessage(out map[string]any) string {
	errObj, _ := out["error"].(map[string]any)
	msg, _ := errObj["message"].(string)
	return msg
}
