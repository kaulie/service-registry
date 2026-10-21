package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaulie/service-registry/internal/model"
)

func TestNormalizeSQLiteValue(t *testing.T) {
	if got := normalizeSQLiteValue([]byte("abc")); got != "abc" {
		t.Fatalf("[]byte 应变成字符串，实际 %#v", got)
	}
	if got := normalizeSQLiteValue(int64(7)); got != int64(7) {
		t.Fatalf("整数应原样保留，实际 %#v", got)
	}
}

func TestImportLegacySQLiteRequiresPostgres(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.ImportLegacySQLite(context.Background(), filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("SQLite 存储不应接受导入")
	}
}

func TestImportLegacySQLite(t *testing.T) {
	dsn := os.Getenv("REGISTRY_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 REGISTRY_POSTGRES_TEST_DSN")
	}
	ctx := context.Background()

	t.Run("空库导入且不复活已删除的服务", func(t *testing.T) {
		st := openPostgresScratch(t, dsn)
		missing := filepath.Join(t.TempDir(), "no-such.db")
		got, err := st.ImportLegacySQLite(ctx, missing)
		if err != nil || got.Imported || got.Status != SQLiteImportAbsent {
			t.Fatalf("没有文件时应跳过：%+v %v", got, err)
		}

		path := filepath.Join(t.TempDir(), "registry.db")
		seedSQLiteCatalog(t, path)

		got, err = st.ImportLegacySQLite(ctx, path)
		if err != nil || !got.Imported || got.Status != SQLiteImportImported {
			t.Fatalf("应导入：%+v %v", got, err)
		}
		svcs, total, err := st.ListServices(ctx, ServiceFilter{})
		if err != nil || total != 1 || len(svcs) != 1 || svcs[0].Name != "catalog" {
			t.Fatalf("导入后的目录不对：total=%d %+v %v", total, svcs, err)
		}
		insts, err := st.ListInstances(ctx, "team-a", "catalog")
		if err != nil || len(insts) != 1 || insts[0].Host != "10.0.0.8" || insts[0].Port != 9099 {
			t.Fatalf("导入后的实例不对：%+v %v", insts, err)
		}
		rev, err := st.CurrentRevision(ctx)
		if err != nil || rev < 1 {
			t.Fatalf("导入后的 revision 应大于 0：%d %v", rev, err)
		}

		// 序列必须拨过已导入的 revision，否则下一次写入会撞主键。
		if _, err := st.CreateInstance(ctx, model.Instance{
			Namespace: "team-a", Service: "catalog", Scheme: "http", Host: "10.0.0.9", Port: 9099, OS: "linux",
		}, "tester"); err != nil {
			t.Fatalf("导入后继续写入失败（revision 序列可能没拨过去）：%v", err)
		}

		again, err := st.ImportLegacySQLite(ctx, path)
		if err != nil || again.Imported || again.Status != SQLiteImportAlready {
			t.Fatalf("第二次应不再导入：%+v %v", again, err)
		}
		if err := st.DeleteService(ctx, "team-a", "catalog", "tester"); err != nil {
			t.Fatalf("删除服务失败：%v", err)
		}
		if _, err := st.ImportLegacySQLite(ctx, path); err != nil {
			t.Fatalf("已记账后再次导入失败：%v", err)
		}
		if _, err := st.GetService(ctx, "team-a", "catalog"); err != ErrNotFound {
			t.Fatalf("已删除的服务不应从 SQLite 复活：%v", err)
		}
	})

	t.Run("已有数据不被旧 SQLite 覆盖", func(t *testing.T) {
		st := openPostgresScratch(t, dsn)
		mustNamespace(t, st, "keep")
		mustService(t, st, "keep", "live", model.Endpoint{Method: "GET", Path: "/health"})

		path := filepath.Join(t.TempDir(), "older.db")
		seedSQLiteCatalog(t, path)
		got, err := st.ImportLegacySQLite(ctx, path)
		if err != nil || got.Imported || got.Status != SQLiteImportSkippedNonEmpty {
			t.Fatalf("非空库应跳过：%+v %v", got, err)
		}
		if _, err := st.GetService(ctx, "team-a", "catalog"); err != ErrNotFound {
			t.Fatalf("旧 SQLite 的服务不应写进来：%v", err)
		}
		if _, err := st.GetService(ctx, "keep", "live"); err != nil {
			t.Fatalf("原有服务应还在：%v", err)
		}
		again, err := st.ImportLegacySQLite(ctx, path)
		if err != nil || again.Status != SQLiteImportAlready {
			t.Fatalf("跳过之后应记账：%+v %v", again, err)
		}
	})
}

func seedSQLiteCatalog(t *testing.T, path string) {
	t.Helper()
	src, err := Open(path)
	if err != nil {
		t.Fatalf("准备 SQLite 失败：%v", err)
	}
	defer src.Close()
	ctx := context.Background()
	if _, err := src.CreateNamespace(ctx, "team-a", "from sqlite", "hash-team-a", "tester"); err != nil {
		t.Fatalf("SQLite 命名空间失败：%v", err)
	}
	mustService(t, src, "team-a", "catalog", model.Endpoint{Method: "GET", Path: "/health"})
	if _, err := src.CreateInstance(ctx, model.Instance{
		Namespace: "team-a", Service: "catalog", Scheme: "http", Host: "10.0.0.8", Port: 9099, OS: "linux", Arch: "amd64",
	}, "tester"); err != nil {
		t.Fatalf("SQLite 实例失败：%v", err)
	}
}

// openPostgresScratch 在测试用的 PostgreSQL 里建一个独立 schema，测完删掉。
// 先确认 search_path 生效，再让 OpenPostgres 建表，避免误写到 public。
func openPostgresScratch(t *testing.T, dsn string) *Store {
	t.Helper()
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("生成 schema 名失败：%v", err)
	}
	schema := "sr_imp_" + hex.EncodeToString(buf)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("连接 PostgreSQL 失败：%v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatalf("创建测试 schema 失败：%v", err)
	}

	scratch := dsnWithSearchPath(dsn, schema)
	probe, err := sql.Open("pgx", scratch)
	if err != nil {
		t.Fatalf("打开测试 schema 失败：%v", err)
	}
	var got string
	err = probe.QueryRow(`SELECT current_schema()`).Scan(&got)
	_ = probe.Close()
	if err != nil {
		t.Fatalf("读取 current_schema 失败：%v", err)
	}
	if got != schema {
		t.Fatalf("search_path 没生效（当前 %s），拒绝继续以免写到 public", got)
	}

	st, err := OpenPostgres(scratch)
	if err != nil {
		t.Fatalf("打开 PostgreSQL 失败：%v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var opened string
	if err := st.db.QueryRowContext(context.Background(), `SELECT current_schema()`).Scan(&opened); err != nil {
		t.Fatalf("确认 schema 失败：%v", err)
	}
	if opened != schema {
		t.Fatalf("OpenPostgres 没落在测试 schema（当前 %s）", opened)
	}
	return st
}

func dsnWithSearchPath(dsn, schema string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "options=" + url.QueryEscape("-csearch_path="+schema)
}
