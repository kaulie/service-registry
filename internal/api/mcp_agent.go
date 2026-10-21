package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/kaulie/service-registry/internal/model"
	"github.com/kaulie/service-registry/internal/store"
)

// agent 要加载的 MCP 配置的尺寸上限。请求体本身还有 1MB 的硬顶。
const (
	maxMCPCommandLen = 1024
	maxMCPArgLen     = 2048
	maxMCPArgs       = 32
	maxMCPEnv        = 32
	maxMCPEnvKeyLen  = 128
	maxMCPEnvValLen  = 4096
	maxMCPCwdLen     = 2048
	maxMCPURLLen     = 2048
	maxMCPHeaders    = 32
	maxMCPHeaderLen  = 128
	maxMCPHeaderVal  = 4096
)

var (
	envKeyRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerNameRe = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
)

// agentMCPBody 是登记时的请求体，形状和 agent 拿到的那一项相同。
type agentMCPBody struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// handleListAgentMCPServers 返回 agent 要加载的 MCP 列表。
// 响应就是 {"mcpServers": {名字: 配置}}，可以直接交给 Cursor / agent。
// 本接口只读登记数据：不启动 stdio 进程，也不请求 http/sse 地址。
func (s *Server) handleListAgentMCPServers(w http.ResponseWriter, r *http.Request) {
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if _, ok := s.requireRead(w, r, ns); !ok {
		return
	}
	if ns != "" {
		if err := validateName("命名空间名", ns); err != nil {
			s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	list, err := s.store.ListAgentMCPServers(r.Context(), ns)
	if err != nil {
		s.mapStoreError(w, r, err, "agent MCP 列表")
		return
	}
	servers := map[string]any{}
	for _, item := range list {
		servers[item.Name] = item.ClientConfig()
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"mcpServers": servers})
}

// handlePutAgentMCPServer 登记或更新一个 agent MCP 服务（幂等）。
func (s *Server) handlePutAgentMCPServer(w http.ResponseWriter, r *http.Request) {
	nsName := r.PathValue("ns")
	name := r.PathValue("name")
	rl, ok := s.requireWrite(w, r, nsName)
	if !ok {
		return
	}
	if err := validateName("命名空间名", nsName); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateName("MCP 服务名", name); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.ensureNamespaceExists(w, r, nsName) {
		return
	}
	body, ok := s.decodeAgentMCP(w, r)
	if !ok {
		return
	}
	cfg, err := normalizeAgentMCP(body)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	cfg.Name = name
	cfg.Namespace = nsName
	saved, created, err := s.store.UpsertAgentMCPServer(r.Context(), cfg, rl.actor)
	if err != nil {
		var taken *store.MCPNameTakenError
		if errors.As(err, &taken) {
			s.writeError(w, r, http.StatusConflict, "conflict",
				fmt.Sprintf("MCP 服务名 %s 已在命名空间 %s 登记；agent 列表用这个名字当键，所以全局唯一", name, taken.OwnerNamespace))
			return
		}
		s.mapStoreError(w, r, err, "MCP 服务 "+name)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	s.writeJSON(w, status, map[string]any{
		"namespace": saved.Namespace,
		"name":      saved.Name,
		"mcpServer": saved.ClientConfig(),
	})
}

// handleDeleteAgentMCPServer 从某个命名空间摘掉一个 agent MCP 服务。
func (s *Server) handleDeleteAgentMCPServer(w http.ResponseWriter, r *http.Request) {
	nsName := r.PathValue("ns")
	name := r.PathValue("name")
	rl, ok := s.requireWrite(w, r, nsName)
	if !ok {
		return
	}
	if err := validateName("命名空间名", nsName); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateName("MCP 服务名", name); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.store.DeleteAgentMCPServer(r.Context(), nsName, name, rl.actor); err != nil {
		s.mapStoreError(w, r, err, "MCP 服务 "+name)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) decodeAgentMCP(w http.ResponseWriter, r *http.Request) (agentMCPBody, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var body agentMCPBody
	if err := dec.Decode(&body); err != nil {
		msg := "请求体不合法：" + err.Error()
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			msg = fmt.Sprintf("请求体过大（上限 %d 字节）", maxBodyBytes)
		case errors.Is(err, io.EOF):
			msg = "请求体为空"
		case strings.Contains(err.Error(), "unknown field"):
			msg = "请求体有无法识别的字段（本地进程用 command/args/env/cwd，远程用 type/url/headers）：" + err.Error()
		}
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return agentMCPBody{}, false
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", "请求体里有多余内容")
		return agentMCPBody{}, false
	}
	return body, true
}

// normalizeAgentMCP 把请求体收成存储形态。
// 本地进程：type 省略或 stdio，必须有 command。远程：type 为 http 或 sse，必须有 url。
// 两种字段不能混在一条里，避免登记进去的配置和 agent 真正拿到的不一样。
func normalizeAgentMCP(in agentMCPBody) (model.AgentMCPServer, error) {
	transport := strings.ToLower(strings.TrimSpace(in.Type))
	hasCommand := strings.TrimSpace(in.Command) != "" || len(in.Args) > 0 || len(in.Env) > 0 || strings.TrimSpace(in.Cwd) != ""
	hasRemote := strings.TrimSpace(in.URL) != "" || len(in.Headers) > 0
	switch transport {
	case "", "stdio":
		if hasRemote {
			return model.AgentMCPServer{}, errors.New("本地进程（stdio）不要带 url 或 headers；远程 MCP 请把 type 设为 http 或 sse")
		}
		return normalizeStdioMCP(in)
	case "http", "sse":
		if hasCommand {
			return model.AgentMCPServer{}, fmt.Errorf("type 为 %s 时不要带 command/args/env/cwd", transport)
		}
		return normalizeRemoteMCP(transport, in)
	default:
		return model.AgentMCPServer{}, fmt.Errorf("type 只能是 stdio、http 或 sse：%q", in.Type)
	}
}

func normalizeStdioMCP(in agentMCPBody) (model.AgentMCPServer, error) {
	command := strings.TrimSpace(in.Command)
	if err := validatePlain("command", command, maxMCPCommandLen, false); err != nil {
		return model.AgentMCPServer{}, err
	}
	args, err := normalizeArgs(in.Args)
	if err != nil {
		return model.AgentMCPServer{}, err
	}
	env, err := normalizeEnv(in.Env)
	if err != nil {
		return model.AgentMCPServer{}, err
	}
	cwd := strings.TrimSpace(in.Cwd)
	if err := validatePlain("cwd", cwd, maxMCPCwdLen, true); err != nil {
		return model.AgentMCPServer{}, err
	}
	return model.AgentMCPServer{
		Transport: "stdio",
		Command:   command,
		Args:      args,
		Env:       env,
		Cwd:       cwd,
	}, nil
}

func normalizeRemoteMCP(transport string, in agentMCPBody) (model.AgentMCPServer, error) {
	rawURL, err := validateMCPURL(in.URL)
	if err != nil {
		return model.AgentMCPServer{}, err
	}
	headers, err := normalizeHeaders(in.Headers)
	if err != nil {
		return model.AgentMCPServer{}, err
	}
	return model.AgentMCPServer{
		Transport: transport,
		URL:       rawURL,
		Headers:   headers,
	}, nil
}

func normalizeArgs(args []string) ([]string, error) {
	if len(args) > maxMCPArgs {
		return nil, fmt.Errorf("args 过多（上限 %d 个）", maxMCPArgs)
	}
	if len(args) == 0 {
		return nil, nil
	}
	out := make([]string, len(args))
	for i, arg := range args {
		if err := validatePlain(fmt.Sprintf("args[%d]", i), arg, maxMCPArgLen, false); err != nil {
			return nil, err
		}
		out[i] = arg
	}
	return out, nil
}

func normalizeEnv(env map[string]string) (map[string]string, error) {
	if len(env) > maxMCPEnv {
		return nil, fmt.Errorf("env 过多（上限 %d 项）", maxMCPEnv)
	}
	if len(env) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		key := strings.TrimSpace(k)
		if utf8.RuneCountInString(key) > maxMCPEnvKeyLen || !envKeyRe.MatchString(key) {
			return nil, fmt.Errorf("env 的键不合法（大写或小写字母、数字、下划线，且不能以数字开头）：%q", k)
		}
		if err := validatePlain("env."+key, v, maxMCPEnvValLen, true); err != nil {
			return nil, err
		}
		out[key] = v
	}
	return out, nil
}

func normalizeHeaders(headers map[string]string) (map[string]string, error) {
	if len(headers) > maxMCPHeaders {
		return nil, fmt.Errorf("headers 过多（上限 %d 项）", maxMCPHeaders)
	}
	if len(headers) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		key := strings.TrimSpace(k)
		if utf8.RuneCountInString(key) > maxMCPHeaderLen || !headerNameRe.MatchString(key) {
			return nil, fmt.Errorf("headers 的名字不合法（只允许字母、数字和连字符）：%q", k)
		}
		if err := validatePlain("headers."+key, v, maxMCPHeaderVal, true); err != nil {
			return nil, err
		}
		out[key] = v
	}
	return out, nil
}

func validateMCPURL(v string) (string, error) {
	v = strings.TrimSpace(v)
	if err := validatePlain("url", v, maxMCPURLLen, false); err != nil {
		return "", err
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("url 必须是不带用户信息、不带 # 片段的 http 或 https 地址：%q", v)
	}
	return v, nil
}

func validatePlain(kind, v string, max int, allowEmpty bool) error {
	if v == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("%s不能为空", kind)
	}
	if utf8.RuneCountInString(v) > max {
		return fmt.Errorf("%s过长（上限 %d 字符）", kind, max)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s不能包含控制字符", kind)
		}
	}
	return nil
}
