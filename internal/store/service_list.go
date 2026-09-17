package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/kaulie/service-registry/internal/model"
)

// ServiceFilter 是服务列表的过滤条件。
type ServiceFilter struct {
	Namespace string
	Tag       string
	Owner     string
	Protocol  string
	// Department 按**部门**过滤：departmentId 或 departmentName 命中其一即可
	// （两个字段都存了，调用方不必先知道组织接口里那个 ID 叫什么）。
	Department string
	Query      string
	Limit      int
	Offset     int
}

// ListServices 返回服务契约列表与满足条件的总数。
func (s *Store) ListServices(ctx context.Context, f ServiceFilter) ([]model.Service, int, error) {
	where, args := serviceWhere(f)

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM services `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := serviceSelect + ` ` + where + ` ORDER BY namespace, name`
	if f.Limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, f.Limit, f.Offset)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]model.Service, 0, 16)
	keys := make([]serviceKey, 0, 16)
	for rows.Next() {
		svc, err := scanService(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, svc)
		keys = append(keys, serviceKey{svc.Namespace, svc.Name})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(out) == 0 {
		return out, total, nil
	}

	eps, err := s.endpointsFor(ctx, keys)
	if err != nil {
		return nil, 0, err
	}
	for i := range out {
		k := serviceKey{out[i].Namespace, out[i].Name}
		out[i].API.Endpoints = eps[k]
		if out[i].API.Endpoints == nil {
			out[i].API.Endpoints = []model.Endpoint{}
		}
	}
	return out, total, nil
}

func serviceWhere(f ServiceFilter) (string, []any) {
	var clauses []string
	var args []any
	if f.Namespace != "" {
		clauses = append(clauses, "namespace = ?")
		args = append(args, f.Namespace)
	}
	if f.Tag != "" {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM json_each(services.tags) WHERE value = ?)")
		args = append(args, f.Tag)
	}
	if f.Owner != "" {
		clauses = append(clauses, "owner = ?")
		args = append(args, f.Owner)
	}
	if f.Protocol != "" {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM json_each(services.protocols) WHERE value = ?)")
		args = append(args, f.Protocol)
	}
	if f.Department != "" {
		clauses = append(clauses, "(department_id = ? OR department_name = ?)")
		args = append(args, f.Department, f.Department)
	}
	if f.Query != "" {
		clauses = append(clauses, "(name LIKE ? OR description LIKE ? OR owner LIKE ? OR version LIKE ? OR git_repo_url LIKE ? OR department_name LIKE ? OR department_id LIKE ?)")
		like := "%" + f.Query + "%"
		args = append(args, like, like, like, like, like, like, like)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// DeleteService 删除服务及其端点索引与实例（显式级联）。
func (s *Store) DeleteService(ctx context.Context, namespace, name, actor string) error {
	ref := namespace + "/" + name
	return s.write(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM services WHERE namespace = ? AND name = ?`, namespace, name).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		var instCount int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM instances WHERE namespace = ? AND service = ?`, namespace, name).
			Scan(&instCount); err != nil {
			return err
		}
		for _, q := range []string{
			`DELETE FROM instances WHERE namespace = ? AND service = ?`,
			`DELETE FROM endpoints WHERE namespace = ? AND service = ?`,
			`DELETE FROM services WHERE namespace = ? AND name = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, namespace, name); err != nil {
				return err
			}
		}
		_, err := recordChange(ctx, tx, change{
			Namespace: namespace, Entity: model.EntityService, Ref: ref, Op: model.OpDelete, Actor: actor,
			Detail: fmt.Sprintf("删除服务契约（连带 %d 个实例与端点索引）", instCount),
		})
		return err
	})
}
