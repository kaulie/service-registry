package store

import (
	"context"
	"errors"
	"testing"

	"github.com/kaulie/service-registry/internal/model"
)

func TestCreateInstanceRules(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustNamespace(t, st, "ns")

	// 服务契约不存在 → ErrNotFound（实例必须挂在已登记的契约上）。
	if _, err := st.CreateInstance(ctx, model.Instance{
		Namespace: "ns", Service: "ghost", Scheme: "http", Host: "127.0.0.1", Port: 1,
	}, "tester"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未登记服务时应 ErrNotFound，实际 %v", err)
	}

	mustService(t, st, "ns", "svc", model.Endpoint{Method: "GET", Path: "/x"})
	inst, err := st.CreateInstance(ctx, model.Instance{
		Namespace: "ns", Service: "svc", Scheme: "http", Host: "127.0.0.1", Port: 8080,
		Metadata: map[string]string{"zone": "a"},
	}, "tester")
	if err != nil {
		t.Fatalf("创建实例失败：%v", err)
	}
	if inst.ID == "" || inst.Addr() != "http://127.0.0.1:8080" || inst.Revision == 0 {
		t.Fatalf("实例字段不正确：%+v", inst)
	}

	// 同地址重复登记 → 冲突。
	if _, err := st.CreateInstance(ctx, model.Instance{
		Namespace: "ns", Service: "svc", Scheme: "http", Host: "127.0.0.1", Port: 8080,
	}, "tester"); !errors.Is(err, ErrConflict) {
		t.Fatalf("同地址应冲突，实际 %v", err)
	}

	// PATCH：改 scheme 成功；把地址改成与已有实例相同则冲突。
	scheme := "https"
	if _, err := st.UpdateInstance(ctx, "ns", "svc", inst.ID, InstancePatch{Scheme: &scheme}, "tester"); err != nil {
		t.Fatalf("更新 scheme 失败：%v", err)
	}
	other, err := st.CreateInstance(ctx, model.Instance{
		Namespace: "ns", Service: "svc", Scheme: "https", Host: "10.0.0.9", Port: 8443,
	}, "tester")
	if err != nil {
		t.Fatalf("创建第二个实例失败：%v", err)
	}
	conflictHost, conflictPort := "127.0.0.1", 8080
	if _, err := st.UpdateInstance(ctx, "ns", "svc", other.ID,
		InstancePatch{Host: &conflictHost, Port: &conflictPort}, "tester"); !errors.Is(err, ErrConflict) {
		t.Fatalf("撞地址应冲突，实际 %v", err)
	}

	// 元数据更新后应真的落库。
	meta := map[string]string{"zone": "b"}
	updated, err := st.UpdateInstance(ctx, "ns", "svc", other.ID, InstancePatch{Metadata: &meta}, "tester")
	if err != nil || updated.Metadata["zone"] != "b" {
		t.Fatalf("更新 metadata 失败：%+v %v", updated, err)
	}

	got, err := st.GetInstance(ctx, inst.ID)
	if err != nil || got.Scheme != "https" {
		t.Fatalf("读取实例错误：%+v %v", got, err)
	}
	if _, err := st.GetInstance(ctx, "inst_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的实例应 ErrNotFound，实际 %v", err)
	}

	if err := st.DeleteInstance(ctx, "ns", "svc", other.ID, "tester"); err != nil {
		t.Fatalf("删除实例失败：%v", err)
	}
	list, err := st.ListInstances(ctx, "ns", "svc")
	if err != nil || len(list) != 1 {
		t.Fatalf("实例列表错误：%+v %v", list, err)
	}
}
