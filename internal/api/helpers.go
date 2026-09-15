package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kaulie/service-registry/internal/store"
)

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		s.log.Error("写响应失败", "err", err)
	}
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	if status >= 500 {
		s.metrics.Inc("registry_errors_total", map[string]string{"code": code})
	}
	s.writeJSON(w, status, map[string]any{"error": apiError{Code: code, Message: msg}})
}

// decodeJSON 解析请求体（限制大小，空体/畸形 JSON 都给出明确原因）。
func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		msg := "请求体不合法：" + err.Error()
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			msg = fmt.Sprintf("请求体过大（上限 %d 字节）", maxBodyBytes)
		case errors.Is(err, io.EOF):
			msg = "请求体为空"
		}
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return false
	}
	return true
}

// mapStoreError 把数据层错误映射为 HTTP 状态码。
func (s *Server) mapStoreError(w http.ResponseWriter, r *http.Request, err error, what string) {
	switch {
	case err == nil:
		return
	case errors.Is(err, store.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "not_found", what+"不存在")
	case errors.Is(err, store.ErrConflict):
		s.writeError(w, r, http.StatusConflict, "conflict", what+"已存在")
	default:
		s.log.Error("数据层错误", "err", err, "what", what)
		s.writeError(w, r, http.StatusInternalServerError, "internal", "内部错误："+err.Error())
	}
}

// queryInt 读取整数查询参数（非法值退回默认值，不报错）。
func queryInt(r *http.Request, key string, def int) int {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return def
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			return def
		}
	}
	return n
}

// queryInt64 读取 int64 查询参数（用于 revision 游标）。
func queryInt64(r *http.Request, key string, def int64) int64 {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return def
	}
	var n int64
	for _, c := range v {
		if c < '0' || c > '9' {
			return def
		}
		if n > 1<<62/10 {
			return def
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

func queryBool(r *http.Request, key string) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key))) {
	case "1", "true", "yes", "y":
		return true
	}
	return false
}

// waitFor 挂起等待"有新变更"，或超时/客户端断开。
func (s *Server) waitFor(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	if d > s.cfg.PullWaitMax {
		d = s.cfg.PullWaitMax
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-s.store.Hub().Wait():
	case <-timer.C:
	case <-ctx.Done():
	}
}
