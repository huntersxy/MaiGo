// Package plugin 提供 WebUI 的插件管理路由（/api/webui/plugins 系列）。
//
// 对齐 MaiBot src/webui/routers/plugin/。
package plugin

import (
	"context"
	"maibot-go/internal/webui/httputil"
	"net"
	"net/http"

	appversion "maibot-go/internal/common/version"
	"strings"

	auth "maibot-go/internal/webui/middleware"

	"maibot-go/internal/common/network_security"

	"maibot-go/internal/plugin_market"
	"maibot-go/internal/plugin_runtime"
)

// Deps 插件管理路由依赖
//
// 只保留本包用到的字段；父包 routers.Deps 字段多且含测试注入项，
// 由父包在挂载路由时投影成这个结构。
type Deps struct {
	PluginMarket *pluginmarket.Service
	PluginHost   *pluginruntime.Host
	InstanceDir  string

	// BuildVersion 构建版本号，供插件运行时信息展示
	BuildVersion string
	// AdminAuth 认证管理器，供测试与中间件直接校验
	AdminAuth *auth.Manager

	// RequireAuth 认证校验；由父包注入，避免把 cookie/token 逻辑复制一份。
	// 返回 false 时已写出 401 响应，调用方直接返回。
	RequireAuth func(w http.ResponseWriter, r *http.Request) bool
	// ValidateBody 请求体校验；由父包注入
	ValidateBody func(w http.ResponseWriter, r *http.Request) bool
	// NetworkSecurityOptions 组装公共出站校验参数；由父包注入
	NetworkSecurityOptions func(allowPrivate, allowFakeIP bool) networksecurity.Options
	// MarketIconClient 图标下载客户端，测试可注入；为空时新建
	MarketIconClient *http.Client
	// LookupIP 测试可注入的 DNS 解析器；为空时走默认解析器
	LookupIP func(ctx context.Context, host string) ([]net.IPAddr, error)
	// EnforcePublicOutbound 返回出站公网校验开关
	EnforcePublicOutbound func() bool
}

// buildVersion 返回构建版本
//
// 对齐父包 version_source.go：显式注入优先，未注入时回退到编译期版本，
// 否则 /plugins/version 会返回 unknown，前端兼容性判断拿不到真实版本。
func (d Deps) buildVersion() string {
	if v := strings.TrimSpace(d.BuildVersion); v != "" && !appversion.IsUnknown(v) {
		return v
	}
	return appversion.String()
}

// enforcePublicOutbound 返回出站公网校验开关，未接线时保持 Python 默认开启
func (d Deps) enforcePublicOutbound() bool {
	if d.EnforcePublicOutbound != nil {
		return d.EnforcePublicOutbound()
	}
	return true
}

// networkSecurityOptions 组装公共出站校验参数
func (d Deps) networkSecurityOptions(allowPrivate, allowFakeIP bool) networksecurity.Options {
	if d.NetworkSecurityOptions != nil {
		return d.NetworkSecurityOptions(allowPrivate, allowFakeIP)
	}
	return networksecurity.Options{
		RequirePublicNetwork:          d.enforcePublicOutbound(),
		AllowConfiguredPrivateNetwork: allowPrivate,
		AllowFakeIP:                   allowFakeIP,
		LookupIP:                      d.LookupIP,
	}
}

// marketIconClientOrDefault 返回图标下载客户端
func (d Deps) marketIconClientOrDefault() *http.Client {
	return d.MarketIconClient
}

// requireAuth 认证校验的 nil 安全包装
//
// RequireAuth 由父包注入；未接线时一律拒绝（不伪造通过），
// 避免 nil 调用 panic 把 401 变成 500。
func (d Deps) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if d.RequireAuth == nil {
		httputil.WriteJSON(w, http.StatusUnauthorized, map[string]any{"detail": "Token 无效或已过期"})
		return false
	}
	return d.RequireAuth(w, r)
}
