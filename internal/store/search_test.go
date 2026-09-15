package store

import (
	"context"
	"testing"

	"github.com/kaulie/service-registry/internal/model"
)

func seedSearchable(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	mustNamespace(t, st, "ns")
	in := ServiceInput{
		Service: model.Service{
			Namespace: "ns", Name: "event-center",
			Tags: []string{"events"}, API: model.ServiceAPI{Protocols: []string{"http"}},
		},
		Endpoints: []model.Endpoint{
			{Method: "GET", Path: "/health", Summary: "健康检查"},
			{Method: "POST", Path: "/v1/ingest/{source}", Summary: "通用注入", Tags: []string{"ingest"}},
			{Method: "GET", Path: "/v1/streams/{stream}/events", Summary: "拉取事件"},
		},
	}
	if _, _, err := st.UpsertService(ctx, in, "tester"); err != nil {
		t.Fatalf("登记服务失败：%v", err)
	}
}

// TestSearchEndpointsMatching 覆盖"这个接口谁提供"的三种匹配方式。
func TestSearchEndpointsMatching(t *testing.T) {
	st := newTestStore(t)
	seedSearchable(t, st)
	ctx := context.Background()

	cases := []struct {
		name      string
		filter    EndpointFilter
		wantPaths []string
		wantType  string
	}{
		{
			name:      "精确路径",
			filter:    EndpointFilter{Method: "GET", Path: "/health"},
			wantPaths: []string{"/health"},
			wantType:  "exact",
		},
		{
			name:      "具体路径命中模板",
			filter:    EndpointFilter{Method: "GET", Path: "/v1/streams/abc/events"},
			wantPaths: []string{"/v1/streams/{stream}/events"},
			wantType:  "template",
		},
		{
			name:      "模板原样查询",
			filter:    EndpointFilter{Method: "GET", Path: "/v1/streams/{stream}/events"},
			wantPaths: []string{"/v1/streams/{stream}/events"},
			wantType:  "exact",
		},
		{
			name:      "通配跨段",
			filter:    EndpointFilter{Path: "/v1/**"},
			wantPaths: []string{"/v1/streams/{stream}/events", "/v1/ingest/{source}"},
		},
		{
			name:      "通配单段",
			filter:    EndpointFilter{Path: "/v1/streams/*/events"},
			wantPaths: []string{"/v1/streams/{stream}/events"},
			wantType:  "glob",
		},
		{
			name:      "强制精确则不再回退模板",
			filter:    EndpointFilter{Path: "/v1/streams/abc/events", Match: "exact"},
			wantPaths: nil,
		},
		{
			name:      "方法过滤",
			filter:    EndpointFilter{Method: "post", Path: "/v1/ingest/foo"},
			wantPaths: []string{"/v1/ingest/{source}"},
		},
		{
			name:      "关键字过滤（按摘要）",
			filter:    EndpointFilter{Query: "注入"},
			wantPaths: []string{"/v1/ingest/{source}"},
		},
		{
			name:      "命名空间不匹配",
			filter:    EndpointFilter{Namespace: "other"},
			wantPaths: nil,
		},
	}

	for _, c := range cases {
		got, _, err := st.SearchEndpoints(ctx, c.filter)
		if err != nil {
			t.Fatalf("%s：检索失败 %v", c.name, err)
		}
		var paths []string
		for _, m := range got {
			paths = append(paths, m.Endpoint.Path)
			if c.wantType != "" && m.MatchType != c.wantType {
				t.Errorf("%s：命中方式期望 %q，实际 %q", c.name, c.wantType, m.MatchType)
			}
		}
		if len(paths) != len(c.wantPaths) {
			t.Errorf("%s：期望 %v，实际 %v", c.name, c.wantPaths, paths)
			continue
		}
		for i := range paths {
			if paths[i] != c.wantPaths[i] {
				t.Errorf("%s：第 %d 条路径期望 %q，实际 %q", c.name, i, c.wantPaths[i], paths[i])
			}
		}
	}
}

func TestSearchEndpointsIncludesServiceContextAndPaging(t *testing.T) {
	st := newTestStore(t)
	seedSearchable(t, st)
	ctx := context.Background()

	if _, err := st.CreateInstance(ctx, model.Instance{
		Namespace: "ns", Service: "event-center", Scheme: "http", Host: "10.0.0.1", Port: 80,
	}, "ci"); err != nil {
		t.Fatalf("创建实例失败：%v", err)
	}

	got, _, err := st.SearchEndpoints(ctx, EndpointFilter{Path: "/v1/**", Limit: 1})
	if err != nil || len(got) != 1 {
		t.Fatalf("分页失败：%+v %v", got, err)
	}
	m := got[0]
	if m.Namespace != "ns" || m.Service != "event-center" || m.InstanceCount != 1 {
		t.Fatalf("命中项缺少服务上下文：%+v", m)
	}

	// 排序：命中方式优先（精确/模板先于通配），同类内路径长的在前。
	all, hasMore, err := st.SearchEndpoints(ctx, EndpointFilter{Path: "/**"})
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if hasMore || len(all) != 3 {
		t.Fatalf("期望 3 条命中且无更多：len=%d hasMore=%v", len(all), hasMore)
	}

	// 空结果必须是空数组（不能是 null），前端才好处理。
	empty, _, err := st.SearchEndpoints(ctx, EndpointFilter{Path: "/nope"})
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("空结果应为非 nil 空切片：%+v %v", empty, err)
	}
}
