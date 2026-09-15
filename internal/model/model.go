// Package model 定义服务注册中心的数据模型。
//
// 语义边界（重要）：本中心是**元信息的存储中心**，不是可用性系统。
// 它只记录"谁登记了什么"，不探活、不心跳、不启停任何进程，
// 因此实例是否存在/可达由**消费方自行校验**（契约里的 healthPath 只是元信息字段）。
package model

import "time"

// 变更实体类型。
const (
	EntityNamespace = "namespace"
	EntityService   = "service"
	EntityInstance  = "instance"
)

// 变更操作类型。
const (
	OpCreate = "create"
	OpUpdate = "update"
	OpDelete = "delete"
)

// Instance 是服务的一个运行时端点声明（无心跳、无状态位）。
type Instance struct {
	ID           string            `json:"id"`
	Namespace    string            `json:"namespace"`
	Service      string            `json:"service"`
	Scheme       string            `json:"scheme"` // http | https | tcp
	Host         string            `json:"host"`
	Port         int               `json:"port"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Revision     int64             `json:"revision,omitempty"`
	RegisteredAt time.Time         `json:"registeredAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
	RegisteredBy string            `json:"registeredBy,omitempty"`
}

// Addr 返回 "scheme://host:port"（便于调用方直接使用）。
func (i Instance) Addr() string {
	return SchemeHost(i.Scheme, i.Host, i.Port)
}

// Ref 返回稳定引用 "namespace/service/instanceID"。
func (i Instance) Ref() string {
	return i.Namespace + "/" + i.Service + "/" + i.ID
}

// BaseURL 返回不带路径的基地址。
func (i Instance) BaseURL() string { return i.Addr() }

// SchemeHost 拼装 "scheme://host:port"，port<=0 时省略端口。
func SchemeHost(scheme, host string, port int) string {
	if scheme == "" {
		scheme = "http"
	}
	if port <= 0 {
		return scheme + "://" + host
	}
	return scheme + "://" + host + ":" + itoa(port)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// AuthScheme 描述服务对外 API 的鉴权方式（元信息，仅用于展示与检索）。
type AuthScheme struct {
	Scheme string `json:"scheme"`          // bearer | api-key | basic | hmac | none
	In     string `json:"in,omitempty"`    // header | query | cookie
	Name   string `json:"name,omitempty"`  // 头/参数名，如 Authorization / X-API-Key
	Notes  string `json:"notes,omitempty"` // 自由说明
}

// Endpoint 是服务对外 API 的一个端点（**服务的基础属性**）。
type Endpoint struct {
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Summary     string   `json:"summary,omitempty"`
	OperationID string   `json:"operationId,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Auth        []string `json:"auth,omitempty"` // 引用的 securityScheme 名字
}

// ServiceAPI 是服务的对外 API 描述。spec 原文不在此结构中返回
// （体积大且通常不必要），用 specHash/hasSpec/specBytes 表示，原文走
// GET /v1/namespaces/{ns}/services/{svc}/spec。
type ServiceAPI struct {
	Protocols []string     `json:"protocols,omitempty"`
	Auth      []AuthScheme `json:"authSchemes,omitempty"`
	DocsURL   string       `json:"docsUrl,omitempty"`
	SpecURL   string       `json:"specUrl,omitempty"`
	SpecHash  string       `json:"specHash,omitempty"`
	SpecBytes int          `json:"specBytes,omitempty"`
	HasSpec   bool         `json:"hasSpec"`
	Endpoints []Endpoint   `json:"endpoints"`
}

// Service 是服务契约（低频变更，发版时更新）。
type Service struct {
	Namespace    string     `json:"namespace"`
	Name         string     `json:"name"`
	Version      string     `json:"version,omitempty"`
	Owner        string     `json:"owner,omitempty"`
	Description  string     `json:"description,omitempty"`
	Tags         []string   `json:"tags,omitempty"`
	BasePath     string     `json:"basePath,omitempty"`
	HealthPath   string     `json:"healthPath,omitempty"` // 元信息：消费方/看门狗可据此自行探活
	API          ServiceAPI `json:"api"`
	Revision     int64      `json:"revision,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	RegisteredBy string     `json:"registeredBy,omitempty"`

	// InstanceCount 仅在列表/详情查询时填充（不落库）。
	InstanceCount int `json:"instanceCount"`
}

// Ref 返回稳定引用 "namespace/service"。
func (s Service) Ref() string { return s.Namespace + "/" + s.Name }

// Namespace 是注册隔离域。TokenHash 永不出库到 API 层。
type Namespace struct {
	Name          string    `json:"name"`
	Description   string    `json:"description,omitempty"`
	TokenSet      bool      `json:"tokenSet"`
	ServiceCount  int       `json:"serviceCount"`
	InstanceCount int       `json:"instanceCount"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Change 是一条全局递增的变更记录，同时充当**增量拉取游标**与**审计日志**。
type Change struct {
	Revision  int64     `json:"revision"`
	At        time.Time `json:"at"`
	Namespace string    `json:"namespace"`
	Entity    string    `json:"entity"` // namespace | service | instance
	Ref       string    `json:"ref"`    // ns | ns/svc | ns/svc/inst_xxx
	Op        string    `json:"op"`     // create | update | delete | sync
	Actor     string    `json:"actor,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}
