package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// rebind 把 SQLite 的 `?` 占位符换成 PostgreSQL 的 `$1`…`$n`。
// 引号里的问号保持原样（本仓库的 SQL 几乎不用，但避免误伤字面量）。
func rebind(postgres bool, query string) string {
	if !postgres || !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	inStr := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		if c == '\'' {
			b.WriteByte(c)
			// 字符串里的 '' 是转义的单引号，不结束字面量。
			if inStr && i+1 < len(query) && query[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			inStr = !inStr
			continue
		}
		if c == '?' && !inStr {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// sqlDB 在 PostgreSQL 上重写占位符；SQLite 原样下发。
type sqlDB struct {
	*sql.DB
	pg bool
}

func (d *sqlDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.DB.ExecContext(ctx, rebind(d.pg, query), args...)
}

func (d *sqlDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, rebind(d.pg, query), args...)
}

func (d *sqlDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, rebind(d.pg, query), args...)
}

func (d *sqlDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sqlTx, error) {
	tx, err := d.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &sqlTx{Tx: tx, pg: d.pg}, nil
}

// sqlTx 是写事务句柄。嵌入 *sql.Tx，Commit / Rollback 仍走原实现。
type sqlTx struct {
	*sql.Tx
	pg bool
}

func (t *sqlTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, rebind(t.pg, query), args...)
}

func (t *sqlTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, rebind(t.pg, query), args...)
}

func (t *sqlTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, rebind(t.pg, query), args...)
}
