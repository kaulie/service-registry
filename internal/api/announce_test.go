package api

import (
	"net/http"
	"testing"
	"time"
)

func TestAnnounceInstance(t *testing.T) {
	e := newEnv(t, nil)
	seedService(t, e)

	status, first := e.do(t, http.MethodPut, "/v1/namespaces/team-a/services/event-center/announce", map[string]any{
		"port": 9099,
		"os":   "linux",
	}, testAdminToken)
	if status != http.StatusCreated {
		t.Fatalf("首次上报应为 201，实际 %d：%v", status, first)
	}
	if first["created"] != true {
		t.Fatalf("created 应为 true：%v", first)
	}
	inst := first["instance"].(map[string]any)
	if inst["port"].(float64) != 9099 || inst["host"] != "127.0.0.1" || inst["scheme"] != "http" || inst["os"] != "linux" {
		t.Fatalf("上报结果不对（host 应取对端 127.0.0.1）：%v", inst)
	}
	id, _ := inst["id"].(string)
	firstAnnounced, _ := inst["lastAnnouncedAt"].(string)
	if firstAnnounced == "" {
		t.Fatalf("首次上报应带 lastAnnouncedAt：%v", inst)
	}
	time.Sleep(20 * time.Millisecond)

	status, again := e.do(t, http.MethodPut, "/v1/namespaces/team-a/services/event-center/announce", map[string]any{
		"port": 9099,
		"os":   "Linux",
	}, testAdminToken)
	if status != http.StatusOK || again["created"] != false {
		t.Fatalf("同一地址再报应为 200 且 created=false，实际 %d：%v", status, again)
	}
	againInst := again["instance"].(map[string]any)
	if againInst["id"] != id {
		t.Fatalf("再报应仍是同一条实例：%v", again)
	}
	if againInst["updatedAt"] != inst["updatedAt"] {
		t.Fatalf("内容没变时 updatedAt 应保持不动：%v → %v", inst["updatedAt"], againInst["updatedAt"])
	}
	secondAnnounced, _ := againInst["lastAnnouncedAt"].(string)
	if secondAnnounced == "" || secondAnnounced == firstAnnounced {
		t.Fatalf("再报应刷新 lastAnnouncedAt：%q → %q", firstAnnounced, secondAnnounced)
	}
	audit := e.ok(t, http.MethodGet, "/v1/audit?namespace=team-a&entity=instance", nil)
	if n := len(audit["entries"].([]any)); n != 1 {
		t.Fatalf("内容没变不应再写审计，实际 %d 条：%v", n, audit["entries"])
	}

	e.ok(t, http.MethodPut, "/v1/namespaces/team-a/services/event-center/announce", map[string]any{
		"host": "10.0.0.8",
		"port": 9099,
		"os":   "mac",
		"arch": "arm64",
	})
	list := e.ok(t, http.MethodGet, "/v1/namespaces/team-a/services/event-center/instances", nil)
	if list["total"].(float64) != 2 {
		t.Fatalf("另一台机器的上报不应摘掉原来的实例：%v", list)
	}

	e.expectStatus(t, http.MethodPut, "/v1/namespaces/team-a/services/event-center/announce", testAdminToken,
		map[string]any{"os": "linux"}, http.StatusBadRequest)
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/team-a/services/missing/announce", testAdminToken,
		map[string]any{"port": 1, "os": "linux"}, http.StatusNotFound)
	e.expectStatus(t, http.MethodPut, "/v1/namespaces/team-a/services/event-center/announce", "",
		map[string]any{"port": 1, "os": "linux"}, http.StatusUnauthorized)
}
