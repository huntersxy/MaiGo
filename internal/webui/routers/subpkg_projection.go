package webuiapi

// 父包与各路由子包之间的依赖投影与薄转发。
//
// 子包的 Deps 只声明本域真正需要的字段，父包 Deps 字段更多且含测试注入项，
// 因此统一在这里投影，避免子包反向依赖父包（会造成循环引用）。

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"maibot-go/internal/webui/httputil"
	webuichat "maibot-go/internal/webui/routers/chat"
	webuiemoji "maibot-go/internal/webui/routers/emoji"
	webuiplugin "maibot-go/internal/webui/routers/plugin"
	webuiws "maibot-go/internal/webui/routers/websocket"
)

// websocketDeps 把父包 Deps 投影成 websocket 子包的依赖
func (d Deps) websocketDeps() webuiws.Deps {
	return webuiws.Deps{
		Memory:            d.Memory,
		AdminAuth:         d.AdminAuth,
		PluginHost:        d.PluginHost,
		PluginMarket:      d.PluginMarket,
		Agent:             d.Agent,
		ChatHub:           d.ChatHub,
		InstanceDir:       d.InstanceDir,
		Files:             d.Files,
		SessionTokenValid: d.sessionTokenValid,
	}
}

// pluginDeps 把父包 Deps 投影成 plugin 子包的依赖
func (d Deps) pluginDeps() webuiplugin.Deps {
	return webuiplugin.Deps{
		PluginMarket:           d.PluginMarket,
		PluginHost:             d.PluginHost,
		InstanceDir:            d.InstanceDir,
		BuildVersion:           d.BuildVersion,
		RequireAuth:            d.requireAuth,
		ValidateBody:           validateRequestBodyForRoute,
		EnforcePublicOutbound:  d.enforcePublicOutbound,
		NetworkSecurityOptions: d.networkSecurityOptions,
		MarketIconClient:       d.marketIconClient,
	}
}

// handleWSLogs 转发到 websocket 子包
func (d Deps) handleWSLogs(w http.ResponseWriter, r *http.Request) {
	d.websocketDeps().HandleWSLogs(w, r)
}

// handleWSUnified 转发到 websocket 子包
func (d Deps) handleWSUnified(w http.ResponseWriter, r *http.Request) {
	d.websocketDeps().HandleWSUnified(w, r)
}

// pluginAuthMiddleware 转发到 plugin 子包
func (d Deps) pluginAuthMiddleware(next http.Handler) http.Handler {
	return d.pluginDeps().AuthMiddleware(next)
}

// mountChatRoutes 转发到 chat 子包
func (d Deps) mountChatRoutesCompat(cr chi.Router) {
	webuichat.Mount(cr, d.chatDeps())
}

// mountPluginMarketRoutesCompat 转发到 plugin 子包
func (d Deps) mountPluginMarketRoutesCompat(pr chi.Router) {
	webuiplugin.MountMarket(pr, d.pluginDeps())
}

// setupPluginProgress 把市场安装进度接到 WebSocket 广播
func (d Deps) setupPluginProgress() {
	webuiplugin.SetupProgress(d.pluginDeps())
}

// emojiDeps 把父包 Deps 投影成 emoji 子包的依赖
func (d Deps) emojiDeps() webuiemoji.Deps {
	return webuiemoji.Deps{
		InstanceDir: d.InstanceDir,
		RequireAuth: d.requireAuth,
		AdminAuth:   d.AdminAuth,
	}
}

// mountEmojiRoutes 转发到 emoji 子包
func (d Deps) mountEmojiRoutes(er chi.Router) {
	webuiemoji.Mount(er, d.emojiDeps())
}

// mountUserEmojiRoutes 转发到 emoji 子包
func (d Deps) mountUserEmojiRoutes(ur chi.Router) {
	webuiemoji.MountUser(ur, d.emojiDeps())
}

// handleUserEmojisRoot 处理无尾斜杠的 /user-emojis，按方法分发
func (d Deps) handleUserEmojisRoot(w http.ResponseWriter, r *http.Request) {
	deps := d.emojiDeps()
	switch r.Method {
	case http.MethodGet:
		deps.HandleUserEmojisList(w, r)
	case http.MethodPost:
		deps.HandleUserEmojisAdd(w, r)
	default:
		httputil.WriteJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "Method Not Allowed"})
	}
}
