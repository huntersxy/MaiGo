package plugin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// AuthMiddleware 插件路由认证中间件：只有版本与 Git 状态无需登录
func (d Deps) AuthMiddleware(next http.Handler) http.Handler {
	return d.pluginAuthMiddleware(next)
}

// SetupProgress 把市场安装进度接到 WebSocket 广播；未接市场服务时保持 idle
func SetupProgress(d Deps) {
	setupPluginProgress(d)
}

// Mount 挂载 /plugins 全部路由（含认证中间件与市场路由）
//
// 与父包 router.go 的挂载顺序保持一致：中间件先装，再注册市场路由。
func Mount(pr chi.Router, d Deps) {
	pr.Use(d.pluginAuthMiddleware)
	mountPluginMarketRoutes(pr, d)
}

// ResetProgressForTest 重置插件进度中心，仅供测试隔离用
func ResetProgressForTest() {
	defaultPluginProgressHub = newPluginProgressHub()
}
