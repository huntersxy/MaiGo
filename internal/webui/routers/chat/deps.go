// Package chat 提供 WebUI 的本地聊天路由（/api/chat 系列）。
//
// 对齐 MaiBot src/webui/routers/chat/。
package chat

import (
	"net/http"

	auth "maibot-go/internal/webui/middleware"

	memory "maibot-go/internal/common/database"
	maisaruntime "maibot-go/internal/maisaka/runtime"
	"maibot-go/internal/platform_io"
)

// PlatformAdapterSource 提供平台适配器路由绑定与运行中驱动只读快照
type PlatformAdapterSource interface {
	AdapterRouteStatuses(routeKey platformio.RouteKey) []platformio.AdapterRouteStatus
	GetDriver(driverID string) platformio.Adapter
	AdapterDescriptors() []platformio.DriverDescriptor
}

// Deps 本地聊天路由依赖
type Deps struct {
	Memory           *memory.Manager
	Agent            *maisaruntime.Agent
	PlatformAdapters PlatformAdapterSource
	InstanceDir      string

	// RequireAuth 认证校验；由父包注入
	RequireAuth func(w http.ResponseWriter, r *http.Request) bool
	// ValidateBody 请求体校验；由父包注入
	ValidateBody func(w http.ResponseWriter, r *http.Request) bool
	// AdapterPolicy 适配器策略管理器加载器，测试可注入；为空时走全局单例
	AdapterPolicy func() (*platformio.AdapterPolicyManager, error)
	// AdminAuth 认证管理器，供测试与中间件直接校验
	AdminAuth *auth.Manager
}
