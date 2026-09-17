package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsAndEnvOverrides(t *testing.T) {
	// 默认值：本机 127.0.0.1:4240，读接口开放，内联 spec 上限 256KB。
	t.Setenv("REGISTRY_HTTP_ADDR", "")
	t.Setenv("SERVICE_PORT", "")
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
	if cfg.PortSource != PortSourceDefault {
		t.Fatalf("默认端口来源应为 %q，实际 %q", PortSourceDefault, cfg.PortSource)
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
	if cfg.PortSource != "PORT" {
		t.Fatalf("端口来源应为 PORT，实际 %q", cfg.PortSource)
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
	if cfg.PortSource != PortSourceAddr {
		t.Fatalf("显式地址的来源应为 %q，实际 %q", PortSourceAddr, cfg.PortSource)
	}
}

// TestLoadPortPrecedence 端口来源的优先级：REGISTRY_HTTP_ADDR > SERVICE_PORT > PORT > 默认。
// TestLoadOrganization 覆盖组织接口（部门的权威数据源）的配置解析：
// 默认指向本机组织服务，可显式指到别处，也可 off 关掉（部门只当标签）。
func TestLoadOrganization(t *testing.T) {
	t.Setenv("REGISTRY_HTTP_ADDR", "127.0.0.1:0")

	// 默认：开箱可用（本机 organization 服务的 4244）。
	t.Setenv("REGISTRY_ORG_URL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("加载默认配置失败：%v", err)
	}
	if cfg.OrgURL != DefaultOrgURL {
		t.Fatalf("默认应指向组织服务 %q，实际 %q", DefaultOrgURL, cfg.OrgURL)
	}
	if cfg.OrgTimeout <= 0 || cfg.OrgCacheTTL <= 0 {
		t.Fatalf("默认超时/缓存时长应为正数：%+v", cfg)
	}

	// 显式地址：末尾斜杠被规整掉。
	t.Setenv("REGISTRY_ORG_URL", "http://org.internal:9000/")
	t.Setenv("REGISTRY_ORG_TIMEOUT", "1500ms")
	t.Setenv("REGISTRY_ORG_CACHE_TTL", "5s")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("加载配置失败：%v", err)
	}
	if cfg.OrgURL != "http://org.internal:9000" {
		t.Fatalf("地址应去末尾斜杠，实际 %q", cfg.OrgURL)
	}
	if cfg.OrgTimeout != 1500*time.Millisecond || cfg.OrgCacheTTL != 5*time.Second {
		t.Fatalf("超时/缓存覆盖未生效：%+v", cfg)
	}

	// off / none / - / disabled：关掉这个能力（空串 = 未配置）。
	for _, off := range []string{"off", "none", "-", "disabled", "OFF"} {
		t.Setenv("REGISTRY_ORG_URL", off)
		cfg, err = Load()
		if err != nil {
			t.Fatalf("REGISTRY_ORG_URL=%q 应视为关闭：%v", off, err)
		}
		if cfg.OrgURL != "" {
			t.Fatalf("REGISTRY_ORG_URL=%q 应归一成空串，实际 %q", off, cfg.OrgURL)
		}
	}

	// 非法取值：启动即失败（不要带着用不了的地址假装跑起来）。
	for _, bad := range []string{"org.internal:4244", "127.0.0.1:4244", "ftp://org/a", "://x"} {
		t.Setenv("REGISTRY_ORG_URL", bad)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "REGISTRY_ORG_URL") {
			t.Fatalf("REGISTRY_ORG_URL=%q 应启动即失败并点名变量，实际 err=%v", bad, err)
		}
	}

	// 时长非法也要拦住：0/负数没有意义（0 秒超时等于永远不可达）。
	t.Setenv("REGISTRY_ORG_URL", "")
	t.Setenv("REGISTRY_ORG_TIMEOUT", "0")
	if _, err := Load(); err == nil {
		t.Fatal("REGISTRY_ORG_TIMEOUT=0 应当报错")
	}
	t.Setenv("REGISTRY_ORG_TIMEOUT", "")
	t.Setenv("REGISTRY_ORG_CACHE_TTL", "-1s")
	if _, err := Load(); err == nil {
		t.Fatal("REGISTRY_ORG_CACHE_TTL=-1s 应当报错")
	}
}

