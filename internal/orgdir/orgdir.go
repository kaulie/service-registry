// Package orgdir 是**组织架构服务**（organization，`GET /api/v1/departments`）的
// 只读目录客户端。
//
// 为什么需要它：服务契约上新增了「部门」属性，而部门的权威数据不在本中心、
// 在组织架构服务里。本包负责把那份数据取回来，供两层使用：
//
//   - 登记契约时：把请求里的 departmentId / departmentName 对齐到权威部门
//     （数据**从组织接口同步**，而不是靠人手工抄一份）；
//   - 面板与其他消费方：`GET /v1/departments` 直接拿到部门候选列表（下拉/校验）。
//
// 语义边界（与 docs/DESIGN.md 一致）：
//
//   - 这是**按需拉取 + 短 TTL 缓存**，不是后台定时任务：本中心依然没有常驻同步器；
//   - 组织接口挂了**不影响本中心读写**：返回「上次成功的快照 + stale=true」，
//     写契约时只提示、不因为别人不可用而失败；
//   - 本包只读、不写组织服务（不建部门、不改部门）。
package orgdir

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Source 是目录数据的来源标识（出现在 /v1/departments 响应里，便于消费方判断）。
const Source = "organization"

// DefaultPath 是组织服务暴露部门列表的路径。
const DefaultPath = "/api/v1/departments"

// 默认超时与缓存时长：组织服务是本机/内网的轻量读接口，短超时 + 短缓存足够，
// 两种情况都不会把本中心的请求卡住（写契约最多多花一次 timeout）。
const (
	DefaultTimeout  = 3 * time.Second
	DefaultCacheTTL = 30 * time.Second
)

// maxBodyBytes 限制读取组织服务响应的体积（防止对端返回异常大的 body）。
const maxBodyBytes = 1 << 20

// Department 是一个部门（组织接口的字段原样映射）。
type Department struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type,omitempty"` // 研发 / 测试 / 产品 / 管理
}

// Label 返回「名称（ID）」形式的人类可读标签（错误提示与审计说明用）。
func (d Department) Label() string {
	switch {
	case d.ID != "" && d.Name != "":
		return d.Name + "（" + d.ID + "）"
	case d.Name != "":
		return d.Name
	default:
		return d.ID
	}
}

// Snapshot 是一次「部门目录」的读取结果。
//
// 它与平台其余快照的语义一致：**永远带上"这份数据的成色"**——
// 是刚取的（Available）、还是缓存/降级的（Cached / Stale），失败原因是什么（Error）。
type Snapshot struct {
	Source      string       `json:"source"`
	URL         string       `json:"url,omitempty"`
	Path        string       `json:"path,omitempty"`
	Enabled     bool         `json:"enabled"`   // 是否配置了组织接口（REGISTRY_ORG_URL）
	Available   bool         `json:"available"` // 本次是否真的从组织接口取到了数据
	Cached      bool         `json:"cached"`    // 结果来自 TTL 缓存（未出网）
	Stale       bool         `json:"stale"`     // 组织接口不可达，用的是上次成功的数据
	FetchedAt   time.Time    `json:"fetchedAt,omitzero"`
	Error       string       `json:"error,omitempty"`
	Departments []Department `json:"departments"`
	Types       []string     `json:"types,omitempty"`
}

// Fresh 表示"这份数据可以当权威用"（刚取到，或缓存还在 TTL 内，且非降级数据）。
func (s Snapshot) Fresh() bool { return s.Available && !s.Stale }

// LabelList 返回「名称（ID）」列表，用于错误提示（如"当前可用的部门：…"）。
func (s Snapshot) LabelList() string {
	parts := make([]string, 0, len(s.Departments))
	for _, d := range s.Departments {
		parts = append(parts, d.Label())
	}
	return strings.Join(parts, "、")
}

// Client 是组织接口的客户端（并发安全）。
type Client struct {
	baseURL string
	path    string
	http    *http.Client
	ttl     time.Duration
	now     func() time.Time

	mu      sync.Mutex
	current Snapshot // 最近一次结果（成功或失败），TTL 内直接复用
	attempt time.Time
	hasCur  bool
	last    Snapshot // 最近一次**成功**的快照（失败时用它降级）
	hasLast bool
}

// NewClient 构造目录客户端。baseURL 为空表示未配置（Enabled() == false）。
// timeout<=0 取 DefaultTimeout；ttl<=0 表示不做缓存（每次调用都出网，测试常用）。
func NewClient(baseURL string, timeout, ttl time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if ttl < 0 {
		ttl = 0
	}
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		path:    DefaultPath,
		http:    &http.Client{Timeout: timeout},
		ttl:     ttl,
		now:     time.Now,
	}
}

