package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// SQLite 导入结果。Status 取值见下面的常量。
type SQLiteImport struct {
	Imported bool
	Status   string
}

const (
	// SQLiteImportImported 本次把 SQLite 里的目录复制进了 PostgreSQL。
	SQLiteImportImported = "imported"
	// SQLiteImportSkippedNonEmpty PostgreSQL 里已经有目录，没有用 SQLite 覆盖。
	SQLiteImportSkippedNonEmpty = "skipped-nonempty"
	// SQLiteImportAlready 之前已经处理过（导入过，或确认过不用导入）。
	SQLiteImportAlready = "already"
	// SQLiteImportAbsent 没有可导入的 SQLite 文件。
	SQLiteImportAbsent = "absent"
)

const sqliteImportedKey = "sqlite_imported_v1"

// sqliteImportTables 是复制顺序。没有外键；changes 放最后，方便接着把自增序列拨过去。
var sqliteImportTables = []string{
	"namespaces",
	"services",
	"endpoints",
	"instances",
	"agent_mcp_servers",
	"changes",
}

// sqliteImportDataTables 用来判断 PostgreSQL 是不是空库。
// 只有启动时播种的命名空间不算：那种库仍然应该把 SQLite 接进来。
var sqliteImportDataTables = []string{
	"services",
	"endpoints",
	"instances",
	"agent_mcp_servers",
	"changes",
}

// ImportLegacySQLite 把 REGISTRY_DB_PATH 指向的 SQLite 导入当前 PostgreSQL，只做一次。
//
// 库还是空的、文件也在，就整份复制并记下 meta.sqlite_imported_v1。之后再启动不再读这个文件，
// 所以在 PostgreSQL 里删掉的服务不会从 SQLite 里复活。文件不存在不算错误，也不记账，
// 这样文件若在第一次写入之前出现，仍可以导入。库里已经有服务 / 实例 / 变更时原样保留，
// 只记账，避免用一份更旧的 SQLite 把线上目录盖掉。
func (s *Store) ImportLegacySQLite(ctx context.Context, path string) (SQLiteImport, error) {
	if !s.pg {
		return SQLiteImport{}, fmt.Errorf("只有 PostgreSQL 才能导入 SQLite")
	}
	if err := s.ensureMetaTable(ctx); err != nil {
		return SQLiteImport{}, err
	}
	done, err := s.sqliteImportMarked(ctx)
	if err != nil {
		return SQLiteImport{}, err
	}
	if done {
		return SQLiteImport{Status: SQLiteImportAlready}, nil
	}
	if isMemory(path) || strings.TrimSpace(path) == "" {
		return SQLiteImport{Status: SQLiteImportAbsent}, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return SQLiteImport{Status: SQLiteImportAbsent}, nil
		}
		return SQLiteImport{}, err
	}

	nonempty, err := s.postgresCatalogNonEmpty(ctx)
	if err != nil {
		return SQLiteImport{}, err
	}
	if nonempty {
		if err := s.markSQLiteImported(ctx, "skipped-nonempty"); err != nil {
			return SQLiteImport{}, err
		}
		return SQLiteImport{Status: SQLiteImportSkippedNonEmpty}, nil
	}

	src, err := openLegacySQLite(path)
	if err != nil {
		return SQLiteImport{}, err
	}
	defer src.Close()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SQLiteImport{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// 空库上可能已经有启动时播种的命名空间。目录本身是空的，用 SQLite 里的那份换掉它，
	// 这样令牌哈希不会丢。
	if _, err := tx.ExecContext(ctx, `DELETE FROM namespaces`); err != nil {
		return SQLiteImport{}, fmt.Errorf("清掉播种的命名空间失败：%w", err)
	}
	for _, table := range sqliteImportTables {
		if err := copySQLiteTable(ctx, tx, src, table); err != nil {
			return SQLiteImport{}, err
		}
	}
	if err := resetChangesSequence(ctx, tx); err != nil {
		return SQLiteImport{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO meta (k, v) VALUES (?, ?)`,
		sqliteImportedKey, time.Now().UTC().Format(time.RFC3339)+" imported",
	); err != nil {
		return SQLiteImport{}, fmt.Errorf("记录 SQLite 导入标记失败：%w", err)
	}
	if err := tx.Commit(); err != nil {
		return SQLiteImport{}, err
	}
	return SQLiteImport{Imported: true, Status: SQLiteImportImported}, nil
}

func (s *Store) ensureMetaTable(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS meta (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
)`)
	if err != nil {
		return fmt.Errorf("准备导入标记表失败：%w", err)
	}
	return nil
}

func (s *Store) sqliteImportMarked(ctx context.Context) (bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT v FROM meta WHERE k = ?`, sqliteImportedKey).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) postgresCatalogNonEmpty(ctx context.Context) (bool, error) {
	for _, table := range sqliteImportDataTables {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			return false, fmt.Errorf("检查表 %s 是否为空失败：%w", table, err)
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) markSQLiteImported(ctx context.Context, note string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta (k, v) VALUES (?, ?) ON CONFLICT (k) DO NOTHING`,
		sqliteImportedKey, time.Now().UTC().Format(time.RFC3339)+" "+note,
	)
	if err != nil {
		return fmt.Errorf("记录 SQLite 导入标记失败：%w", err)
	}
	return nil
}

