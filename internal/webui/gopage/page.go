// Package gopage 提供 Go 平台开发者设置页；Bot 与宿主共用同一份内嵌页面。
package gopage

import (
	_ "embed"
	"net/http"
)

//go:embed go_developer_page.html
var pageHTML []byte

// Handler 输出开发者设置页
func Handler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 页面内嵌在二进制里，禁止缓存可以让升级后立即看到新界面。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pageHTML)
}
