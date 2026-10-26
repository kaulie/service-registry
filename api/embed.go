package openapi

import _ "embed"

// Document 是本服务对外 API 的自述契约。进程启动时把它写进自己的服务记录，
// 让目录里的端点与正在运行的路由一致。
//
//go:embed openapi.yaml
var Document []byte
