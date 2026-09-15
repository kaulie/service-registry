package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kaulie/service-registry/internal/model"
	"github.com/kaulie/service-registry/internal/store"
)

// sseMaxBatch 每次唤醒最多推送的变更条数（剩余部分下一轮继续，游标不丢）。
const sseMaxBatch = 500

// handleEvents 实时变更订阅（SSE）。
//
// 协议：
//
//	event: hello   data: {"revision":12}         连接建立时的当前游标
//	event: change  data: {"revision":13,...}     每一条变更（与 /v1/changes 同构）
//	: ping                                       保活注释（防中间层超时断连）
//
// ?since= 可指定起始游标（默认从"当前"开始，只看新变更）；
// ?namespace= / ?service= 可过滤。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	svc := strings.TrimSpace(r.URL.Query().Get("service"))
	if _, ok := s.requireRead(w, r, ns); !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, r, http.StatusInternalServerError, "internal", "该连接不支持流式推送")
		return
	}

	current, err := s.store.CurrentRevision(r.Context())
	if err != nil {
		s.mapStoreError(w, r, err, "当前 revision")
		return
	}
	last := queryInt64(r, "since", current)

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // 让 nginx 不要缓冲
	w.WriteHeader(http.StatusOK)
	s.writeSSE(w, "hello", map[string]any{"revision": last})
	flusher.Flush()

	ticker := time.NewTicker(s.cfg.SSEKeepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-s.store.Hub().Wait():
			changes, _, err := s.store.Changes(r.Context(), store.ChangeFilter{
				Since: last, Namespace: ns, Limit: sseMaxBatch,
			})
			if err != nil {
				if r.Context().Err() != nil {
					return
				}
				s.writeSSE(w, "error", map[string]any{"message": err.Error()})
				flusher.Flush()
				return
			}
			sent := 0
			for _, c := range changes {
				if svc != "" && !changeMatchesService(c, ns, svc) {
					// 游标照常前进：只过滤推送，不阻塞后续变更。
					last = c.Revision
					continue
				}
				s.writeSSE(w, "change", c)
				last = c.Revision
				sent++
			}
			if notes := maxRev(changes); notes > last {
				last = notes
			}
			if sent > 0 || len(changes) > 0 {
				flusher.Flush()
			}
		}
	}
}

func (s *Server) writeSSE(w http.ResponseWriter, event string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte(`{"error":"序列化失败"}`)
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
}

func maxRev(changes []model.Change) int64 {
	var max int64
	for _, c := range changes {
		if c.Revision > max {
			max = c.Revision
		}
	}
	return max
}

// changeMatchesService 判断一条变更是否与某个服务相关。
// 变更的 ref 形如 ns / ns/svc / ns/svc/inst_xxx。
func changeMatchesService(c model.Change, ns, svc string) bool {
	ref := c.Ref
	if ns != "" && !strings.HasPrefix(ref, ns+"/") && ref != ns {
		return false
	}
	parts := strings.Split(ref, "/")
	if len(parts) < 2 {
		return false
	}
	return parts[1] == svc
}
