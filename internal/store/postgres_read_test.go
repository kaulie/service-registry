package store

import (
	"os"
	"testing"
)

// TestPostgresReadsImportedCatalog 连上已导入的 service_registry 库，确认读路径能取出目录。
// 没有 REGISTRY_POSTGRES_TEST_DSN 时跳过，CI 不依赖远端库。
func TestPostgresReadsImportedCatalog(t *testing.T) {
	dsn := os.Getenv("REGISTRY_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 REGISTRY_POSTGRES_TEST_DSN")
	}
	st, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("打开 PostgreSQL 失败：%v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := t.Context()
	svcs, total, err := st.ListServices(ctx, ServiceFilter{})
	if err != nil {
		t.Fatalf("列出服务失败：%v", err)
	}
	if total != 14 || len(svcs) != 14 {
		t.Fatalf("服务数应为 14，实际 total=%d len=%d", total, len(svcs))
	}
	if _, _, err := st.ListServices(ctx, ServiceFilter{Tag: "events"}); err != nil {
		t.Fatalf("按标签过滤失败：%v", err)
	}
	if _, _, err := st.ListServices(ctx, ServiceFilter{OrgID: "d0002", Query: "service"}); err != nil {
		t.Fatalf("按部门/关键字过滤失败：%v", err)
	}

	insts, err := st.ListInstances(ctx, "default", "organization")
	if err != nil || len(insts) != 1 || insts[0].Host != "127.0.0.1" || insts[0].OS != "mac" {
		t.Fatalf("organization 实例不正确：%+v %v", insts, err)
	}
	rev, err := st.CurrentRevision(ctx)
	if err != nil || rev != 145 {
		t.Fatalf("revision 应为 145，实际 %d %v", rev, err)
	}
}
