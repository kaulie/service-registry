package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaulie/service-registry/internal/model"
)

func TestPublishOwnContractReplacesStubAndKeepsIdentity(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustNamespace(t, st, "default")
	raw := readOwnSpec(t)

	_, _, err := st.UpsertService(ctx, ServiceInput{
		Service: model.Service{
			Namespace: "default", Name: "service_registry", Version: "0.0.1", Owner: "kaulie",
			GitRepoURL:   "https://github.com/kaulie/service-registry",
			DepartmentID: "D0002", DepartmentName: "基础架构部",
			HealthPath: "/ready", MCPPath: "/mcp",
			API: model.ServiceAPI{Protocols: []string{"http"}},
		},
		SpecRaw:    []byte("openapi: 3.0.3\ninfo:\n  title: stub\n  version: 0.0.1\npaths:\n  /health:\n    get:\n      summary: 健康检查\n"),
		SpecFormat: "yaml",
		SpecHash:   "stub",
		Endpoints:  []model.Endpoint{{Method: "GET", Path: "/health", Summary: "健康检查"}},
	}, "anonymous")
	if err != nil {
		t.Fatalf("写入最小模板失败：%v", err)
	}
	if _, err := st.CreateInstance(ctx, model.Instance{
		Namespace: "default", Service: "service_registry", Scheme: "http",
		Host: "127.0.0.1", Port: 4240, OS: "mac",
	}, "anonymous"); err != nil {
		t.Fatalf("写入实例失败：%v", err)
	}

	first, err := st.PublishOwnContract(ctx, "default", "service_registry", raw, "self")
	if err != nil {
		t.Fatalf("刷新自述契约失败：%v", err)
	}
	if !first.Updated || first.Created || first.Endpoints < 20 {
		t.Fatalf("应替换端点且数量完整：%+v", first)
	}
	svc, err := st.GetService(ctx, "default", "service_registry")
	if err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	if svc.Owner != "kaulie" || svc.DepartmentID != "D0002" || svc.DepartmentName != "基础架构部" ||
		svc.GitRepoURL != "https://github.com/kaulie/service-registry" || svc.Version != "0.0.1" ||
		svc.HealthPath != "/ready" || svc.InstanceCount != 1 {
		t.Fatalf("身份或实例被改掉了：%+v", svc)
	}
	if len(svc.API.Endpoints) != first.Endpoints {
		t.Fatalf("端点数 %d，结果 %d", len(svc.API.Endpoints), first.Endpoints)
	}
	if !hasEndpoint(svc.API.Endpoints, "PUT", "/v1/namespaces/{ns}/services/{svc}/announce") {
		t.Fatal("自述契约里没有 announce")
	}
	rev := svc.Revision

	again, err := st.PublishOwnContract(ctx, "default", "service_registry", raw, "self")
	if err != nil {
		t.Fatalf("再次刷新失败：%v", err)
	}
	if again.Updated || again.Created {
		t.Fatalf("原文没变不应再写：%+v", again)
	}
	svc, err = st.GetService(ctx, "default", "service_registry")
	if err != nil {
		t.Fatalf("再读失败：%v", err)
	}
	if svc.Revision != rev {
		t.Fatalf("revision 被推进了：%d → %d", rev, svc.Revision)
	}
}

func TestPublishOwnContractCreatesWhenMissing(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustNamespace(t, st, "default")
	raw := readOwnSpec(t)

	got, err := st.PublishOwnContract(ctx, "default", "service_registry", raw, "self")
	if err != nil {
		t.Fatalf("新建失败：%v", err)
	}
	if !got.Created || got.Endpoints < 20 {
		t.Fatalf("应新建完整契约：%+v", got)
	}
	svc, err := st.GetService(ctx, "default", "service_registry")
	if err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	if svc.HealthPath != "/health" || svc.MCPPath != "/mcp" || len(svc.API.Endpoints) != got.Endpoints {
		t.Fatalf("新建契约字段不对：health=%q mcp=%q eps=%d", svc.HealthPath, svc.MCPPath, len(svc.API.Endpoints))
	}
}

func readOwnSpec(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("读取自述契约失败：%v", err)
	}
	return raw
}

func hasEndpoint(eps []model.Endpoint, method, path string) bool {
	for _, ep := range eps {
		if ep.Method == method && ep.Path == path {
			return true
		}
	}
	return false
}
