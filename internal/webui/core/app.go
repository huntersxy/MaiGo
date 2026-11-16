package app

import (
	httpservice "MaiGo/internal/common/http_service"
	"context"
	"fmt"
	"net/http"
	neturl "net/url"
	"os"
	"strings"
	"sync"
	"time"

	"MaiGo/internal/common/database"
	"MaiGo/internal/common/version"
	"MaiGo/internal/config"
	"MaiGo/internal/config/model_profiles"
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
	"MaiGo/internal/webui/dashboardstatic"
	"MaiGo/internal/webui/middleware"
	"MaiGo/internal/webui/routers"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"
)

type App struct {
	boot             config.Bootstrap
	auth             *auth.Manager
	memMgr           *memory.Manager
	maiAgent         *maisaruntime.Agent
	modelStore       *modelprofiles.Store
	mcpStore         *mcp.Store
	files            *filesvc.Service
	newsSvc          *news.Service
	htmlRenderSvc    *htmlrender.Service
	chatHub          *internalchat.Hub
	pluginMarket     *pluginmarket.Service
	pluginHost       *pluginruntime.Host
	antiCrawler      *antiCrawlerMiddleware
	acpAgent         webuiapi.AgentControlService
	desktopPet       webuiapi.DesktopPetControl
	desktopPetChat   func(context.Context, string) (string, error)
	botAccounts      *botservices.BotAccountService
	botOnline        func(platform, accountID string) bool
	botAccountPairs  func(context.Context) ([][2]string, error)
	platformAdapters webuiapi.PlatformAdapterSource
	avatar           *platformio.AvatarService
	router           http.Handler

	enforceMu             sync.RWMutex
	enforcePublicOutbound func() bool

	schedulerMu           sync.RWMutex
	sessionSchedulerStats func() any
	sessionTaskSubmitter  func(context.Context, string, func(context.Context) error) error
}

func New(boot config.Bootstrap, memMgr *memory.Manager, maiAgent *maisaruntime.Agent, models *modelprofiles.Store, mcpServers *mcp.Store, files *filesvc.Service, newsSvc *news.Service, htmlRenderSvc *htmlrender.Service, chatHub *internalchat.Hub, pluginHost *pluginruntime.Host, acpAgent webuiapi.AgentControlService, desktopPet webuiapi.DesktopPetControl, desktopPetChat func(context.Context, string) (string, error), botAccounts *botservices.BotAccountService, botOnline func(platform, accountID string) bool, botAccountPairs func(context.Context) ([][2]string, error), platformAdapters webuiapi.PlatformAdapterSource, avatar *platformio.AvatarService) *App {
	instanceDir := config.InstanceRoot()
	securityConfig, err := loadAntiCrawlerConfig(instanceDir)
	if err != nil {
		// main.go 的 New 调用没有错误返回值，坏 TOML 用 panic 拒绝启动，避免静默降级
		panic(err)
	}
	// 鉴权令牌即实例 data/webui.json 的 access_token，对齐 Python TokenManager
	authManager := auth.NewManager(func(token string) bool {
		return webuiapi.VerifyAccessToken(instanceDir, token)
	})
	authManager.SetSecureCookie(securityConfig.secureCookie)
	app := &App{
		boot:             boot,
		auth:             authManager,
		antiCrawler:      newAntiCrawlerMiddleware(securityConfig, authManager),
		memMgr:           memMgr,
		maiAgent:         maiAgent,
		modelStore:       models,
		mcpStore:         mcpServers,
		files:            files,
		newsSvc:          newsSvc,
		htmlRenderSvc:    htmlRenderSvc,
		chatHub:          chatHub,
		pluginMarket:     pluginmarket.NewService(),
		pluginHost:       pluginHost,
		acpAgent:         acpAgent,
		desktopPet:       desktopPet,
		desktopPetChat:   desktopPetChat,
		botAccounts:      botAccounts,
		botOnline:        botOnline,
		botAccountPairs:  botAccountPairs,
		platformAdapters: platformAdapters,
		avatar:           avatar,
	}
	// 对齐 Python webui/app.py 的 TextGZipMiddleware：最外层包装，只压一次性文本响应
	router := textGZipMiddleware(app.routes())
	app.router = router
	return app
}

// PluginMarket 暴露插件市场服务，供启动装配注入运行时协调器
func (a *App) PluginMarket() *pluginmarket.Service {
	return a.pluginMarket
}

// SetEnforcePublicOutboundProvider 注入动态出站公网校验开关，同时下发给插件市场
// SetSessionSchedulerStats 注入原生会话调度器统计；必须在开始服务前调用。
func (a *App) SetSessionSchedulerStats(provider func() any) {
	if a == nil {
		return
	}
	a.schedulerMu.Lock()
	a.sessionSchedulerStats = provider
	a.schedulerMu.Unlock()
}

// sessionSchedulerStatsSnapshot 在请求时读取最新 provider，避免装配顺序限制。
func (a *App) sessionSchedulerStatsSnapshot() any {
	if a == nil {
		return nil
	}
	a.schedulerMu.RLock()
	provider := a.sessionSchedulerStats
	a.schedulerMu.RUnlock()
	if provider == nil {
		return nil
	}
	return provider()
}

