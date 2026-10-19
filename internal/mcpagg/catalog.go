// Package mcpagg 把服务契约上的 mcpPath 和实例地址收成 MCP 目录，
// 并在 /mcp 上把各个服务自己的工具转发出去。
//
// 目录本身只读登记数据，不访问实例。访问发生在 agent 调用聚合入口时。
package mcpagg

import (
	"net/url"
	"sort"
	"strings"

	"github.com/kaulie/service-registry/internal/model"
)

// AggregatorPath 是注册中心对 agent 暴露的 Streamable HTTP MCP。
const AggregatorPath = "/mcp"

// CatalogPath 是只读目录接口：拼出每个服务的 MCP 地址，不访问那些地址。
const CatalogPath = "/v1/mcp/servers"

// HopHeader 由聚合入口打在转发出去的请求上。后端如果又是本入口，看到它就不再继续扇出。
const HopHeader = "X-MCP-Aggregator"

// HopValue 是 HopHeader 的值。
const HopValue = "service-registry"

// Server 是目录里的一个服务：契约上的 MCP 路径，加上每个 HTTP(S) 实例拼出的地址。
type Server struct {
	Namespace   string     `json:"namespace"`
	Service     string     `json:"service"`
	Description string     `json:"description,omitempty"`
	MCPPath     string     `json:"mcpPath"`
	Endpoints   []Endpoint `json:"endpoints"`
}

// Endpoint 是一个实例上的 MCP 地址。
type Endpoint struct {
	InstanceID string `json:"instanceId"`
	URL        string `json:"url"`
	Scheme     string `json:"scheme"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
}

// Compose 用契约和实例拼目录。tcp/tls 实例没有 HTTP MCP，不出现在 endpoints 里。
// 没有实例的服务仍留在目录中，endpoints 为空。
func Compose(services []model.Service, instances []model.Instance) []Server {
	byKey := map[string][]model.Instance{}
	for _, inst := range instances {
		byKey[inst.Namespace+"\x00"+inst.Service] = append(byKey[inst.Namespace+"\x00"+inst.Service], inst)
	}
	out := make([]Server, 0, len(services))
	for _, svc := range services {
		path := strings.TrimSpace(svc.MCPPath)
		if path == "" {
			path = model.DefaultMCPPath
		}
		item := Server{
			Namespace:   svc.Namespace,
			Service:     svc.Name,
			Description: svc.Description,
			MCPPath:     path,
			Endpoints:   []Endpoint{},
		}
		for _, inst := range byKey[svc.Namespace+"\x00"+svc.Name] {
			if ep, ok := endpointOf(inst, path); ok {
				item.Endpoints = append(item.Endpoints, ep)
			}
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Service < out[j].Service
	})
	return out
}

func endpointOf(inst model.Instance, mcpPath string) (Endpoint, bool) {
	switch inst.Scheme {
	case "http", "https":
	default:
		return Endpoint{}, false
	}
	if inst.Host == "" || !strings.HasPrefix(mcpPath, "/") {
		return Endpoint{}, false
	}
	return Endpoint{
		InstanceID: inst.ID,
		URL:        inst.Addr() + mcpPath,
		Scheme:     inst.Scheme,
		Host:       inst.Host,
		Port:       inst.Port,
	}, true
}

// ToolName 是聚合入口上的工具名：namespace__service__tool。
// 超长或含非法字符时 ok 为 false，调用方跳过该工具。
func ToolName(namespace, service, tool string) (string, bool) {
	name := namespace + "__" + service + "__" + tool
	if tool == "" || len(name) > 128 {
		return "", false
	}
	for _, r := range name {
		if !validToolRune(r) {
			return "", false
		}
	}
	return name, true
}

func validToolRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.'
}

// IsSelf 判断这条 MCP 地址是不是这次请求打到的聚合入口自己，避免 tools/list 再调回自己。
func IsSelf(requestHost, endpoint string) bool {
	requestHost = strings.TrimSpace(requestHost)
	if requestHost == "" {
		return false
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return false
	}
	if !strings.EqualFold(u.Host, requestHost) {
		return false
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	return path == AggregatorPath || path == AggregatorPath+"/"
}
