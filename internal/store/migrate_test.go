package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/service-registry/internal/model"
)

// TestOpenMigratesLegacyServicesTable 验证"老库补列"。
//
// 为什么必须有这个测试：`CREATE TABLE IF NOT EXISTS` 不会给**已存在**的表加列，而本服务
// 是原地升级（runtime 里的 backend/data/registry.db 会被保留），所以不写迁移的话，
// 老库会在读 git_repo_url 时报 "no such column"。
func TestOpenMigratesLegacyServicesTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")

	// 用"加列之前"的建表语句造一个老库。
	legacy := strings.Replace(schema, "  git_repo_url  TEXT NOT NULL DEFAULT '',\n", "", 1)
	if legacy == schema {
		t.Fatal("schema 里已经没有 git_repo_url 这一行了，本测试的前提失效（请更新测试）")
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("打开老库失败：%v", err)
	}
	if _, err := db.Exec(legacy); err != nil {
		t.Fatalf("建老表失败：%v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO services (namespace, name, created_at, updated_at)
		 VALUES ('legacy', 'svc', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("写入老数据失败：%v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭老库失败：%v", err)
	}

	st, err := Open(path) // 迁移在这里发生
	if err != nil {
		t.Fatalf("打开并迁移失败：%v", err)
	}
	defer func() { _ = st.Close() }()

	cols, err := tableColumns(ctx, st.db, "services")
	if err != nil {
		t.Fatalf("读取表结构失败：%v", err)
	}
	if !cols["git_repo_url"] {
		t.Fatal("迁移没有给老库的 services 补上 git_repo_url 列")
	}

	// 老数据还在，新列取默认值（空）。
	svc, err := st.GetService(ctx, "legacy", "svc")
	if err != nil {
		t.Fatalf("读取老数据失败：%v", err)
	}
	if svc.GitRepoURL != "" {
		t.Fatalf("老数据的新列应为空，实际 %q", svc.GitRepoURL)
	}

	// 迁移后写入/读回都要正常。
	if _, _, err := st.UpsertService(ctx, ServiceInput{Service: model.Service{
		Namespace: "legacy", Name: "svc", GitRepoURL: "https://github.com/kaulie/legacy.git",
	}}, "tester"); err != nil {
		t.Fatalf("写入 gitRepoUrl 失败：%v", err)
	}
	svc, err = st.GetService(ctx, "legacy", "svc")
	if err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	if svc.GitRepoURL != "https://github.com/kaulie/legacy.git" {
		t.Fatalf("读回的 gitRepoUrl 不对：%q", svc.GitRepoURL)
	}

	// 幂等：再开一次（迁移条目已生效）不能报错，数据不受影响。
	again, err := Open(path)
	if err != nil {
		t.Fatalf("重复打开（迁移幂等性）失败：%v", err)
	}
	defer func() { _ = again.Close() }()
	svc, err = again.GetService(ctx, "legacy", "svc")
	if err != nil || svc.GitRepoURL != "https://github.com/kaulie/legacy.git" {
		t.Fatalf("重复迁移后数据异常：%v / %q", err, svc.GitRepoURL)
	}
}
