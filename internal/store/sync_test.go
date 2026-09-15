package store

import (
	"context"
	"testing"

	"github.com/kaulie/service-registry/internal/model"
)

// TestSyncInstancesDeclarative 验证声明式整组对齐：补齐、更新、摘除、幂等。
func TestSyncInstancesDeclarative(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustNamespace(t, st, "ns")
	mustService(t, st, "ns", "svc", model.Endpoint{Method: "GET", Path: "/x"})

	res, err := st.SyncInstances(ctx, "ns", "svc", []model.Instance{
		{Scheme: "http", Host: "10.0.0.1", Port: 80},
		{Scheme: "http", Host: "10.0.0.2", Port: 80},
	}, "ci")
	if err != nil {
		t.Fatalf("首次同步失败：%v", err)
	}
	if res.Created != 2 || res.Updated != 0 || res.Deleted != 0 {
		t.Fatalf("首次同步计数错误：%+v", res)
	}

	// 重复同集合 → 全部为 unchanged（幂等，CI 可以放心反复调）。
	res, err = st.SyncInstances(ctx, "ns", "svc", []model.Instance{
		{Scheme: "http", Host: "10.0.0.1", Port: 80},
		{Scheme: "http", Host: "10.0.0.2", Port: 80},
	}, "ci")
	if err != nil {
		t.Fatalf("重复同步失败：%v", err)
	}
	if res.Unchanged != 2 || res.Created != 0 || res.Updated != 0 || res.Deleted != 0 {
		t.Fatalf("重复同步应为全部 unchanged：%+v", res)
	}

	// 换掉一个、保留一个、新增一个。
	// 语义注意：实例身份按 (scheme,host,port) 判定，所以"地址变了"= 摘除旧的 + 新建新的；
	// 想让"换地址"记作 update，必须显式带 id 锚定身份（见下一个断言）。
	res, err = st.SyncInstances(ctx, "ns", "svc", []model.Instance{
		{Scheme: "http", Host: "10.0.0.1", Port: 8080},                                            // 地址变了 → 新建
		{Scheme: "http", Host: "10.0.0.2", Port: 80, Metadata: map[string]string{"tier": "edge"}}, // 元数据变了 → 更新
		{Scheme: "http", Host: "10.0.0.3", Port: 80},                                              // 新增
	}, "ci")
	if err != nil {
		t.Fatalf("二次同步失败：%v", err)
	}
	if res.Created != 2 || res.Updated != 1 || res.Deleted != 1 || len(res.Instances) != 3 {
		t.Fatalf("二次同步计数错误：%+v", res)
	}

	// 带 id 锚定：同一个实例换端口应记作 update，而不是删旧建新。
	target := res.Instances[0]
	res, err = st.SyncInstances(ctx, "ns", "svc", []model.Instance{
		{ID: target.ID, Scheme: "http", Host: "10.0.0.1", Port: 9090},
		{Scheme: "http", Host: "10.0.0.2", Port: 80, Metadata: map[string]string{"tier": "edge"}},
		{Scheme: "http", Host: "10.0.0.3", Port: 80},
	}, "ci")
	if err != nil {
		t.Fatalf("按 id 锚定的同步失败：%v", err)
	}
	if res.Updated != 1 || res.Created != 0 || res.Deleted != 0 || res.Unchanged != 2 {
		t.Fatalf("按 id 锚定的同步计数错误：%+v", res)
	}

	// 声明式语义：期望集合里没有的一律摘除。
	res, err = st.SyncInstances(ctx, "ns", "svc", []model.Instance{
		{Scheme: "http", Host: "10.0.0.2", Port: 80, Metadata: map[string]string{"tier": "edge"}},
	}, "ci")
	if err != nil {
		t.Fatalf("收敛同步失败：%v", err)
	}
	if res.Deleted != 2 || res.Unchanged != 1 {
		t.Fatalf("收敛同步计数错误：%+v", res)
	}
	list, err := st.ListInstances(ctx, "ns", "svc")
	if err != nil || len(list) != 1 || list[0].Host != "10.0.0.2" {
		t.Fatalf("最终实例集合错误：%+v %v", list, err)
	}

	// 空集合 = 全部摘除（CI 下线服务时用它一次清干净）。
	res, err = st.SyncInstances(ctx, "ns", "svc", nil, "ci")
	if err != nil || res.Deleted != 1 {
		t.Fatalf("空集合同步错误：%+v %v", res, err)
	}

	// 服务契约不存在时应报错，不静默建孤儿实例。
	if _, err := st.SyncInstances(ctx, "ns", "ghost", nil, "ci"); err == nil {
		t.Fatal("未登记服务时应报错")
	}
}

