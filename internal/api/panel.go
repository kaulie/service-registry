package api

import (
	"io/fs"
	"net/http"

	"github.com/kaulie/service-registry/web"
)

// panelHandler 托管独立的控制面板（原生 HTML/CSS/JS，无构建步骤、无框架）。
//
// 面板与 /v1/*、/health、/metrics 完全隔离：前端所有数据都通过公开的
// 只读接口获取，因此面板本身不需要任何特殊权限（写操作在面板里仍要令牌）。
func (s *Server) panelHandler() http.Handler {
	sub, err := fs.Sub(web.Assets, ".")
	if err != nil {
		s.log.Error("面板资源不可用", "err", err)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.writeError(w, r, http.StatusInternalServerError, "internal", "面板资源不可用："+err.Error())
		})
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /panel/ → /（由 FileServer 提供 index.html）；/panel/app.js → /app.js
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + trimPanelPrefix(r.URL.Path)
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r2)
	})
}

func trimPanelPrefix(p string) string {
	const prefix = "/panel/"
	if len(p) <= len(prefix) {
		return ""
	}
	return p[len(prefix):]
}
