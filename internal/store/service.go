package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/kaulie/service-registry/internal/model"
)

// ServiceInput 是登记/更新服务契约的入参（已由上层校验与解析）。
type ServiceInput struct {
	Service    model.Service
	SpecRaw    []byte // 内联 OpenAPI 原文（可空）
	SpecFormat string // yaml | json
	SpecHash   string
	Endpoints  []model.Endpoint
}

// UpsertService 登记或更新服务契约（幂等），并**整体替换**其端点索引。
// 返回服务视图与"是否新建"。
func (s *Store) UpsertService(ctx context.Context, in ServiceInput, actor string) (model.Service, bool, error) {
	svc := in.Service
	now := time.Now().UTC()
	created := false

	err := s.write(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM services WHERE namespace = ? AND name = ?`, svc.Namespace, svc.Name).
			Scan(&exists); err != nil {
			return err
		}
		created = exists == 0

		if created {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO services (namespace, name, version, owner, description, git_repo_url, tags, protocols,
				                      base_path, health_path, auth_schemes, docs_url, spec_url,
				                      spec, spec_format, spec_hash, revision, created_at, updated_at, registered_by)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?,?,?)`,
				svc.Namespace, svc.Name, svc.Version, svc.Owner, svc.Description, svc.GitRepoURL,
				encodeStrings(svc.Tags), encodeStrings(svc.API.Protocols), svc.BasePath, svc.HealthPath,
				encodeAuthSchemes(svc.API.Auth), svc.API.DocsURL, svc.API.SpecURL,
				string(in.SpecRaw), in.SpecFormat, in.SpecHash,
				formatTime(now), formatTime(now), actor); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `
				UPDATE services SET version = ?, owner = ?, description = ?, git_repo_url = ?, tags = ?, protocols = ?,
				       base_path = ?, health_path = ?, auth_schemes = ?, docs_url = ?, spec_url = ?,
				       spec = ?, spec_format = ?, spec_hash = ?, updated_at = ?, registered_by = ?
				 WHERE namespace = ? AND name = ?`,
				svc.Version, svc.Owner, svc.Description, svc.GitRepoURL,
				encodeStrings(svc.Tags), encodeStrings(svc.API.Protocols),
				svc.BasePath, svc.HealthPath, encodeAuthSchemes(svc.API.Auth), svc.API.DocsURL, svc.API.SpecURL,
				string(in.SpecRaw), in.SpecFormat, in.SpecHash, formatTime(now), actor,
				svc.Namespace, svc.Name); err != nil {
				return err
			}
		}

		// 契约是"一次登记即全量"，端点索引整体替换，避免陈旧端点残留。
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM endpoints WHERE namespace = ? AND service = ?`, svc.Namespace, svc.Name); err != nil {
			return err
		}
		for _, ep := range in.Endpoints {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO endpoints (namespace, service, method, path, summary, operation_id, tags, auth)
				VALUES (?,?,?,?,?,?,?,?)`,
				svc.Namespace, svc.Name, ep.Method, ep.Path, ep.Summary, ep.OperationID,
				encodeStrings(ep.Tags), encodeStrings(ep.Auth)); err != nil {
				return err
			}
		}

		op := model.OpUpdate
		if created {
			op = model.OpCreate
		}
		rev, err := recordChange(ctx, tx, change{
			Namespace: svc.Namespace, Entity: model.EntityService, Ref: svc.Ref(), Op: op, Actor: actor,
			Detail: fmt.Sprintf("登记服务契约：%d 个端点%s", len(in.Endpoints), specNote(in)),
		})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE services SET revision = ? WHERE namespace = ? AND name = ?`,
			rev, svc.Namespace, svc.Name); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return model.Service{}, false, err
	}
	out, err := s.GetService(ctx, svc.Namespace, svc.Name)
	if err != nil {
		return model.Service{}, created, err
	}
	return out, created, nil
}

func specNote(in ServiceInput) string {
	if len(in.SpecRaw) == 0 {
		return "（端点由调用方显式声明）"
	}
	short := in.SpecHash
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("（含内联 OpenAPI 规范 %s，%d 字节）", short, len(in.SpecRaw))
}