func TestLoadPortPrecedence(t *testing.T) {
	clear := func() {
		t.Setenv("REGISTRY_HTTP_ADDR", "")
		t.Setenv("SERVICE_PORT", "")
		t.Setenv("PORT", "")
		t.Setenv("REGISTRY_BIND", "")
	}

	// SERVICE_PORT 单独给：用它（这是本次新增的能力）。
	clear()
	t.Setenv("SERVICE_PORT", "5001")
	cfg, err := Load()
	if err != nil || cfg.Addr() != "127.0.0.1:5001" {
		t.Fatalf("SERVICE_PORT 应生效：%q %v", cfg.Addr(), err)
	}
	if cfg.PortSource != "SERVICE_PORT" {
		t.Fatalf("端口来源应为 SERVICE_PORT，实际 %q", cfg.PortSource)
	}

	// SERVICE_PORT 与 PORT 同时给：本服务自己的变量优先（宿主机上的 PORT 常是别的进程的）。
	t.Setenv("PORT", "5002")
	cfg, err = Load()
	if err != nil || cfg.Addr() != "127.0.0.1:5001" {
		t.Fatalf("SERVICE_PORT 应优先于 PORT：%q %v", cfg.Addr(), err)
	}

	// SERVICE_PORT 读不到（空串/空白）→ 回落 PORT。
	t.Setenv("SERVICE_PORT", "   ")
	cfg, err = Load()
	if err != nil || cfg.Addr() != "127.0.0.1:5002" || cfg.PortSource != "PORT" {
		t.Fatalf("SERVICE_PORT 为空应回落 PORT：%q %v（来源 %q）", cfg.Addr(), err, cfg.PortSource)
	}

	// 两个都读不到 → 默认 4240。
	t.Setenv("PORT", "")
	cfg, err = Load()
	if err != nil || cfg.Addr() != "127.0.0.1:"+DefaultPort || cfg.PortSource != PortSourceDefault {
		t.Fatalf("都没读到应回落默认端口：%q %v（来源 %q）", cfg.Addr(), err, cfg.PortSource)
	}

	// REGISTRY_BIND 只换绑定 IP，端口仍来自 SERVICE_PORT。
	t.Setenv("SERVICE_PORT", "5003")
	t.Setenv("REGISTRY_BIND", "0.0.0.0")
	cfg, err = Load()
	if err != nil || cfg.Addr() != "0.0.0.0:5003" {
		t.Fatalf("REGISTRY_BIND 应只影响 IP：%q %v", cfg.Addr(), err)
	}

	// 显式完整地址优先级最高（压过 SERVICE_PORT）。
	t.Setenv("REGISTRY_HTTP_ADDR", "127.0.0.1:5004")
	cfg, err = Load()
	if err != nil || cfg.Addr() != "127.0.0.1:5004" || cfg.PortSource != PortSourceAddr {
		t.Fatalf("REGISTRY_HTTP_ADDR 应压过 SERVICE_PORT：%q %v（来源 %q）", cfg.Addr(), err, cfg.PortSource)
	}
}

// TestLoadRejectsBadPorts 端口取值非法时**启动即失败**（不悄悄回落默认端口）。
func TestLoadRejectsBadPorts(t *testing.T) {
	cases := []struct{ name, key, value string }{
		{"SERVICE_PORT 不是数字", "SERVICE_PORT", "http"},
		{"SERVICE_PORT 为 0", "SERVICE_PORT", "0"},
		{"SERVICE_PORT 越界", "SERVICE_PORT", "70000"},
		{"SERVICE_PORT 带冒号", "SERVICE_PORT", ":5001"},
		{"PORT 不是数字", "PORT", "abc"},
		{"PORT 越界", "PORT", "-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("REGISTRY_HTTP_ADDR", "")
			t.Setenv("SERVICE_PORT", "")
			t.Setenv("PORT", "")
			t.Setenv(c.key, c.value)
			_, err := Load()
			if err == nil {
				t.Fatalf("%s=%q 应当报错", c.key, c.value)
			}
			if !strings.Contains(err.Error(), c.key) {
				t.Fatalf("错误信息应点名 %s：%v", c.key, err)
			}
		})
	}

	// REGISTRY_HTTP_ADDR 显式给了地址时，不再关心端口变量（也不校验它们）。
	t.Setenv("REGISTRY_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("SERVICE_PORT", "not-a-port")
	if _, err := Load(); err != nil {
		t.Fatalf("显式地址在时不该因端口变量报错：%v", err)
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
