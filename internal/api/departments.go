package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/kaulie/service-registry/internal/orgdir"
)

// handleListDepartments 返回**组织接口里的部门目录**。
//
// 本中心不拥有部门数据：它只把组织服务（REGISTRY_ORG_URL）的
// `GET /api/v1/departments` 取回来（短 TTL 缓存），供面板做下拉、
// 供消费方做校验/联表（服务契约上的 departmentId 就是引用这里的 ID）。
//
// 无论组织接口是否可用都返回 200，用 available/cached/stale/error 说明数据成色：
// 面板据此显示"已同步 / 暂时不可达（用上次数据）"，而不是把整页卡在转圈上。
// `?refresh=1` 可强制出网刷新（跳过 TTL 缓存）。
func (s *Server) handleListDepartments(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireRead(w, r, ""); !ok {
		return
	}
	snap := s.org.Snapshot(r.Context(), queryBool(r, "refresh"))
	s.writeJSON(w, http.StatusOK, snap)
}

// deptResolution 是一次"部门对齐"的结果。
type deptResolution struct {
	ID       string // 对齐后的部门 ID（组织接口里的稳定标识）
	Name     string // 对齐后的部门名
	Resolved bool   // 是否在组织接口的部门目录里对上了
	Note     string // 人类可读说明（进响应与审计）
}

// resolveDepartment 把请求里的部门字段对齐到组织接口的部门目录
// （「数据从组织接口同步」的落地位置）。
//
// 规则（详见 docs/DESIGN.md「部门属性」）：
//
//  1. 两个字段都没给 → 完全不动部门，也不去问组织接口（写契约的快路径）；
//  2. 给了就查目录（TTL 缓存内不出网）：命中就把 ID/名称补成权威值
//     （部门改名后重新登记会自动纠回来）；
//  3. 目录**可用且非空**时，明确给了 departmentId 却查不到 → 400：
//     事实清楚，就该挡住手抄错的 ID（面板下拉按构造不会出错，这挡的是脚本/手写）；
//  4. 目录不可达、或目录里还没有任何部门 → 按声明值保存，只记一条提醒：
//     本中心的写路径**不能因为另一个服务挂了就失败**（组织服务是内存存储，
//     重启会清空；那时把所有带部门的登记都判失败，代价远大于收益）。
//
// departmentName 单独给（不带 ID）时始终只当"标签"，不做存在性校验。
func (s *Server) resolveDepartment(ctx context.Context, id, name string) (deptResolution, error) {
	id, name = strings.TrimSpace(id), strings.TrimSpace(name)
	if id == "" && name == "" {
		return deptResolution{}, nil
	}

	snap := s.org.Snapshot(ctx, false)
	if !snap.Enabled {
		return deptResolution{ID: id, Name: name,
			Note: "组织接口未配置（REGISTRY_ORG_URL），部门按声明值保存"}, nil
	}
	if d, ok := orgdir.Find(snap, id, name); ok {
		note := "部门已按组织接口对齐：" + d.Label()
		if snap.Stale {
			note += "（组织接口暂时不可达，用的是上次同步的数据）"
		}
		return deptResolution{ID: d.ID, Name: d.Name, Resolved: true, Note: note}, nil
	}
	if id != "" && snap.Available && len(snap.Departments) > 0 {
		return deptResolution{}, errString("组织接口（" + snap.URL + "）里没有部门 ID " + id +
			"，当前可用的部门：" + snap.LabelList() +
			"；不想被校验时可以只给 departmentName（那就只当标签存）")
	}

	reason := "组织接口不可达"
	switch {
	case !snap.Available:
		if snap.Error != "" {
			reason = "组织接口不可达（" + snap.Error + "）"
		}
	case len(snap.Departments) == 0:
		reason = "组织接口里还没有任何部门"
	}
	return deptResolution{ID: id, Name: name,
		Note: "部门未能在组织接口里对齐，按声明值保存：" + deptText(id, name) + "（" + reason + "）"}, nil
}

// deptText 拼「名称（ID）」，两者都为空时返回空串。
func deptText(id, name string) string {
	switch {
	case id != "" && name != "":
		return name + "（" + id + "）"
	case name != "":
		return name
	default:
		return id
	}
}
