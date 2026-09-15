package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/kaulie/service-registry/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func mustNamespace(t *testing.T, st *Store, name string) {
	t.Helper()
	if _, err := st.CreateNamespace(context.Background(), name, "测试用", "hash-"+name, "tester"); err != nil {
		t.Fatalf("创建命名空间失败：%v", err)
	}
}

func mustService(t *testing.T, st *Store, ns, name string, endpoints ...model.Endpoint) model.Service {
	t.Helper()
	in := ServiceInput{
		Service: model.Service{
			Namespace: ns, Name: name, Version: "1.0.0", Owner: "tester",
			Tags: []string{"demo"}, API: model.ServiceAPI{Protocols: []string{"http"}},
		},
		Endpoints: endpoints,
	}
	svc, _, err := st.UpsertService(context.Background(), in, "tester")
	if err != nil {
		t.Fatalf("登记服务失败：%v", err)
	}
	return svc
}

func TestNamespaceCRUDAndCascade(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustNamespace(t, st, "team-a")

	if _, err := st.CreateNamespace(ctx, "team-a", "", "", "tester"); !errors.Is(err, ErrConflict) {
		t.Fatalf("重复创建应冲突，实际 %v", err)
	}
	ns, hash, err := st.GetNamespace(ctx, "team-a")
	if err != nil || ns.Name != "team-a" || hash != "hash-team-a" || !ns.TokenSet {
		t.Fatalf("读取命名空间错误：%+v %q %v", ns, hash, err)
	}
	if _, _, err := st.GetNamespace(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的命名空间应 ErrNotFound，实际 %v", err)
	}

	mustService(t, st, "team-a", "svc", model.Endpoint{Method: "GET", Path: "/x"})
	if _, err := st.CreateInstance(ctx, model.Instance{
		Namespace: "team-a", Service: "svc", Scheme: "http", Host: "127.0.0.1", Port: 8080,
	}, "tester"); err != nil {
		t.Fatalf("创建实例失败：%v", err)
	}
	if _, err := st.UpdateNamespace(ctx, "team-a", ptr("新描述"), "tester"); err != nil {
		t.Fatalf("更新描述失败：%v", err)
	}
	list, err := st.ListNamespaces(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("命名空间列表错误：%+v %v", list, err)
	}
	if list[0].ServiceCount != 1 || list[0].InstanceCount != 1 || list[0].Description != "新描述" {
		t.Fatalf("计数/描述错误：%+v", list[0])
	}

	if err := st.DeleteNamespace(ctx, "team-a", "tester"); err != nil {
		t.Fatalf("删除命名空间失败：%v", err)
	}
	counts, err := st.Counts(ctx)
	if err != nil {
		t.Fatalf("统计失败：%v", err)
	}
	if counts.Services != 0 || counts.Instances != 0 || counts.Endpoints != 0 || counts.Namespaces != 0 {
		t.Fatalf("级联删除不彻底：%+v", counts)
	}
}

func TestServiceUpsertReplacesEndpoints(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustNamespace(t, st, "ns")

	svc := mustService(t, st, "ns", "svc",
		model.Endpoint{Method: "GET", Path: "/a"},
		model.Endpoint{Method: "POST", Path: "/b"})
	if svc.Revision == 0 || len(svc.API.Endpoints) != 2 || svc.InstanceCount != 0 {
		t.Fatalf("首次登记结果错误：%+v", svc)
	}
	firstRev := svc.Revision

	// 再次登记：端点索引应被整体替换（旧端点不残留）。
	in := ServiceInput{
		Service:   model.Service{Namespace: "ns", Name: "svc", Version: "2.0.0", API: model.ServiceAPI{Protocols: []string{"http"}}},
		Endpoints: []model.Endpoint{{Method: "GET", Path: "/c"}},
		SpecRaw:   []byte(`{"openapi":"3.0.0","paths":{"/c":{"get":{}}}}`),
		SpecHash:  "deadbeef", SpecFormat: "json",
	}
	svc2, created, err := st.UpsertService(ctx, in, "tester")
	if err != nil || created {
		t.Fatalf("二次登记应更新而非新建：created=%v err=%v", created, err)
	}
	if len(svc2.API.Endpoints) != 1 || svc2.API.Endpoints[0].Path != "/c" {
		t.Fatalf("端点未被替换：%+v", svc2.API.Endpoints)
	}
	if !svc2.API.HasSpec || svc2.API.SpecBytes == 0 || svc2.Revision <= firstRev {
		t.Fatalf("spec/revision 不正确：%+v", svc2)
	}
	raw, format, err := st.ServiceSpec(ctx, "ns", "svc")
	if err != nil || format != "json" || len(raw) == 0 {
		t.Fatalf("读取内联 spec 失败：%q %q %v", raw, format, err)
	}

	if err := st.DeleteService(ctx, "ns", "svc", "tester"); err != nil {
		t.Fatalf("删除服务失败：%v", err)
	}
	if _, err := st.GetService(ctx, "ns", "svc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后应 ErrNotFound，实际 %v", err)
	}
	if err := st.DeleteService(ctx, "ns", "svc", "tester"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除应 ErrNotFound，实际 %v", err)
	}
}

func TestServiceListFilters(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustNamespace(t, st, "ns")
	mustService(t, st, "ns", "alpha", model.Endpoint{Method: "GET", Path: "/a"})

	beta := ServiceInput{
		Service: model.Service{
			Namespace: "ns", Name: "beta", Owner: "other",
			Tags: []string{"events", "pubsub"}, Description: "事件中心",
			API: model.ServiceAPI{Protocols: []string{"grpc"}},
		},
		Endpoints: []model.Endpoint{{Method: "POST", Path: "/b"}},
	}
	if _, _, err := st.UpsertService(ctx, beta, "tester"); err != nil {
		t.Fatalf("登记 beta 失败：%v", err)
	}

	cases := []struct {
		name   string
		filter ServiceFilter
		want   int
	}{
		{"全部", ServiceFilter{}, 2},
		{"按标签", ServiceFilter{Tag: "pubsub"}, 1},
		{"按 owner", ServiceFilter{Owner: "other"}, 1},
		{"按协议", ServiceFilter{Protocol: "grpc"}, 1},
		{"按关键字", ServiceFilter{Query: "事件"}, 1},
		{"无命中", ServiceFilter{Tag: "nope"}, 0},
	}
	for _, c := range cases {
		got, total, err := st.ListServices(ctx, c.filter)
		if err != nil {
			t.Fatalf("%s：查询失败 %v", c.name, err)
		}
		if len(got) != c.want || total != c.want {
			t.Errorf("%s：期望 %d，实际 %d（total=%d）", c.name, c.want, len(got), total)
		}
	}

	// 分页与端点索引：limit/offset 生效，且返回的服务带端点。
	got, total, err := st.ListServices(ctx, ServiceFilter{Limit: 1})
	if err != nil || len(got) != 1 || total != 2 {
		t.Fatalf("分页错误：len=%d total=%d err=%v", len(got), total, err)
	}
	if got[0].Name != "alpha" || len(got[0].API.Endpoints) != 1 {
		t.Fatalf("列表未带端点索引或排序错误：%+v", got[0])
	}
}

func ptr(s string) *string { return &s }
