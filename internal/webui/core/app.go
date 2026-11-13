package app

import (
	"context"
	"fmt"
	httpservice "maibot-go/internal/common/http_service"
	"net/http"
	neturl "net/url"
	"os"
	"strings"
	"sync"
	"time"

	"maibot-go/internal/common/database"
	"maibot-go/internal/common/version"
	"maibot-go/internal/config"
	"maibot-go/internal/config/model_profiles"
	"maibot-go/internal/core/internal_chat"
	"maibot-go/internal/files"
	maisaruntime "maibot-go/internal/maisaka/runtime"
	"maibot-go/internal/mcp_module"
	"maibot-go/internal/platform_io"
	"maibot-go/internal/plugin_market"
	"maibot-go/internal/plugin_runtime"
	botservices "maibot-go/internal/services"
	"maibot-go/internal/services/html_render"
	"maibot-go/internal/services/news"
	"maibot-go/internal/webui/dashboardstatic"
	"maibot-go/internal/webui/middleware"
	"maibot-go/internal/webui/routers"

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
	botAccounts      *botservices.BotAccountService
	botOnline        func(platform, accountID string) bool
	botAccountPairs  func(context.Context) ([][2]string, error)
	platformAdapters webuiapi.PlatformAdapterSource
	avatar           *platformio.AvatarService
	router           http.Handler

	enforceMu             sync.RWMutex
	enforcePublicOutbound func() bool
}

func New(boot config.Bootstrap, memMgr *memory.Manager, maiAgent *maisaruntime.Agent, models *modelprofiles.Store, mcpServers *mcp.Store, files *filesvc.Service, newsSvc *news.Service, htmlRenderSvc *htmlrender.Service, chatHub *internalchat.Hub, pluginHost *pluginruntime.Host, acpAgent webuiapi.AgentControlService, botAccounts *botservices.BotAccountService, botOnline func(platform, accountID string) bool, botAccountPairs func(context.Context) ([][2]string, error), platformAdapters webuiapi.PlatformAdapterSource, avatar *platformio.AvatarService) *App {
	instanceDir := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	if instanceDir == "" {
		// 与 main.go 的 instanceRoot 一致：未显式设置时以工作目录为实例根，读取 config/bot_config.toml
		instanceDir, _ = os.Getwd()
	}
	securityConfig, err := loadAntiCrawlerConfig(instanceDir)
	if err != nil {
		// main.go 的 New 调用没有错误返回值，坏 TOML 用 panic 拒绝启动，避免静默降级
		panic(err)
	}
	authManager := auth.NewManager(boot.WebAdminKey, auth.SessionTTL)
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
		BotAccounts:           a.botAccounts,
		BotAccountOnline:      a.botOnline,
		BotAccountPairs:       a.botAccountPairs,
		PlatformAdapters:      a.platformAdapters,
		Avatar:                a.avatar,
		EnforcePublicOutbound: a.enforcePublicOutboundEnabled,
		InstanceDir:           strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")),
		BuildVersion:          version.String(),
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
		"name":   "maibot-go",
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
