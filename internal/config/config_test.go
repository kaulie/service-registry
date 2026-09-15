package config

import (
	"testing"
	"time"
)

func TestLoadDefaultsAndEnvOverrides(t *testing.T) {
	// 默认值：本机 127.0.0.1:4240，读接口开放，内联 spec 上限 256KB。
	t.Setenv("REGISTRY_HTTP_ADDR", "")
	t.Setenv("PORT", "")
	t.Setenv("REGISTRY_DB_PATH", "")
	t.Setenv("REGISTRY_READ_AUTH", "")
	t.Setenv("REGISTRY_DEFAULT_NS", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("加载默认配置失败：%v", err)
	}
	if cfg.Addr() != "127.0.0.1:4240" || cfg.DBPath != "./data/registry.db" {
		t.Fatalf("默认监听/库路径错误：%q %q", cfg.Addr(), cfg.DBPath)
	}
	if cfg.ReadAuthRequired || cfg.MaxSpecBytes != 256*1024 || cfg.DefaultNamespace != "default" {
		t.Fatalf("默认值错误：%+v", cfg)
	}
	// 写接口**默认开放**（刻意的默认值，收紧用 REGISTRY_WRITE_AUTH=token）。
	if !cfg.WriteAuthOpen {
		t.Fatalf("写接口应默认开放：%+v", cfg)
	}

	// 平台只注入 PORT 时按其推导监听地址。
	t.Setenv("PORT", "9424")
	cfg, err = Load()
	if err != nil || cfg.Addr() != "127.0.0.1:9424" {
		t.Fatalf("PORT 推导失败：%q %v", cfg.Addr(), err)
	}

	// 显式地址优先于 PORT；写接口收紧为需令牌。
	t.Setenv("REGISTRY_HTTP_ADDR", "0.0.0.0:8080")
	t.Setenv("REGISTRY_READ_AUTH", "token")
	t.Setenv("REGISTRY_WRITE_AUTH", "token")
	t.Setenv("REGISTRY_PULL_WAIT_MAX", "5s")
	t.Setenv("REGISTRY_CHANGE_RETENTION_DAYS", "7")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("加载配置失败：%v", err)
	}
	if cfg.Addr() != "0.0.0.0:8080" || !cfg.ReadAuthRequired || cfg.WriteAuthOpen ||
		cfg.PullWaitMax != 5*time.Second || cfg.ChangeRetentionDays != 7 {
		t.Fatalf("覆盖值未生效：%+v", cfg)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{"读鉴权取值非法", "REGISTRY_READ_AUTH", "maybe"},
		{"写鉴权取值非法", "REGISTRY_WRITE_AUTH", "sometimes"},
		{"整数非法", "REGISTRY_PULL_MAX_LIMIT", "abc"},
		{"时长非法", "REGISTRY_PULL_WAIT_MAX", "30"},
		{"spec 上限必须为正", "REGISTRY_MAX_SPEC_BYTES", "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("REGISTRY_HTTP_ADDR", "127.0.0.1:0")
			t.Setenv(c.key, c.value)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%q 应当报错", c.key, c.value)
			}
		})
	}

	// 默认拉取上限不能大于最大上限（否则语义自相矛盾）。
	t.Setenv("REGISTRY_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("REGISTRY_PULL_DEFAULT_LIMIT", "500")
	t.Setenv("REGISTRY_PULL_MAX_LIMIT", "100")
	if _, err := Load(); err == nil {
		t.Fatal("DEFAULT_LIMIT > MAX_LIMIT 应当报错")
	}
}
