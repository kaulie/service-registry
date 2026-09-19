package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/kaulie/service-registry/internal/orgdir"
	"github.com/kaulie/service-registry/internal/store"
)

// orgRef 是响应里的「这个 org_id 是谁」：组织接口里的那个部门（对上了才有名字/类型/层级）。
type orgRef struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
	// ParentID 原样带出（组织接口给了才有）：本中心不做层级加工 ——
	// 这里只是让消费方拿到目录里同一条事实，不必自己再查一次部门目录。
	ParentID string `json:"parentId,omitempty"`
	// Resolved 表示这个 ID 在组织接口的部门目录里**对上了**（权威数据可确认）。
	Resolved bool `json:"resolved"`
	// Note 是人类可读说明：为什么 resolved 是这个值（对上了/目录里没有/目录不可达…）。
	Note string `json:"note,omitempty"`
}

// orgDirectoryInfo 是「部门目录这份数据的成色」，与 GET /v1/departments 同源，
// 只是不带体积最大的 departments 列表：这个接口关心的是"能不能确认这个组织"。
type orgDirectoryInfo struct {
	Enabled   bool      `json:"enabled"`   // 是否配置了组织接口（REGISTRY_ORG_URL）
	Available bool      `json:"available"` // 本次是否真的取到了目录
	Cached    bool      `json:"cached"`    // 结果来自 TTL 缓存（未出网）
	Stale     bool      `json:"stale"`     // 组织接口不可达，用的是上次成功的数据
	URL       string    `json:"url,omitempty"`
	Path      string    `json:"path,omitempty"`
	FetchedAt time.Time `json:"fetchedAt,omitzero"`
	Error     string    `json:"error,omitempty"`
}

// handleListServicesByOrg 是「按组织（部门）查服务列表」的封装接口：
//
//	GET /v1/orgs/{orgId}/services
//
// `org_id` 就是服务契约上的 `departmentId`（组织架构服务里的部门 ID，如 D0005）。
// 为什么单开一条路径，而不是让调用方自己拼 /v1/services?department=：
//
//   - 这条路径回答的是**组织视角**的问题——「这个组织里有什么服务」：
//     响应里带上组织本身（名称/类型/父部门）与部门目录的成色，
//     调用方（看板、值班页、组织服务）不必再自己去查一次部门目录；
//   - 过滤**只认 ID**：不认名称、不做模糊匹配（ID 稳定，名称会变）。
//     要按名称/关键字找服务，仍然走 `/v1/services?department=` 或 `?q=`。
//
// 失败方向与 /v1/departments 一致：**组织接口挂了不影响这个查询**——
// 依然返回 200，用 `org.resolved` + `org.note` + `organization.*` 说明
// "没对上、原因是什么"，并把本中心按其 ID 登记的服务照常列出。
// 只有当「目录可用且非空、ID 不在目录里、且本中心也没有服务登记在它下面」
// 三个条件同时成立时才是 404：事实清楚才报不存在，
// 否则会把"组织服务挂了"说成"这个组织不存在"。
func (s *Server) handleListServicesByOrg(w http.ResponseWriter, r *http.Request) {
	orgID := strings.TrimSpace(r.PathValue("orgId"))
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if _, ok := s.requireRead(w, r, ns); !ok {
		return
	}

	limit := clampLimit(queryInt(r, "limit", defaultListLimit))
	f := store.ServiceFilter{
		OrgID:     orgID,
		Namespace: ns,
		Tag:       strings.TrimSpace(r.URL.Query().Get("tag")),
		Owner:     strings.TrimSpace(r.URL.Query().Get("owner")),
		Protocol:  strings.TrimSpace(r.URL.Query().Get("protocol")),
		Query:     strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:     limit,
		Offset:    queryInt(r, "offset", 0),
	}
	snap := s.org.Snapshot(r.Context(), queryBool(r, "refresh"))
	services, total, err := s.store.ListServices(r.Context(), f)
	if err != nil {
		s.mapStoreError(w, r, err, "组织 "+orgID+" 下的服务列表")
		return
	}

	dept, known := orgdir.Find(snap, orgID, "")
	if !known && snap.Fresh() && len(snap.Departments) > 0 && total == 0 {
		s.writeError(w, r, http.StatusNotFound, "not_found",
			"组织接口（"+snap.URL+"）里没有部门 ID "+orgID+"，本中心也没有服务登记在它下面；当前可用的部门："+snap.LabelList())
		return
	}

	ref := orgRef{ID: orgID, Resolved: known}
	if known {
		ref.Name, ref.Type, ref.ParentID = dept.Name, dept.Type, dept.ParentID
		ref.Note = "组织信息来自组织接口：" + dept.Label()
		if snap.Stale {
			ref.Note += "（组织接口暂时不可达，用的是上次同步的数据）"
		}
	} else {
		ref.Note = orgMissNote(orgID, snap, total)
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"org":          ref,
		"organization": orgDirInfo(snap),
		"services":     services,
		"total":        total,
		"limit":        limit,
		"offset":       f.Offset,
	})
}

// orgMissNote 说明「为什么没在目录里对上这个 org_id」。
// 四种情况分开讲，是因为消费方要据此决定是"报错给人看"还是"先照着用"。
func orgMissNote(orgID string, snap orgdir.Snapshot, matched int) string {
	switch {
	case !snap.Enabled:
		return "组织接口未配置（REGISTRY_ORG_URL）：org_id 是登记时声明的值，未经部门目录校验"
	case !snap.Available:
		if snap.Error != "" {
			return "组织接口暂时不可达（" + snap.Error + "）：无法确认这个组织，下面是本中心按其 ID 登记的服务"
		}
		return "组织接口暂时不可达：无法确认这个组织，下面是本中心按其 ID 登记的服务"
	case len(snap.Departments) == 0:
		return "组织接口里还没有任何部门：org_id 按登记时的声明值使用"
	default:
		return "部门 " + orgID + " 已不在组织接口的部门目录里，但本中心仍有 " + itoa(matched) +
			" 个服务登记在它下面（部门改名/撤销不会自动改写已登记的契约）"
	}
}

// orgDirInfo 摘出快照里的「数据成色」（大列表不重复带：这个接口不关心别的部门叫什么）。
func orgDirInfo(snap orgdir.Snapshot) orgDirectoryInfo {
	return orgDirectoryInfo{
		Enabled: snap.Enabled, Available: snap.Available, Cached: snap.Cached, Stale: snap.Stale,
		URL: snap.URL, Path: snap.Path, FetchedAt: snap.FetchedAt, Error: snap.Error,
	}
}
