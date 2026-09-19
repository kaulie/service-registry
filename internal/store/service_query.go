package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/kaulie/service-registry/internal/model"
)

// serviceColumns 是 services 表的完整读取列（顺序与 scanService 一致）。
const serviceColumns = `namespace, name, type, app_id, os, version, owner, description, git_repo_url, department_id, department_name, tags,
  protocols, base_path, health_path, auth_schemes, docs_url, spec_url, spec, spec_format, spec_hash, revision,
  created_at, updated_at, registered_by`

const serviceSelect = `SELECT ` + serviceColumns + `,
  (SELECT COUNT(1) FROM instances i WHERE i.namespace = services.namespace AND i.service = services.name)
  FROM services`

// GetService 读取单个服务契约（含端点索引与实例数）。
func (s *Store) GetService(ctx context.Context, namespace, name string) (model.Service, error) {
	row := s.db.QueryRowContext(ctx, serviceSelect+` WHERE namespace = ? AND name = ?`, namespace, name)
	svc, err := scanService(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Service{}, ErrNotFound
	}
	if err != nil {
		return model.Service{}, err
	}
	k := serviceKey{svc.Namespace, svc.Name}
	eps, err := s.endpointsFor(ctx, []serviceKey{k})
	if err != nil {
		return model.Service{}, err
	}
	svc.API.Endpoints = eps[k]
	if svc.API.Endpoints == nil {
		svc.API.Endpoints = []model.Endpoint{}
	}
	return svc, nil
}

// ServiceSpec 返回内联规范原文与格式；没有内联规范则 ErrNotFound。
func (s *Store) ServiceSpec(ctx context.Context, namespace, name string) ([]byte, string, error) {
	var raw, format string
	err := s.db.QueryRowContext(ctx,
		`SELECT spec, spec_format FROM services WHERE namespace = ? AND name = ?`, namespace, name).
		Scan(&raw, &format)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(raw) == "" {
		return nil, "", ErrNotFound
	}
	return []byte(raw), format, nil
}

type serviceKey struct{ namespace, name string }

type rowScanner interface{ Scan(dest ...any) error }

func scanService(r rowScanner) (model.Service, error) {
	var (
		svc                  model.Service
		tags, protocols      string
		authSchemes          string
		specRaw, specFormat  string
		specHash             string
		createdAt, updatedAt string
	)
	if err := r.Scan(&svc.Namespace, &svc.Name, &svc.Type, &svc.AppID, &svc.OS, &svc.Version, &svc.Owner, &svc.Description, &svc.GitRepoURL,
		&svc.DepartmentID, &svc.DepartmentName,
		&tags, &protocols, &svc.BasePath, &svc.HealthPath, &authSchemes, &svc.API.DocsURL, &svc.API.SpecURL,
		&specRaw, &specFormat, &specHash, &svc.Revision, &createdAt, &updatedAt, &svc.RegisteredBy,
		&svc.InstanceCount); err != nil {
		return model.Service{}, err
	}
	_ = specFormat // 格式只在 /spec 端点用到，视图里不暴露
	svc.Tags = decodeStrings(tags)
	svc.API.Protocols = decodeStrings(protocols)
	svc.API.Auth = decodeAuthSchemes(authSchemes)
	svc.API.SpecHash = specHash
	svc.API.SpecBytes = len(specRaw)
	svc.API.HasSpec = strings.TrimSpace(specRaw) != ""
	svc.CreatedAt, svc.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
	return svc, nil
}

// endpointsFor 批量加载端点索引，按服务分组（避免 N+1 查询）。
func (s *Store) endpointsFor(ctx context.Context, keys []serviceKey) (map[serviceKey][]model.Endpoint, error) {
	out := map[serviceKey][]model.Endpoint{}
	if len(keys) == 0 {
		return out, nil
	}
	placeholders := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys)*2)
	for _, k := range keys {
		placeholders = append(placeholders, "(?,?)")
		args = append(args, k.namespace, k.name)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT namespace, service, method, path, summary, operation_id, tags, auth
		 FROM endpoints WHERE (namespace, service) IN (`+strings.Join(placeholders, ",")+`)
		 ORDER BY path, method`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			k             serviceKey
			ep            model.Endpoint
			tags, authStr string
		)
		if err := rows.Scan(&k.namespace, &k.name, &ep.Method, &ep.Path, &ep.Summary,
			&ep.OperationID, &tags, &authStr); err != nil {
			return nil, err
		}
		ep.Tags = decodeStrings(tags)
		ep.Auth = decodeStrings(authStr)
		out[k] = append(out[k], ep)
	}
	return out, rows.Err()
}