// SetSessionTaskSubmitter 注入 WebUI 聊天到原生调度器的提交入口。
func (a *App) SetSessionTaskSubmitter(submitter func(context.Context, string, func(context.Context) error) error) {
	if a == nil {
		return
	}
	a.schedulerMu.Lock()
	a.sessionTaskSubmitter = submitter
	a.schedulerMu.Unlock()
}

func (a *App) sessionTaskSubmitterSnapshot() func(context.Context, string, func(context.Context) error) error {
	if a == nil {
		return nil
	}
	a.schedulerMu.RLock()
	submitter := a.sessionTaskSubmitter
	a.schedulerMu.RUnlock()
	return submitter
}

func (a *App) SetEnforcePublicOutboundProvider(provider func() bool) {
	a.enforceMu.Lock()
	a.enforcePublicOutbound = provider
	a.enforceMu.Unlock()
	if a.pluginMarket != nil {
		a.pluginMarket.SetEnforcePublicOutboundProvider(provider)
	}
}

func (a *App) enforcePublicOutboundEnabled() bool {
	a.enforceMu.RLock()
	provider := a.enforcePublicOutbound
	a.enforceMu.RUnlock()
	if provider != nil {
		return provider()
	}
	return true
}

func (a *App) Addr() string {
	return fmt.Sprintf(":%d", a.boot.ServerPort)
}

func (a *App) Server() *http.Server {
	return &http.Server{
		Addr:    a.Addr(),
		Handler: a.router,
	}
}

func (a *App) routes() http.Handler {
	r := chi.NewRouter()
	// CORS 必须最先执行：预检请求不带业务 Cookie，若先过防爬虫或同源校验会被误拒，
	// 前端开发服务器（5173/7999）就完全无法联调。对齐 Python http_service/app.py
	// 的 _setup_cors 在 include_router 之前 add_middleware 的顺序。
	r.Use(httpservice.CORSMiddleware(a.boot.ServerPort))
	// 防爬虫必须包住同源校验，保证恶意请求在进入业务处理前被拒绝
	r.Use(a.antiCrawler.middleware)
	r.Use(a.sameOriginPostOnly)

	// 对齐 Python _setup_static_files：根路径与未命中的 GET 都由原版 Dashboard 承载
	dashboardDir, dashboardMounted := dashboardstatic.ResolveRoot(strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")))
	r.Get("/robots.txt", a.handleRobotsTxt)
	r.Get("/health", a.handleHealth)

	if a.files != nil {
		filesvc.Mount(r, a.files, filesvc.MountOpts{AdminAuth: a.auth, ChatHooks: a.fileChatHooks()})
	}

	webuiapi.Mount(r, webuiapi.Deps{
		Memory:                a.memMgr,
		AdminAuth:             a.auth,
		AuthRateLimiter:       GetRateLimiter(),
		PluginMarket:          a.pluginMarket,
		PluginHost:            a.pluginHost,
		News:                  a.newsSvc,
		HTMLRender:            a.htmlRenderSvc,
		MCPStore:              a.mcpStore,
		Agent:                 a.maiAgent,
		Files:                 a.files,
		ChatHub:               a.chatHub,
		ACPAgent:              a.acpAgent,
		DesktopPet:            a.desktopPet,
		DesktopPetChat:        a.desktopPetChat,
		BotAccounts:           a.botAccounts,
		BotAccountOnline:      a.botOnline,
		BotAccountPairs:       a.botAccountPairs,
		PlatformAdapters:      a.platformAdapters,
		Avatar:                a.avatar,
		EnforcePublicOutbound: a.enforcePublicOutboundEnabled,
		InstanceDir:           config.InstanceRoot(),
		BuildVersion:          version.String(),
		SessionSchedulerStats: a.sessionSchedulerStatsSnapshot,
		SessionTaskSubmitter:  a.sessionTaskSubmitterSnapshot,
	})

	if dashboardMounted {
		r.Get("/", dashboardstatic.RootIndexHandler(dashboardDir))
		// 未命中的 GET 先找 dist 静态文件，找不到回落 index.html（/api 除外）
		r.NotFound(dashboardstatic.SPAFallbackHandler(dashboardDir))
	} else {
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "未找到原版 Dashboard 构建产物；请用 docker 镜像或设置 MAIBOT_DASHBOARD_DIR。", http.StatusServiceUnavailable)
		})
	}

	return r
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = sonic.ConfigDefault.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"name":   "MaiGo",
		"time":   time.Now().Format(time.RFC3339),
	})
}

// sameOriginPostOnly 拒绝跨站表单提交；前端与 API 同源部署时不会触发
func (a *App) sameOriginPostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if origin != "" {
				originURL, err := neturl.Parse(origin)
				if err != nil || originURL.Host != r.Host || (originURL.Scheme != "http" && originURL.Scheme != "https") {
					http.Error(w, "请求来源无效。", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
