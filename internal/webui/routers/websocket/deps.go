// Package websocket 提供 WebUI 的 WebSocket 统一连接（/api/ws）。
//
// 对齐 MaiBot src/webui/routers/websocket/。
package websocket

import (
	memory "maibot-go/internal/common/database"
	"maibot-go/internal/core/internal_chat"
	filesvc "maibot-go/internal/files"
	maisaruntime "maibot-go/internal/maisaka/runtime"
	"maibot-go/internal/plugin_market"
	"maibot-go/internal/plugin_runtime"
	"maibot-go/internal/webui/authutil"
	auth "maibot-go/internal/webui/middleware"
	webuichat "maibot-go/internal/webui/routers/chat"
)

// chatDeps 把本包依赖投影成 chat 子包的依赖
//
// WebSocket 上的聊天消息要复用 chat 包的历史读写与媒体回读能力，
// 因此按 chat.Deps 的形状投影一次。
func (d Deps) chatDeps() webuichat.Deps {
	return webuichat.Deps{
		Memory:      d.Memory,
		Agent:       d.Agent,
		InstanceDir: d.InstanceDir,
	}
}

// Deps WebSocket 处理器依赖
//
// 只保留本包真正用到的字段。父包 routers.Deps 字段很多且含测试注入项，
// 直接传入会形成跨包私有字段依赖；改由父包在注册路由时投影成这个结构。
type Deps struct {
	Memory       *memory.Manager
	AdminAuth    *auth.Manager
	PluginHost   *pluginruntime.Host
	PluginMarket *pluginmarket.Service
	Agent        *maisaruntime.Agent
	ChatHub      *internalchat.Hub
	InstanceDir  string
	// Files 文件服务，WebSocket 上传附件写入资产库
	Files *filesvc.Service
	// SessionTokenValid 统一会话令牌判定（内存会话 + admin key + data/webui.json 自定义令牌）。
	// 必须与 /auth/check、/ws-token 用同一口径，否则会出现
	// 「页面认为已登录、WebSocket 认为未登录」的登录死循环。
	SessionTokenValid func(token string) bool
}

// sessionTokenValid 用注入的统一判定校验令牌；未注入时退回 authutil 的内置判定
func (d Deps) sessionTokenValid(token string) bool {
	if d.SessionTokenValid != nil {
		return d.SessionTokenValid(token)
	}
	return authutil.ValidSessionToken(d.AdminAuth, token)
}