func openLegacySQLite(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("打开 SQLite %s 失败：%w", path, err)
	}
	return db, nil
}

func copySQLiteTable(ctx context.Context, tx *sqlTx, src *sql.DB, table string) error {
	if !identRe.MatchString(table) {
		return fmt.Errorf("非法的表名：%q", table)
	}
	var one int
	err := src.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&one)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	srcCols, err := sqliteColumnNames(ctx, src, table)
	if err != nil {
		return err
	}
	dstCols, err := postgresColumnNames(ctx, tx, table)
	if err != nil {
		return err
	}
	dstSet := map[string]bool{}
	for _, c := range dstCols {
		dstSet[c] = true
	}
	var cols []string
	for _, c := range srcCols {
		if !identRe.MatchString(c) || !dstSet[c] {
			continue
		}
		cols = append(cols, c)
	}
	if len(cols) == 0 {
		return nil
	}

	rows, err := src.QueryContext(ctx, `SELECT `+strings.Join(cols, ", ")+` FROM `+table)
	if err != nil {
		return fmt.Errorf("读取 SQLite 表 %s 失败：%w", table, err)
	}
	defer rows.Close()

	var batch [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		for i, v := range vals {
			vals[i] = normalizeSQLiteValue(v)
		}
		batch = append(batch, vals)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return insertSQLiteRows(ctx, tx, table, cols, batch)
}

func sqliteColumnNames(ctx context.Context, src *sql.DB, table string) ([]string, error) {
	rows, err := src.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func postgresColumnNames(ctx context.Context, tx *sqlTx, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT column_name FROM information_schema.columns
 WHERE table_schema = current_schema() AND table_name = ?
 ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func insertSQLiteRows(ctx context.Context, tx *sqlTx, table string, cols []string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	const chunk = 80
	colList := strings.Join(cols, ", ")
	for start := 0; start < len(rows); start += chunk {
		end := start + chunk
		if end > len(rows) {
			end = len(rows)
		}
		part := rows[start:end]
		var b strings.Builder
		b.WriteString("INSERT INTO ")
		b.WriteString(table)
		b.WriteString(" (")
		b.WriteString(colList)
		b.WriteString(") VALUES ")
		args := make([]any, 0, len(part)*len(cols))
		for i, row := range part {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('(')
			for j := range cols {
				if j > 0 {
					b.WriteByte(',')
				}
				b.WriteByte('?')
				args = append(args, row[j])
			}
			b.WriteByte(')')
		}
		if _, err := tx.ExecContext(ctx, b.String(), args...); err != nil {
			return fmt.Errorf("写入 %s 失败：%w", table, err)
		}
	}
	return nil
}

func resetChangesSequence(ctx context.Context, tx *sqlTx) error {
	_, err := tx.ExecContext(ctx, `
SELECT setval(pg_get_serial_sequence('changes', 'revision'), s.m, true)
  FROM (SELECT MAX(revision) AS m FROM changes) s
 WHERE s.m IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("拨动 changes.revision 序列失败：%w", err)
	}
	return nil
}

func normalizeSQLiteValue(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	default:
		return v
	}
}
