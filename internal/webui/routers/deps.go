package webuiapi

import (
	"context"
	"net"
	"net/http"
	"time"

	"MaiGo/internal/common/database"
	"MaiGo/internal/common/network_security"
	"MaiGo/internal/core/internal_chat"
	"MaiGo/internal/files"
	maisaruntime "MaiGo/internal/maisaka/runtime"
	"MaiGo/internal/mcp_module"
	"MaiGo/internal/platform_io"
	"MaiGo/internal/plugin_market"
	"MaiGo/internal/plugin_runtime"
	botservices "MaiGo/internal/services"
	"MaiGo/internal/services/html_render"
	"MaiGo/internal/services/news"
	"MaiGo/internal/webui/middleware"
	"MaiGo/internal/webui/services/ai_search"
)

// PlatformAdapterSource 提供平台适配器路由绑定与运行中驱动只读快照，避免 WebUI 直接依赖具体适配器
type PlatformAdapterSource interface {
	AdapterRouteStatuses(routeKey platformio.RouteKey) []platformio.AdapterRouteStatus
	GetDriver(driverID string) platformio.Adapter
	AdapterDescriptors() []platformio.DriverDescriptor
}

// AuthRateLimiter 对齐 webui.core.rate_limiter 对 /auth/verify 的调用面；由 core 注入，避免 routers↔core 循环依赖。
type AuthRateLimiter interface {
	CheckAuthRateLimit(r *http.Request) (retryAfter int, detail string, limited bool)
	RecordFailedAttempt(r *http.Request, maxFailures, windowSeconds, blockDuration int) (blocked bool, remaining int)
	ResetFailures(r *http.Request)
}

// Deps 挂载 Dashboard API 所需的运行时依赖
type Deps struct {
	Memory    *memory.Manager
	AdminAuth *auth.Manager
	// AuthRateLimiter /auth/verify 滑动窗口与失败封禁；为空时跳过（单测可不注入）
	AuthRateLimiter  AuthRateLimiter
	PluginMarket     *pluginmarket.Service
	PluginHost       *pluginruntime.Host
	News             *news.Service
	HTMLRender       *htmlrender.Service
	MCPStore         *mcp.Store
	Agent            *maisaruntime.Agent
	ACPAgent         AgentControlService
	DesktopPet       DesktopPetControl
	DesktopPetChat   func(ctx context.Context, text string) (string, error)
	ReasoningReplay  ReasoningReplayService
	SearchService    *search.Service
	SearchRunner     search.Runner
	BotAccounts      *botservices.BotAccountService
	BotAccountOnline func(platform, accountID string) bool
	BotAccountPairs  func(context.Context) ([][2]string, error)
	// PlatformAdapters 为会话详情与适配器放行状态提供真实路由快照；为空表示未接线
	PlatformAdapters PlatformAdapterSource
	Files            *filesvc.Service
	ChatHub          *internalchat.Hub
	// Avatar 外部平台头像服务（对齐 avatar_service）；为空时外部 platform 返回 502
	Avatar       *platformio.AvatarService
	InstanceDir  string
	BuildVersion string
	// SessionSchedulerStats 返回原生会话调度器快照；nil 表示未启用。
	SessionSchedulerStats func() any
	// SessionTaskSubmitter 返回提交函数；返回 nil 表示原生会话调度器未启用。
	SessionTaskSubmitter func() func(ctx context.Context, sessionKey string, task func(context.Context) error) error
	// DatabaseDSN 可选 SQLite DSN/路径覆盖；为空时用实例 data/MaiBot.db
	DatabaseDSN string
	// EnforcePublicOutbound 返回当前 webui.enforce_public_outbound_url，每次请求读取
	EnforcePublicOutbound func() bool

	// lookupIP 是测试可注入的 DNS 解析器；为空时走默认解析器
	lookupIP func(context.Context, string) ([]net.IPAddr, error)
	// marketIconClient 是测试可注入的图标下载客户端；为空时新建客户端
	marketIconClient *http.Client

	// mediaMetadata 是测试可注入的监控媒体只读存储；为空时从 Memory 的 images 表查询
	mediaMetadata monitorMediaStore

	// statisticsSummary 是测试可注入的统计摘要加载器；为空时走 stats.GetStatisticsSummary
	statisticsSummary statisticsSummaryFunc

	// textLearningExtract 是测试可注入的长文学习服务；为空时走 learners.ExtractTextCandidates
	textLearningExtract textLearningExtractFunc

	// statisticsTaskUsage 是测试可注入的模型任务用量加载器；为空时走 services.ModelTaskUsageService
	statisticsTaskUsage statisticsTaskUsageFunc

	// statisticsDetailed 是测试可注入的详细统计加载器；为空时读取 15m 任务写出的报告快照
	statisticsDetailed statisticsDetailedFunc

	// newsFetch 是测试可注入的资讯抓取实现；为空时走 News.Fetch
	newsFetch func(context.Context, bool) (*news.Response, error)

	// clock 是测试可注入的当前时间；为空时使用 time.Now
	clock func() time.Time

	// adapterPolicy 是测试可注入的适配器策略管理器；为空时走全局 GetAdapterPolicyManager
	adapterPolicy func() (*platformio.AdapterPolicyManager, error)
}

// now 返回当前时刻，测试可注入固定时钟
func (d Deps) now() time.Time {
	if d.clock != nil {
		return d.clock()
	}
	return time.Now()
}

// enforcePublicOutbound 返回当前出站公网校验开关，未接线时保持 Python 默认开启
func (d Deps) enforcePublicOutbound() bool {
	if d.EnforcePublicOutbound != nil {
		return d.EnforcePublicOutbound()
	}
	return true
}

// networkSecurityOptions 组装公共出站校验参数；管理员配置的服务可关闭公网检查
func (d Deps) networkSecurityOptions(allowPrivate, allowFakeIP bool) networksecurity.Options {
	requirePublic := d.enforcePublicOutbound()
	if allowPrivate {
		requirePublic = false
	}
	return networksecurity.Options{
		RequirePublicNetwork: requirePublic,
		AllowFakeIP:          allowFakeIP,
		LookupIP:             d.lookupIP,
	}
}
