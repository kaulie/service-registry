package api

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/kaulie/service-registry/internal/model"
)

// 命名/地址的校验规则集中在这里，保证"存储里的东西都是干净的"。
const (
	maxNameLen     = 63
	maxHostLen     = 253
	maxTextLen     = 4096 // description 等自由文本
	maxMetadataLen = 64   // metadata 键值对数量上限
	maxTagLen      = 64
	maxTags        = 32
	maxRepoURLLen  = 512 // gitRepoUrl（代码仓库地址）
	maxDeptIDLen   = 64  // departmentId（组织接口里的部门 ID，如 D0001）
	maxDeptNameLen = 128 // departmentName（部门名，中文也常见）
)

var (
	// 命名空间/服务名：小写字母数字与 . _ -（与 DNS label 兼容，便于将来映射到网关/域名）。
	nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$`)
	// host：域名或 IP（IPv6 允许 [::1] 形式）。
	hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$|^\[[0-9A-Fa-f:]+\]$`)
	// gitRepoUrl 允许两种常见写法：
	//   1) URL：https://github.com/org/repo(.git)、ssh://git@host:2222/org/repo.git、git://…、file://…
	//   2) scp 风格：git@github.com:org/repo.git（直接把 `git remote -v` 抄过来就能用）
	repoURLRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://\S+$`)
	repoSCPRe = regexp.MustCompile(`^[^\s@/]+@[^\s:/]+:\S+$`)
)

var allowedSchemes = map[string]bool{"http": true, "https": true, "tcp": true, "tls": true}

func validateName(kind, v string) error {
	if v == "" {
		return fmt.Errorf("%s不能为空", kind)
	}
	if !nameRe.MatchString(v) {
		return fmt.Errorf("%s不合法（只允许小写字母/数字/._-，需以字母或数字开头结尾，≤%d 字符）：%q", kind, maxNameLen, v)
	}
	return nil
}

func validateText(kind, v string) error {
	if utf8.RuneCountInString(v) > maxTextLen {
		return fmt.Errorf("%s过长（上限 %d 字符）", kind, maxTextLen)
	}
	return nil
}

// validateGitRepoURL 校验代码仓库地址（可留空）。
//
// 刻意宽松：只要求"看起来是个仓库地址"，不要求域名/仓库真的可达 ——
// 本中心只存元信息，不 clone、不抓取（和 docsUrl / specUrl 同一个原则）。
func validateGitRepoURL(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if utf8.RuneCountInString(v) > maxRepoURLLen {
		return fmt.Errorf("gitRepoUrl 过长（上限 %d 字符）", maxRepoURLLen)
	}
	if !repoURLRe.MatchString(v) && !repoSCPRe.MatchString(v) {
		return fmt.Errorf("gitRepoUrl 不合法：要仓库地址，例如 https://github.com/org/repo.git、"+
			"ssh://git@host/org/repo.git 或 git@host:org/repo.git；当前值 %q", v)
	}
	return nil
}

// validateDepartment 校验部门属性（两个字段都可留空）。
//
// 刻意宽松：不要求部门真的存在于组织接口里（那是 resolveDepartment 的事，
// 且只在组织接口可用时做），这里只保证"存进去的值是干净的"。
func validateDepartment(id, name string) error {
	if err := validateDeptField("departmentId", id, maxDeptIDLen); err != nil {
		return err
	}
	return validateDeptField("departmentName", name, maxDeptNameLen)
}

func validateDeptField(kind, v string, max int) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if utf8.RuneCountInString(v) > max {
		return fmt.Errorf("%s 过长（上限 %d 字符）", kind, max)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s 不能包含控制字符（换行/制表符等）：%q", kind, v)
		}
	}
	return nil
}

func validateTags(tags []string) error {
	if len(tags) > maxTags {
		return fmt.Errorf("标签过多（上限 %d 个）", maxTags)
	}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			return fmt.Errorf("标签不能为空字符串")
		}
		if len(t) > maxTagLen {
			return fmt.Errorf("标签过长（上限 %d 字符）：%q", maxTagLen, t)
		}
	}
	return nil
}

func validateScheme(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "http", nil
	}
	if !allowedSchemes[v] {
		return "", fmt.Errorf("不支持的 scheme：%q（可选 http/https/tcp/tls）", v)
	}
	return v, nil
}

func validateHost(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("host 不能为空")
	}
	if len(v) > maxHostLen {
		return fmt.Errorf("host 过长（上限 %d 字符）", maxHostLen)
	}
	if strings.Contains(v, "://") || strings.ContainsAny(v, "/?#") {
		return fmt.Errorf("host 只能是主机名或 IP（不要带协议/路径）：%q", v)
	}
	if !hostRe.MatchString(v) {
		return fmt.Errorf("host 不合法：%q", v)
	}
	return nil
}

func validatePort(p int) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("port 必须在 1-65535 之间：%d", p)
	}
	return nil
}

func validateMetadata(m map[string]string) error {
	if len(m) > maxMetadataLen {
		return fmt.Errorf("metadata 条目过多（上限 %d）", maxMetadataLen)
	}
	for k, v := range m {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("metadata 的键不能为空")
		}
		if len(k) > 128 || len(v) > 1024 {
			return fmt.Errorf("metadata 键/值过长（键≤128、值≤1024）：%q", k)
		}
	}
	return nil
}

// validateAuthSchemes 校验鉴权描述（纯元信息，但要有基本形状）。
func validateAuthSchemes(list []model.AuthScheme) error {
	if len(list) > 16 {
		return fmt.Errorf("authSchemes 过多（上限 16）")
	}
	for _, a := range list {
		if strings.TrimSpace(a.Scheme) == "" {
			return fmt.Errorf("authSchemes[].scheme 不能为空")
		}
		if a.In != "" && a.In != "header" && a.In != "query" && a.In != "cookie" {
			return fmt.Errorf("authSchemes[].in 只能是 header/query/cookie：%q", a.In)
		}
	}
	return nil
}

// validateProtocols 校验协议列表（http/https/grpc/tcp/... 自由取值，但有形状要求）。
func validateProtocols(list []string) error {
	if len(list) > 8 {
		return fmt.Errorf("protocols 过多（上限 8）")
	}
	for _, p := range list {
		p = strings.TrimSpace(p)
		if p == "" || len(p) > 32 {
			return fmt.Errorf("protocols 取值不合法：%q", p)
		}
	}
	return nil
}
