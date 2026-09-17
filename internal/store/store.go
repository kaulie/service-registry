// Package store 是注册中心的持久化层（SQLite + WAL）。
//
// 设计要点：
//   - **单写多读**：所有写操作在事务里完成，事务里顺带写一条全局递增的
//     changes 记录（既是增量拉取游标，也是审计日志），提交后再广播唤醒
//     所有 long-poll / SSE 读者。
//   - **revision 单调递增**：changes.revision 由 SQLite AUTOINCREMENT 提供，
//     每个实体也记录自己最后一次被改的 revision。
//   - **级联删除显式做**：不依赖 FK pragma，保证在任意连接配置下行为一致。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kaulie/service-registry/internal/model"
	"github.com/kaulie/service-registry/internal/notify"
)

// 数据层错误。
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// Store 是持久化句柄。
type Store struct {
	db  *sql.DB
	hub *notify.Hub
}

// Open 打开（必要时创建）数据库并建表。
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", dsnFor(path))
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败：%w", err)
	}
	// 内存库必须单连接，否则每个连接看到的是各自独立的库。
	if isMemory(path) {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(8)
	}
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连接数据库失败：%w", err)
	}
	s := &Store{db: db, hub: notify.New()}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// Hub 暴露广播器（提交后广播）。
func (s *Store) Hub() *notify.Hub { return s.hub }

// Ping 供 /readyz 使用。
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func isMemory(path string) bool {
	return path == ":memory:" || strings.Contains(path, "mode=memory")
}

func dsnFor(path string) string {
	base := path
	switch {
	case isMemory(path):
		base = "file::memory:?cache=shared"
	case !strings.HasPrefix(path, "file:"):
		base = "file:" + path
	}
	pragmas := []string{
		"_pragma=busy_timeout(5000)",
		"_pragma=foreign_keys(1)",
		"_pragma=synchronous(NORMAL)",
		// 写事务用 IMMEDIATE：先拿写锁再干活，避免"锁升级"导致的 SQLITE_BUSY。
		"_txlock=immediate",
	}
	if !isMemory(path) {
		pragmas = append([]string{"_pragma=journal_mode(WAL)"}, pragmas...)
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + strings.Join(pragmas, "&")
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("建表失败：%w", err)
	}
	if err := s.applyColumnMigrations(ctx); err != nil {
		return err
	}
	return nil
}

// write 在一个事务里执行 fn，并在提交后广播唤醒读者。
func (s *Store) write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// 提交成功才广播：读者醒来后一定能查到刚写入的数据。
	s.hub.Broadcast()
	return nil
}

// change 描述一条待写入的变更记录。
type change struct {
	Namespace string
	Entity    string
	Ref       string
	Op        string
	Actor     string
	Detail    string
}

// recordChange 写入一条变更记录并返回其 revision（全局单调递增）。
func recordChange(ctx context.Context, tx *sql.Tx, c change) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO changes (at, namespace, entity, ref, op, actor, detail) VALUES (?,?,?,?,?,?,?)`,
		formatTime(time.Now()), c.Namespace, c.Entity, c.Ref, c.Op, c.Actor, c.Detail)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ---- 时间 / JSON 小工具 ----

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func encodeJSON(v any, fallback string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fallback
	}
	return string(b)
}

func encodeStrings(v []string) string { return encodeJSON(orEmptySlice(v), "[]") }

func decodeStrings(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func encodeMap(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	return encodeJSON(m, "{}")
}

func decodeMap(s string) map[string]string {
	if s == "" {
		return nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		// 统一表示：空元数据一律是 nil（避免 nil 与 {} 比较不相等导致"假更新"）。
		return nil
	}
	return out
}

func encodeAuthSchemes(v []model.AuthScheme) string { return encodeJSON(orEmptyAuth(v), "[]") }

func decodeAuthSchemes(s string) []model.AuthScheme {
	if s == "" {
		return nil
	}
	var out []model.AuthScheme
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func orEmptySlice(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func orEmptyAuth(v []model.AuthScheme) []model.AuthScheme {
	if v == nil {
		return []model.AuthScheme{}
	}
	return v
}
