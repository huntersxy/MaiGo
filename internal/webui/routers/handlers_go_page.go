package webuiapi

import (
	_ "embed"
	"net/http"
)

//go:embed assets/go_developer_page.html
var goDeveloperPageHTML []byte

// handleGoDeveloperPage 提供 Go 平台专属开发者设置页
//
// GET /go 及 GET /go/
func (d Deps) handleGoDeveloperPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 页面内嵌在二进制里，禁止缓存可以让升级后立即看到新界面。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(goDeveloperPageHTML)
}
