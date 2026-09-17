// Package web 是注册中心**独立的控制面板**（原生 HTML + CSS + JS，无框架、无构建步骤）。
//
// 资源通过 go:embed 编进二进制，因此发版包里不需要额外拷贝前端文件，
// 部署时也不存在"前端资源没跟上"这种问题。
package web

import "embed"

// Assets 是面板静态资源（由 internal/api 在 /panel/ 下托管）：
//
//	index.html + app.js          —— 控制面板
//	contract.html + contract.js  —— 契约编辑页（独立页面）
//	shared.js                    —— 两页共用的工具（令牌/API/toast/部门/表单反馈）
//
//go:embed index.html styles.css shared.js app.js contract.html contract.js
var Assets embed.FS
