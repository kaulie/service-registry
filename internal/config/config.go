// Package config 从环境变量读取配置（前缀 REGISTRY_）。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 是进程的运行时配置。
type Config struct {
	// HTTPAddr 监听地址，如 "127.0.0.1:4240"。
	HTTPAddr string
	// DBPath SQLite 文件路径（":memory:" 用于测试）。
	DBPath string
	// AdminToken 管理令牌；为空则管理接口不鉴权（仅开发/本机）。
	AdminToken string
	// WriteAuthOpen 为真时**写接口默认开放**：不带令牌也能登记/维护/管理。
	//
	// 这是刻意的默认值（本机/内网先把链路跑通），代价是任何人都能往唯一的
	// 元信息真源里写数据 —— 所以启动时会有 WARN，/v1/meta 会暴露 writeAuth，
	// 面板也会显示徽标。生产请设 REGISTRY_WRITE_AUTH=token。
	//
	// 开放不等于完全不认令牌：若请求带了合法令牌，仍然按 admin / namespace 记账
	// （审计里能看出是谁改的）；带了**非法**令牌则一律 403（不静默忽略）。
	WriteAuthOpen bool
	// ReadAuthRequired 为真时读接口也要求令牌（默认为假：便于各平台拉取）。
	ReadAuthRequired bool
	// DefaultNamespace 启动时自动播种的命名空间（空则不播种）。
	DefaultNamespace string
	// ChangeRetentionDays >0 时每日清理超期变更/审计记录；0 = 永久保留。
	ChangeRetentionDays int
	// PullDefaultLimit / PullMaxLimit 增量拉取分页。
	PullDefaultLimit int
	PullMaxLimit     int
	// PullWaitMax long-poll 最长挂起时间。
	PullWaitMax time.Duration
	// SSEKeepAlive SSE 心跳注释间隔。
	SSEKeepAlive time.Duration
	// MaxSpecBytes 内联 OpenAPI 原文大小上限。
	MaxSpecBytes int
	// ShutdownTimeout 优雅退出等待时间。
	ShutdownTimeout time.Duration
}

// Load 读取环境变量并返回配置。任何非法取值都会**快速失败**，
// 避免把服务带进"看起来起来了但配置是错的"状态。
func Load() (Config, error) {
	c := Config{
		HTTPAddr:            env("REGISTRY_HTTP_ADDR", ""),
		DBPath:              env("REGISTRY_DB_PATH", "./data/registry.db"),
		AdminToken:          env("REGISTRY_ADMIN_TOKEN", ""),
		DefaultNamespace:    env("REGISTRY_DEFAULT_NS", "default"),
		ChangeRetentionDays: 0,
		PullDefaultLimit:    100,
		PullMaxLimit:        1000,
		PullWaitMax:         30 * time.Second,
		SSEKeepAlive:        20 * time.Second,
		MaxSpecBytes:        256 * 1024,
		ShutdownTimeout:     10 * time.Second,
	}

	if c.HTTPAddr == "" {
		// 平台只注入 PORT；没给 REGISTRY_HTTP_ADDR 时按它推导。
		port := env("PORT", "4240")
		c.HTTPAddr = env("REGISTRY_BIND", "127.0.0.1") + ":" + port
	}

	switch strings.ToLower(env("REGISTRY_READ_AUTH", "open")) {
	case "open", "", "none":
		c.ReadAuthRequired = false
	case "token", "required":
		c.ReadAuthRequired = true
	default:
		return c, fmt.Errorf("REGISTRY_READ_AUTH 只能是 open 或 token")
	}

	// 写接口默认开放（本机/内网先跑通链路）；生产设 REGISTRY_WRITE_AUTH=token。
	switch strings.ToLower(env("REGISTRY_WRITE_AUTH", "open")) {
	case "open", "", "none":
		c.WriteAuthOpen = true
	case "token", "required":
		c.WriteAuthOpen = false
	default:
		return c, fmt.Errorf("REGISTRY_WRITE_AUTH 只能是 open 或 token")
	}

	var err error
	if c.ChangeRetentionDays, err = envInt("REGISTRY_CHANGE_RETENTION_DAYS", c.ChangeRetentionDays); err != nil {
		return c, err
	}
	if c.PullDefaultLimit, err = envInt("REGISTRY_PULL_DEFAULT_LIMIT", c.PullDefaultLimit); err != nil {
		return c, err
	}
	if c.PullMaxLimit, err = envInt("REGISTRY_PULL_MAX_LIMIT", c.PullMaxLimit); err != nil {
		return c, err
	}
	if c.MaxSpecBytes, err = envInt("REGISTRY_MAX_SPEC_BYTES", c.MaxSpecBytes); err != nil {
		return c, err
	}
	if c.PullWaitMax, err = envDuration("REGISTRY_PULL_WAIT_MAX", c.PullWaitMax); err != nil {
		return c, err
	}
	if c.SSEKeepAlive, err = envDuration("REGISTRY_SSE_KEEPALIVE", c.SSEKeepAlive); err != nil {
		return c, err
	}
	if c.ShutdownTimeout, err = envDuration("REGISTRY_SHUTDOWN_TIMEOUT", c.ShutdownTimeout); err != nil {
		return c, err
	}
	if c.PullDefaultLimit <= 0 {
		return c, fmt.Errorf("REGISTRY_PULL_DEFAULT_LIMIT 必须为正数")
	}
	if c.PullMaxLimit < c.PullDefaultLimit {
		return c, fmt.Errorf("REGISTRY_PULL_MAX_LIMIT 不能小于 REGISTRY_PULL_DEFAULT_LIMIT")
	}
	if c.MaxSpecBytes <= 0 {
		return c, fmt.Errorf("REGISTRY_MAX_SPEC_BYTES 必须为正数")
	}
	return c, nil
}

// Addr 是监听地址的便捷别名。
func (c Config) Addr() string { return c.HTTPAddr }

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def, fmt.Errorf("%s 不是合法整数：%q", key, v)
	}
	return n, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return def, fmt.Errorf("%s 不是合法时长（如 30s）：%q", key, v)
	}
	return d, nil
}
