package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/kaulie/service-registry/internal/idgen"
	"github.com/kaulie/service-registry/internal/model"
)

// sameMetadata 比较两份元数据（nil 与空视为相同）。
func sameMetadata(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// SyncResult 描述一次声明式整组替换的结果。
type SyncResult struct {
	Instances []model.Instance `json:"instances"`
	Created   int              `json:"created"`
	Updated   int              `json:"updated"`
	Deleted   int              `json:"deleted"`
	Unchanged int              `json:"unchanged"`
}

// SyncInstances 声明式同步：把服务的实例集合**整体对齐**到 given。
//
// 这是给 CI / 部署流水线用的入口（一次性调用，不需要任何常驻进程或心跳）：
// 传"期望的实例集合"，本方法做 diff——补齐缺失的、更新变化的、摘除多余的。
// 匹配规则：优先按 id，其次按 (scheme,host,port)。
func (s *Store) SyncInstances(ctx context.Context, namespace, service string, given []model.Instance, actor string) (SyncResult, error) {
	res := SyncResult{Instances: []model.Instance{}}
	now := time.Now().UTC()

	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := assertServiceExists(ctx, tx, namespace, service); err != nil {
			return err
		}
		current, err := listInstancesTx(ctx, tx, namespace, service)
		if err != nil {
			return err
		}
		byID := map[string]model.Instance{}
		byAddr := map[string]model.Instance{}
		for _, cur := range current {
			byID[cur.ID] = cur
			byAddr[addrKey(cur.Scheme, cur.Host, cur.Port)] = cur
		}
		seen := map[string]bool{}

		for _, want := range given {
			want.Namespace, want.Service = namespace, service
			var cur model.Instance
			var found bool
			if c, ok := byID[want.ID]; want.ID != "" && ok {
				cur, found = c, true
			} else if c, ok := byAddr[addrKey(want.Scheme, want.Host, want.Port)]; ok {
				cur, found = c, true
			}

			if !found {
				id := want.ID
				if id == "" {
					id = idgen.New(idgen.PrefixInstance)
				}
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO instances (id, namespace, service, scheme, host, port, metadata, revision,
					                       created_at, updated_at, registered_by)
					VALUES (?,?,?,?,?,?,?,0,?,?,?)`,
					id, namespace, service, want.Scheme, want.Host, want.Port,
					encodeMap(want.Metadata), formatTime(now), formatTime(now), actor); err != nil {
					return err
				}
				rev, err := recordChange(ctx, tx, change{
					Namespace: namespace, Entity: model.EntityInstance,
					Ref: namespace + "/" + service + "/" + id, Op: model.OpCreate, Actor: actor,
					Detail: fmt.Sprintf("同步登记实例 %s", want.Addr()),
				})
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE instances SET revision = ? WHERE id = ?`, rev, id); err != nil {
					return err
				}
				seen[id] = true
				res.Created++
				want.ID, want.Revision, want.RegisteredAt, want.UpdatedAt, want.RegisteredBy = id, rev, now, now, actor
				res.Instances = append(res.Instances, want)
				continue
			}

			seen[cur.ID] = true
			if cur.Scheme == want.Scheme && cur.Host == want.Host && cur.Port == want.Port &&
				sameMetadata(cur.Metadata, want.Metadata) {
				res.Unchanged++
				res.Instances = append(res.Instances, cur)
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE instances SET scheme = ?, host = ?, port = ?, metadata = ?, updated_at = ?, registered_by = ?
				WHERE id = ?`,
				want.Scheme, want.Host, want.Port, encodeMap(want.Metadata), formatTime(now), actor, cur.ID); err != nil {
				return err
			}
			rev, err := recordChange(ctx, tx, change{
				Namespace: namespace, Entity: model.EntityInstance,
				Ref: namespace + "/" + service + "/" + cur.ID, Op: model.OpUpdate, Actor: actor,
				Detail: fmt.Sprintf("同步更新实例 %s → %s", cur.Addr(), want.Addr()),
			})
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE instances SET revision = ? WHERE id = ?`, rev, cur.ID); err != nil {
				return err
			}
			res.Updated++
			res.Instances = append(res.Instances, model.Instance{
				ID: cur.ID, Namespace: namespace, Service: service,
				Scheme: want.Scheme, Host: want.Host, Port: want.Port, Metadata: want.Metadata,
				Revision: rev, RegisteredAt: cur.RegisteredAt, UpdatedAt: now, RegisteredBy: actor,
			})
		}

		// 声明式语义：集合必须完全对齐，多余的实例一律摘除。
		for _, cur := range current {
			if seen[cur.ID] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM instances WHERE id = ?`, cur.ID); err != nil {
				return err
			}
			if _, err := recordChange(ctx, tx, change{
				Namespace: namespace, Entity: model.EntityInstance,
				Ref: namespace + "/" + service + "/" + cur.ID, Op: model.OpDelete, Actor: actor,
				Detail: fmt.Sprintf("同步摘除实例 %s（期望集合中不存在）", cur.Addr()),
			}); err != nil {
				return err
			}
			res.Deleted++
		}
		return nil
	})
	if err != nil {
		return SyncResult{}, err
	}
	return res, nil
}
