package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/kaulie/service-registry/internal/apispec"
	"github.com/kaulie/service-registry/internal/model"
	"github.com/kaulie/service-registry/internal/store"
)

// serviceWrite 是登记服务契约的请求体。
//
// 刻意与响应结构（model.Service）保持一致，只是 api 里多出可写的 spec/specFormat——
// 这样"GET 一个服务 → 改一改 → PUT 回去"可以闭环（便于复制/迁移服务定义）。
type serviceWrite struct {
	// Type 区分登记对象是 service（后端服务，默认）还是 app（移动/桌面应用）。
	Type        string `json:"type"`
	AppID       string `json:"appId"` // 兼容题面里的 app_id 写法（见 UnmarshalJSON）
	OS          string `json:"os"`
	Version     string `json:"version"`
	Owner       string `json:"owner"`
	Description string `json:"description"`
	GitRepoURL  string `json:"gitRepoUrl"`
	// 归属部门：DepartmentID 引用组织接口里的部门（如 D0001），DepartmentName 是展示名。
	// 给哪个都行：服务端会拿组织接口的部门目录把另一个补全（见 resolveDepartment）。
	DepartmentID   string   `json:"departmentId"`
	DepartmentName string   `json:"departmentName"`
	Tags           []string `json:"tags"`
	BasePath       string   `json:"basePath"`
	HealthPath     string   `json:"healthPath"`
	API            apiWrite `json:"api"`
}

type apiWrite struct {
	model.ServiceAPI
	// Spec 是内联的 OpenAPI 3.x / Swagger 2.0 原文（YAML 或 JSON）。
	// 提供后由服务端解析出端点索引；也可只给 api.endpoints 显式声明端点。
	Spec       string `json:"spec"`
	SpecFormat string `json:"specFormat"`
}

// UnmarshalJSON 同时接受 appId（仓库 JSON 约定）与 app_id（题面字段名），
// 二者都给时以 appId 为准。
func (b *serviceWrite) UnmarshalJSON(data []byte) error {
	type plain serviceWrite
	var aux struct {
		*plain
		AppIDSnake string `json:"app_id"`
	}
	aux.plain = (*plain)(b)
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if b.AppID == "" {
		b.AppID = aux.AppIDSnake
	}
	return nil
}

