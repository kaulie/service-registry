// 增量迁移（storage schema migration）
//
// `CREATE TABLE IF NOT EXISTS` 只对**新库**生效，已存在的表不会因为 schema 里多了一列
// 就跟着变。而本服务的部署方式是原地升级（runtime 目录里的 backend/data/registry.db
// 会被保留），所以每次给表加列都必须显式 `ALTER TABLE ... ADD COLUMN` 一次。
//
// 约定：columnMigrations 只增不改（历史上跑过的条目不要动、不要删），每一条自己判断是否
// 已经生效，这样在任意老库上重复执行都安全。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
)

// columnMigration 是"给已存在的表补一列"。
type columnMigration struct {
	Table  string
	Column string
	DDL    string
}

// columnMigrations 按时间顺序追加。写在这里的 DDL 必须自带默认值
// （ADD COLUMN 的 NOT NULL 列在 SQLite 里要求有非 NULL 默认值）。
var columnMigrations = []columnMigration{
	{
		Table:  "services",
		Column: "git_repo_url",
		DDL:    `ALTER TABLE services ADD COLUMN git_repo_url TEXT NOT NULL DEFAULT ''`,
	},
}

// applyColumnMigrations 幂等地把缺的列补上（SQLite 没有 ADD COLUMN IF NOT EXISTS）。
func (s *Store) applyColumnMigrations(ctx context.Context) error {
	cache := map[string]map[string]bool{}
	for _, m := range columnMigrations {
		cols, ok := cache[m.Table]
		if !ok {
			var err error
			cols, err = tableColumns(ctx, s.db, m.Table)
			if err != nil {
				return fmt.Errorf("读取表 %s 的结构失败：%w", m.Table, err)
			}
			cache[m.Table] = cols
		}
		if cols[m.Column] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, m.DDL); err != nil {
			return fmt.Errorf("迁移 %s.%s 失败：%w", m.Table, m.Column, err)
		}
		cols[m.Column] = true
	}
	return nil
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// tableColumns 返回表的列名集合（走 pragma_table_info 表值函数）。
func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	if !identRe.MatchString(table) {
		return nil, fmt.Errorf("非法的表名：%q", table)
	}
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info('`+table+`')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}
