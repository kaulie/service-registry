package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// 角色：谁能做什么。
//
//   - admin：REGISTRY_ADMIN_TOKEN 匹配，或**未配置 admin token** 的本机开发模式
//     （此时写接口不鉴权，actor=anonymous）；可操作任意命名空间、可管令牌。
//   - namespace：仅能写自己命名空间；不能创建/删除命名空间、不能轮换令牌。
//
// 令牌从 `Authorization: Bearer <t>` 或 `X-Registry-Token: <t>` 读取。
type role struct {
	admin     bool
	namespace string
	actor     string
}

// authorize 校验请求权限。needWrite=true 表示写接口。
//
// 顺序（每一层都有明确语义）：
//  1. 带的令牌合法 → 按 admin / namespace 记账（审计里能看出是谁改的）；
//  2. 带了令牌但**非法** → 403（不静默忽略一个错误的令牌）；
//  3. 写接口且写权限开放（REGISTRY_WRITE_AUTH=open，默认）→ 放行，actor=anonymous；
//  4. 读接口且未要求鉴权 → 放行，actor=reader；
//  5. 其余：没令牌 401、令牌不够权 403。
func (s *Server) authorize(r *http.Request, namespace string, needWrite bool) (role, int, string) {
	token := bearerToken(r)

	if token != "" {
		// admin 令牌：全权。
		if s.cfg.AdminToken != "" && constantEqual(token, s.cfg.AdminToken) {
			return role{admin: true, actor: "admin"}, 0, ""
		}
		// 命名空间令牌：只能操作自己那个命名空间。
		if namespace != "" {
			ns, tokenHash, err := s.store.GetNamespace(r.Context(), namespace)
			if err == nil && tokenHash != "" && constantEqual(hashToken(token), tokenHash) {
				return role{namespace: ns.Name, actor: "ns:" + ns.Name}, 0, ""
			}
		}
		return role{}, http.StatusForbidden, "令牌无效，或无权操作该命名空间"
	}

	if needWrite && s.cfg.WriteAuthOpen {
		// 写权限开放：连管理接口（建命名空间、轮换令牌）也放行，
		// 但 actor 记为 anonymous，审计里能看出"这是没带令牌的写入"。
		return role{admin: true, actor: "anonymous"}, 0, ""
	}
	if !needWrite && !s.cfg.ReadAuthRequired {
		return role{actor: "reader"}, 0, ""
	}
	return role{}, http.StatusUnauthorized,
		"缺少令牌：请提供 Authorization: Bearer <token> 或 X-Registry-Token"
}

// requireWrite 是写接口的统一入口：通过返回 role，否则已写响应体。
func (s *Server) requireWrite(w http.ResponseWriter, r *http.Request, namespace string) (role, bool) {
	rl, status, msg := s.authorize(r, namespace, true)
	if status != 0 {
		s.writeError(w, r, status, codeForStatus(status), msg)
		return role{}, false
	}
	return rl, true
}

// requireAdmin 用于命名空间/令牌等管理接口。
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (role, bool) {
	rl, status, msg := s.authorize(r, "", true)
	if status != 0 {
		s.writeError(w, r, status, codeForStatus(status), msg)
		return role{}, false
	}
	if !rl.admin {
		s.writeError(w, r, http.StatusForbidden, "forbidden", "该操作需要 admin 令牌")
		return role{}, false
	}
	return rl, true
}

// requireRead 用于读接口（默认开放，可由 REGISTRY_READ_AUTH=token 收紧）。
func (s *Server) requireRead(w http.ResponseWriter, r *http.Request, namespace string) (role, bool) {
	rl, status, msg := s.authorize(r, namespace, false)
	if status != 0 {
		s.writeError(w, r, status, codeForStatus(status), msg)
		return role{}, false
	}
	return rl, true
}

func codeForStatus(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusBadRequest:
		return "invalid_request"
	default:
		return "error"
	}
}

func bearerToken(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Registry-Token")); v != "" {
		return v
	}
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return ""
	}
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// constantEqual 恒定时间比较，避免令牌比较的时序侧信道。
func constantEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// hashToken 是令牌的存储形式（只存哈希，不存明文）。
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ensureNamespaceExists 在写服务/实例前确认命名空间存在，给出可操作的错误提示。
func (s *Server) ensureNamespaceExists(w http.ResponseWriter, r *http.Request, namespace string) bool {
	ok, err := s.store.NamespaceExists(r.Context(), namespace)
	if err != nil {
		s.mapStoreError(w, r, err, "命名空间")
		return false
	}
	if !ok {
		s.writeError(w, r, http.StatusNotFound, "not_found",
			"命名空间 "+namespace+" 不存在；请先 POST /v1/namespaces 创建（或由 admin 创建）")
		return false
	}
	return true
}
