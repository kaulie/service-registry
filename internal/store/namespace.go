package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/kaulie/service-registry/internal/model"
)

// ---- 命名空间 ----

// CreateNamespace 新建命名空间；已存在返回 ErrConflict。
// tokenHash 为空表示该命名空间只允许 admin 令牌写入。
func (s *Store) CreateNamespace(ctx context.Context, name, description, tokenHash, actor string) (model.Namespace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return model.Namespace{}, errors.New("命名空间名不能为空")
	}
	now := time.Now().UTC()
	var out model.Namespace
	err := s.write(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM namespaces WHERE name = ?`, name).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO namespaces (name, description, token_hash, created_at, updated_at) VALUES (?,?,?,?,?)`,
			name, description, tokenHash, formatTime(now), formatTime(now)); err != nil {
			return err
		}
		if _, err := recordChange(ctx, tx, change{
			Namespace: name, Entity: model.EntityNamespace, Ref: name,
			Op: model.OpCreate, Actor: actor, Detail: "创建命名空间",
		}); err != nil {
			return err
		}
		out = model.Namespace{Name: name, Description: description, TokenSet: tokenHash != "", CreatedAt: now, UpdatedAt: now}
		return nil
	})
	return out, err
}

// EnsureNamespace 幂等地保证命名空间存在（启动时播种用）。
func (s *Store) EnsureNamespace(ctx context.Context, name, description string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO namespaces (name, description, token_hash, created_at, updated_at)
		 VALUES (?,?,?,?,?) ON CONFLICT(name) DO NOTHING`,
		name, description, "", formatTime(time.Now()), formatTime(time.Now()))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetNamespace 读取命名空间（含 tokenHash，仅供鉴权层使用）。
func (s *Store) GetNamespace(ctx context.Context, name string) (model.Namespace, string, error) {
	var (
		ns                   model.Namespace
		createdAt, updatedAt string
		tokenHash            string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT name, description, token_hash, created_at, updated_at FROM namespaces WHERE name = ?`, name).
		Scan(&ns.Name, &ns.Description, &tokenHash, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Namespace{}, "", ErrNotFound
	}
	if err != nil {
		return model.Namespace{}, "", err
	}
	ns.CreatedAt, ns.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
	ns.TokenSet = tokenHash != ""
	return ns, tokenHash, nil
}

// NamespaceExists 判断命名空间是否存在。
func (s *Store) NamespaceExists(ctx context.Context, name string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM namespaces WHERE name = ?`, name).Scan(&n)
	return n > 0, err
}

// ListNamespaces 返回全部命名空间（含服务/实例计数）。
func (s *Store) ListNamespaces(ctx context.Context) ([]model.Namespace, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.name, n.description, n.token_hash <> '', n.created_at, n.updated_at,
		       (SELECT COUNT(1) FROM services s WHERE s.namespace = n.name),
		       (SELECT COUNT(1) FROM instances i WHERE i.namespace = n.name)
		FROM namespaces n ORDER BY n.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Namespace
	for rows.Next() {
		var (
			ns                   model.Namespace
			createdAt, updatedAt string
		)
		if err := rows.Scan(&ns.Name, &ns.Description, &ns.TokenSet, &createdAt, &updatedAt,
			&ns.ServiceCount, &ns.InstanceCount); err != nil {
			return nil, err
		}
		ns.CreatedAt, ns.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
		out = append(out, ns)
	}
	return out, rows.Err()
}
