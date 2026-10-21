package model

import "time"

// AgentMCPServer 是登记给 agent 加载的一个 MCP 服务。
//
// 名字在全部命名空间里唯一，因为 agent 拿到的 mcpServers 用这个名字当键。
// Transport 为 stdio / http / sse。存储形态和给 agent 的 JSON 不是同一个形状：
// ClientConfig 收成 Cursor 那份配置（stdio 不带 type，远程必须带 type）。
type AgentMCPServer struct {
	Name         string
	Namespace    string
	Transport    string
	Command      string
	Args         []string
	Env          map[string]string
	Cwd          string
	URL          string
	Headers      map[string]string
	Revision     int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	RegisteredBy string
}

// ClientConfig 是 agent 能直接放进 mcpServers 的那一项。
// stdio 不写 type；http / sse 必须写 type。空的 args / env / cwd / headers 不出现。
func (s AgentMCPServer) ClientConfig() map[string]any {
	switch s.Transport {
	case "http", "sse":
		cfg := map[string]any{
			"type": s.Transport,
			"url":  s.URL,
		}
		if len(s.Headers) > 0 {
			cfg["headers"] = s.Headers
		}
		return cfg
	default:
		cfg := map[string]any{"command": s.Command}
		if len(s.Args) > 0 {
			cfg["args"] = s.Args
		}
		if len(s.Env) > 0 {
			cfg["env"] = s.Env
		}
		if s.Cwd != "" {
			cfg["cwd"] = s.Cwd
		}
		return cfg
	}
}
