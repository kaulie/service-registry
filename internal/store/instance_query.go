package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kaulie/service-registry/internal/model"
)

// InstancePatch 是实例的部分更新（nil 表示不改该字段）。
type InstancePatch struct {
	Scheme   *string
	Host     *string
	Port     *int
	Metadata *map[string]string
}

// UpdateInstance 部分更新实例声明（地址冲突 → ErrConflict）。
func (s *Store) UpdateInstance(ctx context.Context, namespace, service, id string, p InstancePatch, actor string) (model.Instance, error) {
	cur, err := s.instanceIn(ctx, namespace, service, id)
	if err != nil {
		return model.Instance{}, err
	}
	next := cur
	if p.Scheme != nil {
		next.Scheme = *p.Scheme
	}
	if p.Host != nil {
		next.Host = *p.Host
	}
	if p.Port != nil {
		next.Port = *p.Port
	}
	if p.Metadata != nil {
		next.Metadata = *p.Metadata
	}
	now := time.Now().UTC()

	err = s.write(ctx, func(tx *sql.Tx) error {
		var dup int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(1) FROM instances
			WHERE namespace = ? AND service = ? AND scheme = ? AND host = ? AND port = ? AND id <> ?`,
			namespace, service, next.Scheme, next.Host, next.Port, id).Scan(&dup); err != nil {
			return err
		}
		if dup > 0 {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE instances SET scheme = ?, host = ?, port = ?, metadata = ?, updated_at = ?, registered_by = ?
			WHERE id = ?`,
			next.Scheme, next.Host, next.Port, encodeMap(next.Metadata), formatTime(now), actor, id); err != nil {
			return err
		}
		rev, err := recordChange(ctx, tx, change{
			Namespace: namespace, Entity: model.EntityInstance, Ref: namespace + "/" + service + "/" + id,
			Op: model.OpUpdate, Actor: actor,
			Detail: fmt.Sprintf("更新实例 %s → %s", cur.Addr(), next.Addr()),
		})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE instances SET revision = ? WHERE id = ?`, rev, id)
		return err
	})
	if err != nil {
		return model.Instance{}, err
	}
	return s.instanceIn(ctx, namespace, service, id)
}

// DeleteInstance 删除实例声明。
func (s *Store) DeleteInstance(ctx context.Context, namespace, service, id, actor string) error {
	cur, err := s.instanceIn(ctx, namespace, service, id)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM instances WHERE id = ?`, id); err != nil {
			return err
		}
		_, err := recordChange(ctx, tx, change{
			Namespace: namespace, Entity: model.EntityInstance, Ref: namespace + "/" + service + "/" + id,
			Op: model.OpDelete, Actor: actor, Detail: fmt.Sprintf("注销实例 %s", cur.Addr()),
		})
		return err
	})
}

// ListInstances 返回某服务的全部实例（按地址排序，输出稳定）。
func (s *Store) ListInstances(ctx context.Context, namespace, service string) ([]model.Instance, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+instanceColumns+` FROM instances WHERE namespace = ? AND service = ?
		 ORDER BY scheme, host, port`, namespace, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.Instance, 0, 4)
	for rows.Next() {
		inst, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inst)
	}
	return out, rows.Err()
}

// GetInstance 按 ID 全局查询实例。
func (s *Store) GetInstance(ctx context.Context, id string) (model.Instance, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+instanceColumns+` FROM instances WHERE id = ?`, id)
	inst, err := scanInstance(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Instance{}, ErrNotFound
	}
	return inst, err
}

func (s *Store) instanceIn(ctx context.Context, namespace, service, id string) (model.Instance, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+instanceColumns+` FROM instances WHERE id = ? AND namespace = ? AND service = ?`,
		id, namespace, service)
	inst, err := scanInstance(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Instance{}, ErrNotFound
	}
	return inst, err
}

func listInstancesTx(ctx context.Context, tx *sql.Tx, namespace, service string) ([]model.Instance, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+instanceColumns+` FROM instances WHERE namespace = ? AND service = ? ORDER BY id`,
		namespace, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Instance
	for rows.Next() {
		inst, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inst)
	}
	return out, rows.Err()
}

func scanInstance(r rowScanner) (model.Instance, error) {
	var (
		inst                 model.Instance
		metadata             string
		createdAt, updatedAt string
	)
	if err := r.Scan(&inst.ID, &inst.Namespace, &inst.Service, &inst.Scheme, &inst.Host, &inst.Port,
		&metadata, &inst.Revision, &createdAt, &updatedAt, &inst.RegisteredBy); err != nil {
		return model.Instance{}, err
	}
	inst.Metadata = decodeMap(metadata)
	inst.RegisteredAt, inst.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
	return inst, nil
}

func addrKey(scheme, host string, port int) string {
	return fmt.Sprintf("%s|%s|%d", scheme, host, port)
}

func assertServiceExists(ctx context.Context, tx *sql.Tx, namespace, service string) error {
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM services WHERE namespace = ? AND name = ?`, namespace, service).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