// handlePutService 登记或更新服务契约（幂等）。
// 服务的**基础属性**（对外 API + 描述性元信息）都在这里登记；实例地址另走 /instances。
func (s *Server) handlePutService(w http.ResponseWriter, r *http.Request) {
	nsName, svcName := r.PathValue("ns"), r.PathValue("svc")
	rl, ok := s.requireWrite(w, r, nsName)
	if !ok {
		return
	}
	if err := validateName("命名空间名", nsName); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateName("服务名", svcName); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.ensureNamespaceExists(w, r, nsName) {
		return
	}

	var body serviceWrite
	if !s.decodeJSON(w, r, &body) {
		return
	}
	if err := validateServiceWrite(&body, s.cfg.MaxSpecBytes); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	// 部门属性：形状先校验，再拿组织接口的部门目录对齐（命中就补全 ID/名称）。
	dept, err := s.resolveDepartment(r.Context(), body.DepartmentID, body.DepartmentName)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	body.DepartmentID, body.DepartmentName = dept.ID, dept.Name
	if dept.Note != "" {
		if dept.Resolved {
			s.log.Info("部门按组织接口对齐", "service", nsName+"/"+svcName, "department", deptText(dept.ID, dept.Name))
		} else {
			s.log.Warn("部门未能在组织接口里对齐（按声明值保存）", "service", nsName+"/"+svcName, "note", dept.Note)
		}
	}
	in, err := buildServiceInput(nsName, svcName, body)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	svc, created, err := s.store.UpsertService(r.Context(), in, rl.actor)
	if err != nil {
		s.mapStoreError(w, r, err, "服务 "+svcName)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	out := map[string]any{"service": svc, "created": created}
	// 部门对齐的说明（面板会显示出来：是"已按组织接口对齐"还是"只能按声明值存"）。
	if dept.Note != "" {
		out["departmentNote"] = dept.Note
	}
	s.writeJSON(w, status, out)
}

// buildServiceInput 把请求体转成存储入参，并解析/规范化端点索引。
func buildServiceInput(nsName, svcName string, body serviceWrite) (store.ServiceInput, error) {
	in := store.ServiceInput{}
	in.Service = model.Service{
		Namespace:      nsName,
		Name:           svcName,
		Type:           body.Type,
		AppID:          body.AppID,
		OS:             body.OS,
		Version:        strings.TrimSpace(body.Version),
		Owner:          strings.TrimSpace(body.Owner),
		Description:    body.Description,
		GitRepoURL:     strings.TrimSpace(body.GitRepoURL),
		DepartmentID:   strings.TrimSpace(body.DepartmentID),
		DepartmentName: strings.TrimSpace(body.DepartmentName),
		Tags:           trimAll(body.Tags),
		BasePath:       strings.TrimSpace(body.BasePath),
		HealthPath:     strings.TrimSpace(body.HealthPath),
	}
	in.Service.API = body.API.ServiceAPI
	in.Service.API.Protocols = trimAll(in.Service.API.Protocols)
	in.Service.API.DocsURL = strings.TrimSpace(in.Service.API.DocsURL)
	in.Service.API.SpecURL = strings.TrimSpace(in.Service.API.SpecURL)

	spec := strings.TrimSpace(body.API.Spec)
	switch {
	case spec != "":
		parsed, err := apispec.Parse([]byte(spec))
		if err != nil {
			return in, err
		}
		in.SpecRaw = []byte(spec)
		in.SpecHash = parsed.Hash
		in.SpecFormat = parsed.Format
		if f := strings.ToLower(strings.TrimSpace(body.API.SpecFormat)); f == "yaml" || f == "json" {
			in.SpecFormat = f
		}
		// 显式声明的端点优先；否则采用从规格解析出的端点。
		if len(body.API.Endpoints) > 0 {
			eps, err := apispec.Normalize(body.API.Endpoints)
			if err != nil {
				return in, err
			}
			in.Endpoints = eps
		} else {
			in.Endpoints = parsed.Endpoints
		}
	case len(body.API.Endpoints) > 0:
		eps, err := apispec.Normalize(body.API.Endpoints)
		if err != nil {
			return in, err
		}
		in.Endpoints = eps
	default:
		return in, errString("必须提供 api.spec（内联 OpenAPI 原文）或 api.endpoints（显式端点列表）——" +
			"服务的对外 API 是本注册中心的基础属性，不允许为空")
	}
	return in, nil
}

// normalizeServiceWrite 归一化并校验注册类型相关字段：
//   - type 缺省为 service；
//   - type=app 时 appId 与 os 必填，且 os 取值收敛为统一展示值；
//   - type=service 时不落 appId/os（这些字段只对 app 有意义）。
func normalizeServiceWrite(body *serviceWrite) error {
	typ, err := normalizeServiceType(body.Type)
	if err != nil {
		return err
	}
	body.Type = typ
	body.AppID = strings.TrimSpace(body.AppID)
	if typ == serviceTypeService {
		body.AppID = ""
		body.OS = ""
		return nil
	}
	if body.AppID == "" {
		return errString("type 为 app 时必须提供 appId")
	}
	if err := validateAppID(body.AppID); err != nil {
		return err
	}
	osName, err := normalizeOS(body.OS)
	if err != nil {
		return err
	}
	if osName == "" {
		return errString("type 为 app 时必须选择操作系统（os）：Android/iOS/MacOS/Windows/Linux")
	}
	body.OS = osName
	return nil
}

func validateServiceWrite(body *serviceWrite, maxSpecBytes int) error {
	if err := normalizeServiceWrite(body); err != nil {
		return err
	}
	if err := validateText("服务描述", body.Description); err != nil {
		return err
	}
	if utf8Len(body.Version) > 64 {
		return errString("version 过长（上限 64 字符）")
	}
	if utf8Len(body.Owner) > 128 {
		return errString("owner 过长（上限 128 字符）")
	}
	if err := validateGitRepoURL(body.GitRepoURL); err != nil {
		return err
	}
	if err := validateDepartment(body.DepartmentID, body.DepartmentName); err != nil {
		return err
	}
	if utf8Len(body.BasePath) > 256 {
		return errString("basePath 过长（上限 256 字符）")
	}
	if err := validateTags(body.Tags); err != nil {
		return err
	}
	if err := validateProtocols(body.API.Protocols); err != nil {
		return err
	}
	if err := validateAuthSchemes(body.API.Auth); err != nil {
		return err
	}
	if len(body.API.Spec) > maxSpecBytes {
		return errString("内联 OpenAPI 原文过大（上限 " + itoa(maxSpecBytes) + " 字节）")
	}
	if hp := strings.TrimSpace(body.HealthPath); hp != "" && !strings.HasPrefix(hp, "/") {
		return errString("healthPath 必须以 / 开头（它是元信息，供消费方/看门狗自行探活）")
	}
	return nil
}
