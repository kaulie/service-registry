package mcpagg

import (
	"testing"

	"github.com/kaulie/service-registry/internal/model"
)

func TestComposeBuildsMCPURLs(t *testing.T) {
	services := []model.Service{
		{Namespace: "team-a", Name: "deployment", Description: "部署", MCPPath: ""},
		{Namespace: "team-a", Name: "billing", MCPPath: "/agents/mcp"},
		{Namespace: "team-b", Name: "queue", MCPPath: "/mcp"},
	}
	instances := []model.Instance{
		{ID: "i1", Namespace: "team-a", Service: "deployment", Scheme: "http", Host: "10.0.0.2", Port: 4220},
		{ID: "i2", Namespace: "team-a", Service: "deployment", Scheme: "tcp", Host: "10.0.0.2", Port: 4221},
		{ID: "i3", Namespace: "team-a", Service: "billing", Scheme: "https", Host: "10.0.0.3", Port: 443},
	}
	got := Compose(services, instances)
	if len(got) != 3 {
		t.Fatalf("目录应有 3 个服务，实际 %d", len(got))
	}
	dep := got[0]
	if dep.Service != "billing" || dep.MCPPath != "/agents/mcp" || len(dep.Endpoints) != 1 {
		t.Fatalf("billing 目录不对：%+v", dep)
	}
	if dep.Endpoints[0].URL != "https://10.0.0.3:443/agents/mcp" {
		t.Fatalf("billing URL = %s", dep.Endpoints[0].URL)
	}
	deploy := got[1]
	if deploy.Service != "deployment" || deploy.MCPPath != "/mcp" || len(deploy.Endpoints) != 1 {
		t.Fatalf("没写 mcpPath 时应收成 /mcp，且丢掉 tcp 实例：%+v", deploy)
	}
	if deploy.Endpoints[0].URL != "http://10.0.0.2:4220/mcp" {
		t.Fatalf("deployment URL = %s", deploy.Endpoints[0].URL)
	}
	if got[2].Service != "queue" || len(got[2].Endpoints) != 0 {
		t.Fatalf("没有实例的服务仍应出现，endpoints 为空：%+v", got[2])
	}
}

func TestToolNameAndSelf(t *testing.T) {
	name, ok := ToolName("team-a", "deployment", "deployment_release")
	if !ok || name != "team-a__deployment__deployment_release" {
		t.Fatalf("工具名 = %q ok=%v", name, ok)
	}
	if _, ok := ToolName("team-a", "deployment", ""); ok {
		t.Fatal("空工具名不应采用")
	}
	if !IsSelf("127.0.0.1:4240", "http://127.0.0.1:4240/mcp") {
		t.Fatal("聚合入口自己的地址应被认出来")
	}
	if IsSelf("127.0.0.1:4240", "http://127.0.0.1:4220/mcp") {
		t.Fatal("别的服务的 /mcp 不是自己")
	}
}
