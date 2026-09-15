package store

import (
	"context"
	"fmt"
	"time"

	"github.com/kaulie/service-registry/internal/model"
)

// ChangeFilter 是变更/审计查询的过滤条件。
type ChangeFilter struct {
	Since     int64  // 只返回 revision > Since
	Namespace string // 空 = 全部命名空间
	Entity    string
	Ref       string
	Op        string
	Limit     int
}

// Changes 返回按 revision 升序的变更记录；hasMore 表示还有更多。
// 每个实体的增删改都会产生一条记录，因此返回的切片本身就是
// "自 since 以来到底发生了什么"的完整答案。
func (s *Store) Changes(ctx context.Context, f ChangeFilter) ([]model.Change, bool, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	where := "WHERE revision > ?"
	args := []any{f.Since}
	if f.Namespace != "" {
		where += " AND namespace = ?"
		args = append(args, f.Namespace)
	}
	if f.Entity != "" {
		where += " AND entity = ?"
		args = append(args, f.Entity)
	}
	if f.Ref != "" {
		where += " AND ref = ?"
		args = append(args, f.Ref)
	}
	if f.Op != "" {
		where += " AND op = ?"
		args = append(args, f.Op)
	}

	// 多取一条用于判断 hasMore，避免额外的 COUNT 查询。
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx,
		`SELECT revision, at, namespace, entity, ref, op, actor, detail FROM changes `+where+
			` ORDER BY revision LIMIT ?`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	out := make([]model.Change, 0, limit)
	for rows.Next() {
		var (
			c  model.Change
			at string
		)
		if err := rows.Scan(&c.Revision, &at, &c.Namespace, &c.Entity, &c.Ref, &c.Op, &c.Actor, &c.Detail); err != nil {
			return nil, false, err
		}
		c.At = parseTime(at)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := false
	if len(out) > limit {
		hasMore = true
		out = out[:limit]
	}
	return out, hasMore, nil
}

// CurrentRevision 返回当前全局 revision（无任何变更时为 0）。
func (s *Store) CurrentRevision(ctx context.Context) (int64, error) {
	var rev int64
	// COALESCE 处理空表（MAX 返回 NULL）。
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision), 0) FROM changes`).Scan(&rev); err != nil {
		return 0, err
	}
	return rev, nil
}

// PruneChanges 删除 olderThan 之前的变更/审计记录，返回删除条数。
// 注意：这会同时清掉**审计**记录，仅在明确配置了保留天数时调用。
func (s *Store) PruneChanges(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM changes WHERE at < ?`, formatTime(olderThan))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Counts 是用于指标/概览的计数。
type Counts struct {
	Namespaces int `json:"namespaces"`
	Services   int `json:"services"`
	Instances  int `json:"instances"`
	Endpoints  int `json:"endpoints"`
	Changes    int `json:"changes"`
}

// Counts 统计各类对象数量。
func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(1) FROM namespaces),
		       (SELECT COUNT(1) FROM services),
		       (SELECT COUNT(1) FROM instances),
		       (SELECT COUNT(1) FROM endpoints),
		       (SELECT COUNT(1) FROM changes)`).
		Scan(&c.Namespaces, &c.Services, &c.Instances, &c.Endpoints, &c.Changes)
	if err != nil {
		return c, fmt.Errorf("统计失败：%w", err)
	}
	return c, nil
}