func TestChangesCursorAndAudit(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustNamespace(t, st, "ns")
	mustService(t, st, "ns", "svc", model.Endpoint{Method: "GET", Path: "/x"})
	if _, err := st.CreateInstance(ctx, model.Instance{
		Namespace: "ns", Service: "svc", Scheme: "http", Host: "10.0.0.1", Port: 80,
	}, "ci"); err != nil {
		t.Fatalf("创建实例失败：%v", err)
	}

	rev, err := st.CurrentRevision(ctx)
	if err != nil {
		t.Fatalf("读取 revision 失败：%v", err)
	}
	if rev != 3 { // 命名空间 + 服务 + 实例
		t.Fatalf("revision 应等于变更条数 3，实际 %d", rev)
	}

	all, hasMore, err := st.Changes(ctx, ChangeFilter{Since: 0, Limit: 10})
	if err != nil || hasMore || len(all) != 3 {
		t.Fatalf("全量变更错误：len=%d hasMore=%v err=%v", len(all), hasMore, err)
	}
	if all[0].Entity != model.EntityNamespace || all[1].Entity != model.EntityService || all[2].Entity != model.EntityInstance {
		t.Fatalf("变更顺序/实体错误：%+v", all)
	}
	if all[2].Actor != "ci" || all[2].Ref != "ns/svc/"+all[2].Ref[len("ns/svc/"):] {
		t.Fatalf("审计字段错误：%+v", all[2])
	}

	// 游标：since=2 只应看到第 3 条。
	tail, _, err := st.Changes(ctx, ChangeFilter{Since: 2, Limit: 10})
	if err != nil || len(tail) != 1 || tail[0].Revision != 3 {
		t.Fatalf("增量拉取错误：%+v %v", tail, err)
	}

	// 分页：limit=2 时应 hasMore=true。
	page, hasMore, err := st.Changes(ctx, ChangeFilter{Since: 0, Limit: 2})
	if err != nil || !hasMore || len(page) != 2 {
		t.Fatalf("分页错误：len=%d hasMore=%v err=%v", len(page), hasMore, err)
	}

	// 过滤：按实体 / 按命名空间 / 按引用。
	if got, _, _ := st.Changes(ctx, ChangeFilter{Entity: model.EntityInstance}); len(got) != 1 {
		t.Errorf("按实体过滤错误：%+v", got)
	}
	if got, _, _ := st.Changes(ctx, ChangeFilter{Namespace: "other"}); len(got) != 0 {
		t.Errorf("按命名空间过滤错误：%+v", got)
	}
	if got, _, _ := st.Changes(ctx, ChangeFilter{Ref: "ns/svc"}); len(got) != 1 {
		t.Errorf("按引用过滤错误：%+v", got)
	}

	// 快照 revision 与全局 revision 一致。
	snap, err := st.Snapshot(ctx, "")
	if err != nil || snap.Revision != 3 || len(snap.Services) != 1 || len(snap.Instances) != 1 {
		t.Fatalf("快照错误：%+v %v", snap, err)
	}
	filtered, err := st.Snapshot(ctx, "other")
	if err != nil || len(filtered.Services) != 0 || len(filtered.Namespaces) != 0 {
		t.Fatalf("按命名空间快照错误：%+v %v", filtered, err)
	}
}