// Enabled 表示是否配置了组织接口地址。
func (c *Client) Enabled() bool { return c != nil && c.baseURL != "" }

// BaseURL 返回组织接口基地址（未配置时为空串）。
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.baseURL
}

// CacheTTL 返回缓存时长（0 = 不缓存）。
func (c *Client) CacheTTL() time.Duration {
	if c == nil {
		return 0
	}
	return c.ttl
}

// Snapshot 返回部门目录：TTL 内复用上次结果（Cached=true），force 时强制出网。
//
// 任何失败都**不返回 error**：失败信息放在 Snapshot.Available/Stale/Error 里，
// 调用方（面板 / 写契约）据此决定是提示还是降级，而不是被一个 error 挡住 ——
// 这也是"组织接口挂了不影响本中心"的实现方式。
func (c *Client) Snapshot(ctx context.Context, force bool) Snapshot {
	if !c.Enabled() {
		return Snapshot{
			Source: Source, Enabled: false, Available: false,
			Error:       "未配置组织接口地址（REGISTRY_ORG_URL）",
			Departments: []Department{},
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if !force && c.hasCur && c.ttl > 0 && c.now().Sub(c.attempt) < c.ttl {
		snap := c.current
		snap.Cached = true
		return snap
	}
	snap := c.fetch(ctx)
	c.current, c.attempt, c.hasCur = snap, c.now(), true
	return snap
}

// fetch 真正出网取一次（调用方需持有锁：它会更新"最近一次成功"）。
func (c *Client) fetch(ctx context.Context) Snapshot {
	url := c.baseURL + c.path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return c.degrade("组织接口地址不合法：" + err.Error())
	}
	req.Header.Set("Accept", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return c.degrade("请求组织接口失败：" + err.Error())
	}
	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(res.Body, maxBodyBytes))
	if err != nil {
		return c.degrade("读取组织接口响应失败：" + err.Error())
	}
	if res.StatusCode != http.StatusOK {
		return c.degrade(fmt.Sprintf("组织接口返回 HTTP %d：%s", res.StatusCode, snippet(body)))
	}

	var payload struct {
		Items []Department `json:"items"`
		Types []string     `json:"types"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return c.degrade("组织接口返回的 JSON 无法解析：" + err.Error())
	}

	deps := make([]Department, 0, len(payload.Items))
	for _, d := range payload.Items {
		d.ID = strings.TrimSpace(d.ID)
		d.Name = strings.TrimSpace(d.Name)
		d.Type = strings.TrimSpace(d.Type)
		if d.ID == "" && d.Name == "" {
			continue // 既没 ID 也没名字的条目没有意义，直接丢掉
		}
		deps = append(deps, d)
	}

	snap := Snapshot{
		Source:      Source,
		URL:         c.baseURL,
		Path:        c.path,
		Enabled:     true,
		Available:   true,
		FetchedAt:   c.now(),
		Departments: deps,
		Types:       trimAll(payload.Types),
	}
	c.last, c.hasLast = snap, true
	return snap
}

// degrade 构造"降级"结果：能拿到上次成功的数据就带上（Stale=true），否则只报错。
func (c *Client) degrade(msg string) Snapshot {
	if c.hasLast {
		stale := c.last
		stale.Available = false
		stale.Cached = true
		stale.Stale = true
		stale.Error = msg
		return stale
	}
	return Snapshot{
		Source: Source, URL: c.baseURL, Path: c.path, Enabled: true,
		Available: false, Error: msg, Departments: []Department{},
	}
}

// Find 在快照里按 ID（优先）或名称查找部门；ID/名称比较忽略大小写与首尾空白。
// 第二个返回值表示是否命中。
func Find(snap Snapshot, id, name string) (Department, bool) {
	id, name = strings.TrimSpace(id), strings.TrimSpace(name)
	if id != "" {
		for _, d := range snap.Departments {
			if strings.EqualFold(strings.TrimSpace(d.ID), id) {
				return d, true
			}
		}
	}
	if name != "" {
		for _, d := range snap.Departments {
			if strings.EqualFold(strings.TrimSpace(d.Name), name) {
				return d, true
			}
		}
	}
	return Department{}, false
}

func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// snippet 截断响应体，避免把一大段 HTML 原样塞进错误信息。
func snippet(body []byte) string {
	s := strings.Join(strings.Fields(strings.TrimSpace(string(body))), " ")
	if s == "" {
		return "(空响应)"
	}
	const max = 160
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
