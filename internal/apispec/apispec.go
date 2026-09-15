// Package apispec 解析服务声明的对外 API 契约。
//
// 两种来源（显式优先）：
//  1. endpoints：调用方直接声明端点列表；
//  2. spec：内联的 OpenAPI 3.x / Swagger 2.0 原文（YAML 或 JSON），由本包解析出端点。
//
// 本包只做**存储前的忠实提取与基本校验**，不抓取远端 URL
// （specUrl 仅作为元信息保存）。
package apispec

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kaulie/service-registry/internal/model"
)

// MaxEndpoints 限制单个服务登记的端点数，避免畸形输入把库撑爆。
const MaxEndpoints = 2000

var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// ErrUnsupported 表示原文不是本包认识的规格。
var ErrUnsupported = errors.New("不是可识别的 OpenAPI 3.x / Swagger 2.0 文档")

// Result 是解析产物。
type Result struct {
	Endpoints []model.Endpoint
	Title     string
	Version   string
	Hash      string // 原文 sha256（十六进制）
	Bytes     int
	Format    string // yaml | json
}

// Parse 解析内联规格原文（raw），提取端点。
func Parse(raw []byte) (Result, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return Result{}, ErrUnsupported
	}
	format := "yaml"
	if looksLikeJSON(raw) {
		format = "json"
	}

	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return Result{}, fmt.Errorf("规格原文无法解析（YAML/JSON）：%w", err)
	}
	if doc == nil {
		return Result{}, ErrUnsupported
	}
	if _, ok := doc["openapi"]; !ok {
		if _, ok := doc["swagger"]; !ok {
			return Result{}, ErrUnsupported
		}
	}

	res := Result{Format: format, Bytes: len(raw), Hash: hash(raw)}
	if info, ok := doc["info"].(map[string]any); ok {
		res.Title, _ = info["title"].(string)
		if v, ok := info["version"]; ok {
			res.Version = fmt.Sprint(v)
		}
	}

	paths, _ := doc["paths"].(map[string]any)
	if len(paths) == 0 {
		return res, errors.New("规格里没有 paths（没有可登记的端点）")
	}

	eps := make([]model.Endpoint, 0, 16)
	for _, p := range sortedKeys(paths) {
		if !strings.HasPrefix(p, "/") {
			return Result{}, fmt.Errorf("路径必须以 / 开头：%q", p)
		}
		item, ok := paths[p].(map[string]any)
		if !ok {
			continue
		}
		for _, m := range httpMethods {
			opAny, ok := item[m]
			if !ok {
				continue
			}
			op, _ := opAny.(map[string]any)
			ep := model.Endpoint{
				Method:      strings.ToUpper(m),
				Path:        p,
				Summary:     firstNonEmpty(str(op["summary"]), str(op["description"])),
				OperationID: str(op["operationId"]),
				Tags:        strSlice(op["tags"]),
				Auth:        securityNames(op["security"], item["security"], doc["security"]),
			}
			ep.Summary = truncate(ep.Summary, 160)
			eps = append(eps, ep)
			if len(eps) > MaxEndpoints {
				return Result{}, fmt.Errorf("端点数超过上限 %d", MaxEndpoints)
			}
		}
	}
	if len(eps) == 0 {
		return res, errors.New("规格里没有可识别的 HTTP 操作")
	}
	res.Endpoints = eps
	return res, nil
}

// Normalize 校验并规范化调用方**显式声明**的端点列表。
func Normalize(eps []model.Endpoint) ([]model.Endpoint, error) {
	if len(eps) > MaxEndpoints {
		return nil, fmt.Errorf("端点数超过上限 %d", MaxEndpoints)
	}
	out := make([]model.Endpoint, 0, len(eps))
	seen := map[string]bool{}
	for _, ep := range eps {
		ep.Method = strings.ToUpper(strings.TrimSpace(ep.Method))
		ep.Path = strings.TrimSpace(ep.Path)
		if ep.Method == "" {
			return nil, fmt.Errorf("端点缺少 method（path=%q）", ep.Path)
		}
		if !isHTTPMethod(ep.Method) {
			return nil, fmt.Errorf("不支持的 method：%q", ep.Method)
		}
		if !strings.HasPrefix(ep.Path, "/") {
			return nil, fmt.Errorf("端点路径必须以 / 开头：%q", ep.Path)
		}
		key := ep.Method + " " + ep.Path
		if seen[key] {
			return nil, fmt.Errorf("端点重复：%s", key)
		}
		seen[key] = true
		out = append(out, ep)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out, nil
}

// Hash 返回原文的 sha256（十六进制）。
func Hash(raw []byte) string { return hash(raw) }

func hash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func isHTTPMethod(m string) bool {
	for _, x := range httpMethods {
		if strings.EqualFold(x, m) {
			return true
		}
	}
	return false
}

func looksLikeJSON(raw []byte) bool {
	t := strings.TrimSpace(string(raw))
	return strings.HasPrefix(t, "{")
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func strSlice(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s := str(x); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// securityNames 汇总操作/路径/全局三个层级的 security 方案名（去重、保序）。
func securityNames(sources ...any) []string {
	var out []string
	seen := map[string]bool{}
	for _, src := range sources {
		list, ok := src.([]any)
		if !ok {
			continue
		}
		for _, entry := range list {
			m, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			for _, name := range sortedKeys(m) {
				if !seen[name] {
					seen[name] = true
					out = append(out, name)
				}
			}
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
