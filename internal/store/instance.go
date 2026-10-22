package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/kaulie/service-registry/internal/idgen"
	"github.com/kaulie/service-registry/internal/model"
)

const instanceColumns = `id, namespace, service, scheme, host, port, os, arch, metadata, revision, created_at,
  updated_at, last_announced_at, registered_by`

// CreateInstance 新增一个实例声明（同地址已登记 → ErrConflict）。
func (s *Store) CreateInstance(ctx context.Context, inst model.Instance, actor string) (model.Instance, error) {
	if inst.ID == "" {
		inst.ID = idgen.New(idgen.PrefixInstance)
	}
	now := time.Now().UTC()
	err := s.write(ctx, func(tx *sqlTx) error {
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
			INSERT INTO instances (id, namespace, service, scheme, host, port, os, arch, metadata, revision,
			                       created_at, updated_at, registered_by)
			VALUES (?,?,?,?,?,?,?,?,?,0,?,?,?)`,
			inst.ID, inst.Namespace, inst.Service, inst.Scheme, inst.Host, inst.Port, inst.OS, inst.Arch,
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

// AnnounceInput 是服务上报自己这一条实例。HasArch / HasMetadata 区分「没带」和「带了空值」：
// 再次上报时，没带的字段保留库里的值，避免把部署平台写过的 arch、metadata 冲掉。
type AnnounceInput struct {
	Instance    model.Instance
	HasArch     bool
	HasMetadata bool
}

// AnnounceInstance 按 (scheme,host,port) 幂等登记这一条实例。
// 地址已在且内容没变时不推进 revision。服务契约不存在返回 ErrNotFound。
// created 表示这次是新建。
func (s *Store) AnnounceInstance(ctx context.Context, in AnnounceInput, actor string) (model.Instance, bool, error) {
	inst := in.Instance
	now := time.Now().UTC()
	var (
		out     model.Instance
		created bool
	)
	err := s.write(ctx, func(tx *sqlTx) error {
		if err := assertServiceExists(ctx, tx, inst.Namespace, inst.Service); err != nil {
			return err
		}
		existing, err := instanceByAddr(ctx, tx, inst.Namespace, inst.Service, inst.Scheme, inst.Host, inst.Port)
		if errors.Is(err, sql.ErrNoRows) {
			created = true
			inst.ID = idgen.New(idgen.PrefixInstance)
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO instances (id, namespace, service, scheme, host, port, os, arch, metadata, revision,
				                       created_at, updated_at, last_announced_at, registered_by)
				VALUES (?,?,?,?,?,?,?,?,?,0,?,?,?,?)`,
				inst.ID, inst.Namespace, inst.Service, inst.Scheme, inst.Host, inst.Port, inst.OS, inst.Arch,
				encodeMap(inst.Metadata), formatTime(now), formatTime(now), formatTime(now), actor); err != nil {
				return err
			}
			rev, err := recordChange(ctx, tx, change{
				Namespace: inst.Namespace, Entity: model.EntityInstance, Ref: inst.Ref(),
				Op: model.OpCreate, Actor: actor,
				Detail: fmt.Sprintf("服务上报实例 %s", inst.Addr()),
			})
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE instances SET revision = ? WHERE id = ?`, rev, inst.ID)
			return err
		}
		if err != nil {
			return err
		}
		next := existing
		next.OS = inst.OS
		if in.HasArch {
			next.Arch = inst.Arch
		}
		if in.HasMetadata {
			next.Metadata = inst.Metadata
		}
		if next.OS == existing.OS && next.Arch == existing.Arch && maps.Equal(next.Metadata, existing.Metadata) {
			// 内容没变也记下这次上报。不推进 revision，updated_at 仍表示内容何时变过。
			if _, err := tx.ExecContext(ctx,
				`UPDATE instances SET last_announced_at = ? WHERE id = ?`, formatTime(now), existing.ID); err != nil {
				return err
			}
			existing.LastAnnouncedAt = now
			out = existing
			return nil
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE instances SET os = ?, arch = ?, metadata = ?, updated_at = ?, last_announced_at = ?, registered_by = ?
			WHERE id = ?`,
			next.OS, next.Arch, encodeMap(next.Metadata), formatTime(now), formatTime(now), actor, existing.ID); err != nil {
			return err
		}
		rev, err := recordChange(ctx, tx, change{
			Namespace: existing.Namespace, Entity: model.EntityInstance, Ref: existing.Ref(),
			Op: model.OpUpdate, Actor: actor,
			Detail: fmt.Sprintf("服务再次上报实例 %s", existing.Addr()),
		})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE instances SET revision = ? WHERE id = ?`, rev, existing.ID)
		return err
	})
	if err != nil {
		return model.Instance{}, false, err
	}
	if out.ID != "" {
		return out, false, nil
	}
	if created {
		out, err = s.GetInstance(ctx, inst.ID)
		return out, true, err
	}
	out, err = s.instanceByAddr(ctx, inst.Namespace, inst.Service, inst.Scheme, inst.Host, inst.Port)
	return out, false, err
}

func instanceByAddr(ctx context.Context, tx *sqlTx, namespace, service, scheme, host string, port int) (model.Instance, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT `+instanceColumns+` FROM instances
		WHERE namespace = ? AND service = ? AND scheme = ? AND host = ? AND port = ?`,
		namespace, service, scheme, host, port)
	return scanInstance(row)
}

func (s *Store) instanceByAddr(ctx context.Context, namespace, service, scheme, host string, port int) (model.Instance, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+instanceColumns+` FROM instances
		WHERE namespace = ? AND service = ? AND scheme = ? AND host = ? AND port = ?`,
		namespace, service, scheme, host, port)
	inst, err := scanInstance(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Instance{}, ErrNotFound
	}
	return inst, err
}
