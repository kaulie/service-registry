package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/kaulie/service-registry/internal/idgen"
	"github.com/kaulie/service-registry/internal/model"
)

const instanceColumns = `id, namespace, service, scheme, host, port, metadata, revision, created_at,
  updated_at, registered_by`

// CreateInstance 新增一个实例声明（同地址已登记 → ErrConflict）。
func (s *Store) CreateInstance(ctx context.Context, inst model.Instance, actor string) (model.Instance, error) {
	if inst.ID == "" {
		inst.ID = idgen.New(idgen.PrefixInstance)
	}
	now := time.Now().UTC()
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := assertServiceExists(ctx, tx, inst.Namespace, inst.Service); err != nil {
			return err
		}
		var dup int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(1) FROM instances
			WHERE namespace = ? AND service = ? AND scheme = ? AND host = ? AND port = ?`,
			inst.Namespace, inst.Service, inst.Scheme, inst.Host, inst.Port).Scan(&dup); err != nil {
			return err
		}
		if dup > 0 {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO instances (id, namespace, service, scheme, host, port, metadata, revision,
			                       created_at, updated_at, registered_by)
			VALUES (?,?,?,?,?,?,?,0,?,?,?)`,
			inst.ID, inst.Namespace, inst.Service, inst.Scheme, inst.Host, inst.Port,
			encodeMap(inst.Metadata), formatTime(now), formatTime(now), actor); err != nil {
			return err
		}
		rev, err := recordChange(ctx, tx, change{
			Namespace: inst.Namespace, Entity: model.EntityInstance, Ref: inst.Ref(),
			Op: model.OpCreate, Actor: actor,
			Detail: fmt.Sprintf("登记实例 %s", inst.Addr()),
		})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE instances SET revision = ? WHERE id = ?`, rev, inst.ID)
		return err
	})
	if err != nil {
		return model.Instance{}, err
	}
	return s.GetInstance(ctx, inst.ID)
}
