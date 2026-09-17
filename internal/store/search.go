package store

import (
	"context"
	"strings"

	"github.com/kaulie/service-registry/internal/model"
)

// Snapshot 是给其他平台**一次性拉取**的全量视图。
type Snapshot struct {
	Revision   int64             `json:"revision"`
	Namespaces []model.Namespace `json:"namespaces"`
	Services   []model.Service   `json:"services"`
	Instances  []model.Instance  `json:"instances"`
}

// Snapshot 返回全量快照（namespace 为空表示全部命名空间）。
// 实例按 (namespace, service, host, port) 排序，保证同一 revision 下输出稳定
// （ETag 才有意义）。
func (s *Store) Snapshot(ctx context.Context, namespace string) (Snapshot, error) {
	var snap Snapshot
	rev, err := s.CurrentRevision(ctx)
	if err != nil {
		return snap, err
	}
	snap.Revision = rev

	nsList, err := s.ListNamespaces(ctx)
	if err != nil {
		return snap, err
	}
	if namespace != "" {
		filtered := make([]model.Namespace, 0, 1)
		for _, ns := range nsList {
			if ns.Name == namespace {
				filtered = append(filtered, ns)
			}
		}
		nsList = filtered
	}
	snap.Namespaces = nsList

	svcs, _, err := s.ListServices(ctx, ServiceFilter{Namespace: namespace})
	if err != nil {
		return snap, err
	}
	snap.Services = svcs

	instances, err := s.listInstances(ctx, namespace)
	if err != nil {
		return snap, err
	}
	snap.Instances = instances
	return snap, nil
}

func (s *Store) listInstances(ctx context.Context, namespace string) ([]model.Instance, error) {
	q := `SELECT ` + instanceColumns + ` FROM instances`
	args := []any{}
	if namespace != "" {
		q += ` WHERE namespace = ?`
		args = append(args, namespace)
	}
	q += ` ORDER BY namespace, service, scheme, host, port`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.Instance, 0, 16)
	for rows.Next() {
		inst, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inst)
	}
	return out, rows.Err()
}

// EndpointMatch 是"按 API 找服务"的一条命中。
type EndpointMatch struct {
	Namespace      string `json:"namespace"`
	Service        string `json:"service"`
	ServiceVersion string `json:"serviceVersion,omitempty"`
	Description    string `json:"description,omitempty"`
	GitRepoURL     string `json:"gitRepoUrl,omitempty"`
	// 部门跟着命中一起返回（数据来自组织接口登记的部门属性）：找接口时顺手就知道归属哪个部门。
	DepartmentID   string         `json:"departmentId,omitempty"`
	DepartmentName string         `json:"departmentName,omitempty"`
	InstanceCount  int            `json:"instanceCount"`
	MatchType      string         `json:"matchType"` // exact | template | glob
	Endpoint       model.Endpoint `json:"endpoint"`
}

// EndpointFilter 是 API 检索条件。
type EndpointFilter struct {
	Method    string // 空 = 不限
	Path      string // 具体路径、模板路径或含 * / ** 的模式
	Match     string // 匹配模式：空=自动（精确→模板→通配），或 exact|template|glob
	Namespace string
	Tag       string
	// Department 按归属部门过滤（departmentId 或 departmentName 命中其一即可）。
	Department string
	Query      string // 额外关键字：匹配服务名/描述/端点摘要
	Limit      int
	Offset     int
}

// maxSearchCandidates 限制单次检索扫描的端点数（本地注册中心规模下足够；
// 超出时按 path 前缀缩小范围，见 searchWhere）。
const maxSearchCandidates = 20000

// SearchEndpoints 实现"这个接口谁提供"：
// 依次尝试 精确匹配 → 模板匹配（/v1/x/{id} 命中查询 /v1/x/abc）→ 通配匹配（查询含 *）。
func (s *Store) SearchEndpoints(ctx context.Context, f EndpointFilter) ([]EndpointMatch, bool, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	where, args := endpointWhere(f)
	args = append(args, maxSearchCandidates)
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.namespace, e.service, e.method, e.path, e.summary, e.operation_id, e.tags, e.auth,
		       COALESCE(sv.version, ''), COALESCE(sv.description, ''), COALESCE(sv.git_repo_url, ''),
		       COALESCE(sv.department_id, ''), COALESCE(sv.department_name, ''),
		       (SELECT COUNT(1) FROM instances i WHERE i.namespace = e.namespace AND i.service = e.service)
		FROM endpoints e LEFT JOIN services sv ON sv.namespace = e.namespace AND sv.name = e.service
		`+where+`
		ORDER BY e.namespace, e.service, e.path, e.method
		LIMIT ?`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	matcher := newEndpointMatcher(f.Match, f.Path)
	matches := make([]EndpointMatch, 0, 8)
	for rows.Next() {
		var (
			m             EndpointMatch
			tags, authStr string
		)
		if err := rows.Scan(&m.Namespace, &m.Service, &m.Endpoint.Method, &m.Endpoint.Path,
			&m.Endpoint.Summary, &m.Endpoint.OperationID, &tags, &authStr,
			&m.ServiceVersion, &m.Description, &m.GitRepoURL,
			&m.DepartmentID, &m.DepartmentName, &m.InstanceCount); err != nil {
			return nil, false, err
		}
		m.Endpoint.Tags = decodeStrings(tags)
		m.Endpoint.Auth = decodeStrings(authStr)
		if f.Tag != "" && !contains(m.Endpoint.Tags, f.Tag) {
			continue
		}
		if f.Query != "" && !matchQuery(f.Query, m.Service, m.Description, m.GitRepoURL,
			m.DepartmentName, m.DepartmentID, m.Endpoint.Summary, m.Endpoint.Path) {
			continue
		}
		ok, kind := matcher.match(m.Endpoint.Path)
		if !ok {
			continue
		}
		m.MatchType = kind
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	// 排序：精确 → 模板 → 通配，同类内按命中路径长度降序（更具体的在前）。
	rank := map[string]int{"exact": 0, "template": 1, "glob": 2}
	sortEndpointMatches(matches, rank)

	total := len(matches)
	start := f.Offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	return matches[start:end], end < total, nil
}

func endpointWhere(f EndpointFilter) (string, []any) {
	clauses := []string{"1=1"}
	args := []any{}
	if f.Method != "" {
		clauses = append(clauses, "e.method = ?")
		args = append(args, strings.ToUpper(f.Method))
	}
	if f.Namespace != "" {
		clauses = append(clauses, "e.namespace = ?")
		args = append(args, f.Namespace)
	}
	if f.Department != "" {
		// 部门属性在 services 表上（LEFT JOIN），按 ID 或名称命中其一即可。
		clauses = append(clauses, "(sv.department_id = ? OR sv.department_name = ?)")
		args = append(args, f.Department, f.Department)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}
