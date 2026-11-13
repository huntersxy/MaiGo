package webuiapi

// WebSocket 令牌工具的薄封装。
//
// 实现位于 webui/authutil（父子包共用，避免循环引用）；
// 这里保留 Deps 方法形态。

import (
	"net/http"

	"maibot-go/internal/webui/authutil"
)

// webUISessionToken 从请求 Cookie 中取 WebUI 会话令牌
func (d Deps) webUISessionToken(r *http.Request) string {
	return authutil.SessionToken(r)
}

// validSessionToken 校验会话令牌是否有效
func (d Deps) validSessionToken(token string) bool {
	return authutil.ValidSessionToken(d.AdminAuth, token)
}
