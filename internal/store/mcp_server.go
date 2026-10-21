package store

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/kaulie/service-registry/internal/model"
)

// MCPNameTakenError 表示这个 MCP 服务名已经被另一个命名空间登记。
// agent 列表用名字当键，所以名字全局唯一。
type MCPNameTakenError struct {
	OwnerNamespace string
}

func (e *MCPNameTakenError) Error() string {
	return "MCP 服务名已被命名空间 " + e.OwnerNamespace + " 占用"
}

const agentMCPColumns = `name, namespace, transport, command, args, env, cwd, url, headers, revision, created_at, updated_at, registered_by`

// ListAgentMCPServers 返回 agent 要加载的 MCP 服务。namespace 为空时返回全部。
func (s *Store) ListAgentMCPServers(ctx context.Context, namespace string) ([]model.AgentMCPServer, error) {
	q := `SELECT ` + agentMCPColumns + ` FROM agent_mcp_servers`
	var args []any
	if namespace != "" {
		q += ` WHERE namespace = ?`
		args = append(args, namespace)
	}
	q += ` ORDER BY name`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AgentMCPServer{}
	for rows.Next() {
		item, err := scanAgentMCP(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GetAgentMCPServer 按全局唯一的名字读取。
func (s *Store) GetAgentMCPServer(ctx context.Context, name string) (model.AgentMCPServer, error) {
	item, err := scanAgentMCP(s.db.QueryRowContext(ctx, `SELECT `+agentMCPColumns+` FROM agent_mcp_servers WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return model.AgentMCPServer{}, ErrNotFound
	}
	return item, err
}

// UpsertAgentMCPServer 登记或更新一个 agent MCP 服务。
// 内容没变时不推进 revision。名字被别的命名空间占用时返回 *MCPNameTakenError。
// created 表示这次是新建。
func (s *Store) UpsertAgentMCPServer(ctx context.Context, in model.AgentMCPServer, actor string) (model.AgentMCPServer, bool, error) {
	now := time.Now().UTC()
	var (
		out     model.AgentMCPServer
		created bool
	)
	err := s.write(ctx, func(tx *sqlTx) error {
		existing, err := getAgentMCPTx(ctx, tx, in.Name)
		switch {
		case errors.Is(err, ErrNotFound):
			created = true
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agent_mcp_servers (name, namespace, transport, command, args, env, cwd, url, headers,
				                               revision, created_at, updated_at, registered_by)
				VALUES (?,?,?,?,?,?,?,?,?,0,?,?,?)`,
				in.Name, in.Namespace, in.Transport, in.Command, encodeStrings(in.Args), encodeMap(in.Env),
				in.Cwd, in.URL, encodeMap(in.Headers), formatTime(now), formatTime(now), actor); err != nil {
				return err
			}
		case err != nil:
			return err
		case existing.Namespace != in.Namespace:
			return &MCPNameTakenError{OwnerNamespace: existing.Namespace}
		case sameAgentMCP(existing, in):
			out = existing
			return nil
		default:
			if _, err := tx.ExecContext(ctx, `
				UPDATE agent_mcp_servers SET transport = ?, command = ?, args = ?, env = ?, cwd = ?, url = ?,
				       headers = ?, updated_at = ?, registered_by = ?
				 WHERE name = ?`,
				in.Transport, in.Command, encodeStrings(in.Args), encodeMap(in.Env), in.Cwd, in.URL,
				encodeMap(in.Headers), formatTime(now), actor, in.Name); err != nil {
				return err
			}
		}

		op := model.OpUpdate
		detail := "更新 agent MCP 服务 " + in.Name + "（" + mcpAuditDetail(in) + "）"
		if created {
			op = model.OpCreate
			detail = "登记 agent MCP 服务 " + in.Name + "（" + mcpAuditDetail(in) + "）"
		}
		rev, err := recordChange(ctx, tx, change{
			Namespace: in.Namespace, Entity: model.EntityMCPServer, Ref: in.Namespace + "/" + in.Name,
			Op: op, Actor: actor, Detail: detail,
		})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_mcp_servers SET revision = ? WHERE name = ?`, rev, in.Name)
		return err
	})
	if err != nil {
		return model.AgentMCPServer{}, false, err
	}
	if out.Name != "" {
		return out, false, nil
	}
	out, err = s.GetAgentMCPServer(ctx, in.Name)
	return out, created, err
}

// DeleteAgentMCPServer 删除某个命名空间下的 MCP 服务。名字属于别处时与不存在一样返回 ErrNotFound。
func (s *Store) DeleteAgentMCPServer(ctx context.Context, namespace, name, actor string) error {
	return s.write(ctx, func(tx *sqlTx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM agent_mcp_servers WHERE name = ? AND namespace = ?`, name, namespace)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		_, err = recordChange(ctx, tx, change{
			Namespace: namespace, Entity: model.EntityMCPServer, Ref: namespace + "/" + name,
			Op: model.OpDelete, Actor: actor, Detail: "删除 agent MCP 服务 " + name,
		})
		return err
	})
}

func getAgentMCPTx(ctx context.Context, tx *sqlTx, name string) (model.AgentMCPServer, error) {
	item, err := scanAgentMCP(tx.QueryRowContext(ctx, `SELECT `+agentMCPColumns+` FROM agent_mcp_servers WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return model.AgentMCPServer{}, ErrNotFound
	}
	return item, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanAgentMCP(sc scanner) (model.AgentMCPServer, error) {
	var (
		item                 model.AgentMCPServer
		args, env, headers   string
		createdAt, updatedAt string
	)
	if err := sc.Scan(&item.Name, &item.Namespace, &item.Transport, &item.Command, &args, &env, &item.Cwd,
		&item.URL, &headers, &item.Revision, &createdAt, &updatedAt, &item.RegisteredBy); err != nil {
		return model.AgentMCPServer{}, err
	}
	item.Args = decodeStrings(args)
	item.Env = decodeMap(env)
	item.Headers = decodeMap(headers)
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}

func sameAgentMCP(a, b model.AgentMCPServer) bool {
	return a.Transport == b.Transport && a.Command == b.Command && a.Cwd == b.Cwd && a.URL == b.URL &&
		slices.Equal(a.Args, b.Args) && maps.Equal(a.Env, b.Env) && maps.Equal(a.Headers, b.Headers)
}

// mcpAuditDetail 只写传输方式和远程地址。命令参数、环境变量和请求头可能带凭据，不进审计说明。
func mcpAuditDetail(in model.AgentMCPServer) string {
	switch in.Transport {
	case "http", "sse":
		return in.Transport + " " + in.URL
	default:
		return "stdio"
	}
}
