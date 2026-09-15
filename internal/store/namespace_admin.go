package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/kaulie/service-registry/internal/model"
)

// UpdateNamespace 更新描述（description 为 nil 表示不改描述，只刷新时间戳）。
func (s *Store) UpdateNamespace(ctx context.Context, name string, description *string, actor string) (model.Namespace, error) {
	if _, _, err := s.GetNamespace(ctx, name); err != nil {
		return model.Namespace{}, err
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if description != nil {
			if _, err := tx.ExecContext(ctx,
				`UPDATE namespaces SET description = ?, updated_at = ? WHERE name = ?`,
				*description, formatTime(time.Now()), name); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx,
			`UPDATE namespaces SET updated_at = ? WHERE name = ?`, formatTime(time.Now()), name); err != nil {
			return err
		}
		_, err := recordChange(ctx, tx, change{
			Namespace: name, Entity: model.EntityNamespace, Ref: name,
			Op: model.OpUpdate, Actor: actor, Detail: "更新命名空间描述",
		})
		return err
	})
	if err != nil {
		return model.Namespace{}, err
	}
	ns, _, err := s.GetNamespace(ctx, name)
	return ns, err
}

// SetNamespaceToken 设置/轮换命名空间令牌（tokenHash 为空则清除令牌）。
func (s *Store) SetNamespaceToken(ctx context.Context, name, tokenHash, actor, detail string) error {
	if _, _, err := s.GetNamespace(ctx, name); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE namespaces SET token_hash = ?, updated_at = ? WHERE name = ?`,
			tokenHash, formatTime(time.Now()), name); err != nil {
			return err
		}
		_, err := recordChange(ctx, tx, change{
			Namespace: name, Entity: model.EntityNamespace, Ref: name,
			Op: model.OpUpdate, Actor: actor, Detail: detail,
		})
		return err
	})
}

// DeleteNamespace 删除命名空间及其全部服务与实例（显式级联）。
func (s *Store) DeleteNamespace(ctx context.Context, name, actor string) error {
	if _, _, err := s.GetNamespace(ctx, name); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`DELETE FROM instances WHERE namespace = ?`,
			`DELETE FROM endpoints WHERE namespace = ?`,
			`DELETE FROM services WHERE namespace = ?`,
			`DELETE FROM namespaces WHERE name = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, name); err != nil {
				return err
			}
		}
		_, err := recordChange(ctx, tx, change{
			Namespace: name, Entity: model.EntityNamespace, Ref: name,
			Op: model.OpDelete, Actor: actor, Detail: "删除命名空间（含其全部服务与实例）",
		})
		return err
	})
}
