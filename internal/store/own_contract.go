package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/kaulie/service-registry/internal/apispec"
	"github.com/kaulie/service-registry/internal/model"
)

// OwnContractResult 是用自述 OpenAPI 刷新本服务契约的结果。
type OwnContractResult struct {
	Created   bool
	Updated   bool
	Endpoints int
}

// PublishOwnContract 把本进程内嵌的 OpenAPI 写成 namespace/name 这条契约的端点索引。
//
// 原文 sha256 与库里的 spec_hash 一致时不写库。服务已经存在时只替换 spec 与端点，
// 部门、owner、仓库地址、版本和实例都保持原样；health_path 为空时补成 /health。
// 服务还不存在时建一条最小契约（协议 http、health /health、mcp /mcp）。
func (s *Store) PublishOwnContract(ctx context.Context, namespace, name string, raw []byte, actor string) (OwnContractResult, error) {
	parsed, err := apispec.Parse(raw)
	if err != nil {
		return OwnContractResult{}, fmt.Errorf("解析自述契约失败：%w", err)
	}
	var existsHash string
	err = s.db.QueryRowContext(ctx,
		`SELECT spec_hash FROM services WHERE namespace = ? AND name = ?`, namespace, name).Scan(&existsHash)
	if err == sql.ErrNoRows {
		if _, _, err := s.UpsertService(ctx, ServiceInput{
			Service: model.Service{
				Namespace:   namespace,
				Name:        name,
				Type:        "service",
				Version:     parsed.Version,
				Description: parsed.Title,
				BasePath:    "/",
				HealthPath:  "/health",
				MCPPath:     model.DefaultMCPPath,
				API:         model.ServiceAPI{Protocols: []string{"http"}},
			},
			SpecRaw:    raw,
			SpecFormat: parsed.Format,
			SpecHash:   parsed.Hash,
			Endpoints:  parsed.Endpoints,
		}, actor); err != nil {
			return OwnContractResult{}, err
		}
		return OwnContractResult{Created: true, Endpoints: len(parsed.Endpoints)}, nil
	}
	if err != nil {
		return OwnContractResult{}, err
	}
	if existsHash == parsed.Hash {
		return OwnContractResult{Endpoints: len(parsed.Endpoints)}, nil
	}

	now := time.Now().UTC()
	err = s.write(ctx, func(tx *sqlTx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE services
			   SET spec = ?, spec_format = ?, spec_hash = ?, updated_at = ?, registered_by = ?,
			       health_path = CASE WHEN health_path = '' THEN '/health' ELSE health_path END
			 WHERE namespace = ? AND name = ?`,
			string(raw), parsed.Format, parsed.Hash, formatTime(now), actor, namespace, name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM endpoints WHERE namespace = ? AND service = ?`, namespace, name); err != nil {
			return err
		}
		for _, ep := range parsed.Endpoints {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO endpoints (namespace, service, method, path, summary, operation_id, tags, auth)
				VALUES (?,?,?,?,?,?,?,?)`,
				namespace, name, ep.Method, ep.Path, ep.Summary, ep.OperationID,
				encodeStrings(ep.Tags), encodeStrings(ep.Auth)); err != nil {
				return err
			}
		}
		rev, err := recordChange(ctx, tx, change{
			Namespace: namespace, Entity: model.EntityService, Ref: namespace + "/" + name,
			Op: model.OpUpdate, Actor: actor,
			Detail: fmt.Sprintf("用自述契约刷新端点：%d 个（%s）", len(parsed.Endpoints), shortHash(parsed.Hash)),
		})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE services SET revision = ? WHERE namespace = ? AND name = ?`, rev, namespace, name)
		return err
	})
	if err != nil {
		return OwnContractResult{}, err
	}
	return OwnContractResult{Updated: true, Endpoints: len(parsed.Endpoints)}, nil
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
