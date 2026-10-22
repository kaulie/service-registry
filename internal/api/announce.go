package api

import (
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/kaulie/service-registry/internal/model"
	"github.com/kaulie/service-registry/internal/store"
)

// handleAnnounceInstance 让服务上报自己正在监听的地址。
//
// 路径上是「我是谁」（命名空间 + 服务名），请求体里至少是端口。
// host 省略时用这次 TCP 连接的对端地址，这样本机进程不用自己猜对外主机名。
// 同一个 (scheme,host,port) 再报一次是刷新：内容没变不推进 revision，也不摘掉别的实例。
func (s *Server) handleAnnounceInstance(w http.ResponseWriter, r *http.Request) {
	nsName, svcName := r.PathValue("ns"), r.PathValue("svc")
	rl, ok := s.requireWrite(w, r, nsName)
	if !ok {
		return
	}
	if err := validateName("命名空间名", nsName); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateName("服务名", svcName); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.ensureNamespaceExists(w, r, nsName) {
		return
	}

	var body struct {
		Host     string            `json:"host"`
		Port     int               `json:"port"`
		Scheme   string            `json:"scheme"`
		OS       string            `json:"os"`
		Arch     *string           `json:"arch"`
		Metadata map[string]string `json:"metadata"`
	}
	if !s.decodeJSON(w, r, &body) {
		return
	}
	scheme, err := validateScheme(body.Scheme)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	host := strings.TrimSpace(body.Host)
	if host == "" {
		host = peerHost(r)
	}
	if err := validateHost(host); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validatePort(body.Port); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	osName, err := normalizeMachineOS(body.OS, true)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var arch string
	if body.Arch != nil {
		arch, err = normalizeMachineArch(*body.Arch, false)
		if err != nil {
			s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	if body.Metadata != nil {
		if err := validateMetadata(body.Metadata); err != nil {
			s.writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}

	saved, created, err := s.store.AnnounceInstance(r.Context(), store.AnnounceInput{
		Instance: model.Instance{
			Namespace: nsName,
			Service:   svcName,
			Scheme:    scheme,
			Host:      host,
			Port:      body.Port,
			OS:        osName,
			Arch:      arch,
			Metadata:  body.Metadata,
		},
		HasArch:     body.Arch != nil,
		HasMetadata: body.Metadata != nil,
	}, rl.actor)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "not_found",
				"服务 "+nsName+"/"+svcName+" 尚未登记契约；请先 PUT /v1/namespaces/"+nsName+"/services/"+svcName)
			return
		}
		s.mapStoreError(w, r, err, "实例")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	s.writeJSON(w, status, map[string]any{"instance": saved, "created": created})
}

// peerHost 取出请求对端的主机名，IPv6 收成注册中心接受的 [addr] 形式。
func peerHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	host = strings.Trim(host, "[]")
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}
