package host

// 宿主对外 HTTP：WebUI 静态资源、登录、Bot 管理与按 Bot 路由的业务代理。
// 对齐 src/host/webui.py、proxy.py、webui_auth.py 与 settings.py。

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/pelletier/go-toml/v2"

	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/webui/dashboardstatic"
	httputil2 "MaiGo/internal/webui/httputil"
)

const (
	proxyPrefix = "/api/host/bots/"
)

var botProxyPath = regexp.MustCompile(`^/api/host/bots/([0-9a-f]{32})/proxy(/.*)?$`)

var unsafeProxyPrefixes = []string{"api/host", "api/webui/auth", "api/webui/setup"}

// WebUI 宿主对外 HTTP 服务
type WebUI struct {
	supervisor      *Supervisor
	sessions        *Sessions
	credentials     *Credentials
	maintenance     *HostMaintenance
	auth            *hostAuth
	antiCrawler     *AntiCrawler
	resolver        *ClientIPResolver
	static          *StaticHandler
	managementError string
	stop            func()
	stoppingCh      chan struct{}
	config          HostConfig
	staticRoot      string
	server          *http.Server
	listeners       []net.Listener
}

// NewWebUI 构造宿主 WebUI
func NewWebUI(supervisor *Supervisor, config HostConfig, stopping chan struct{}) (*WebUI, error) {
	hostDir := supervisor.workspace.HostDirectory()
	resolver := NewClientIPResolver(config.TrustedProxies, config.TrustXFF)
	webui := &WebUI{
		supervisor:  supervisor,
		config:      config,
		stoppingCh:  stopping,
		maintenance: NewHostMaintenance(supervisor),
		resolver:    resolver,
	}
	webui.antiCrawler = NewAntiCrawler(config, resolver, func(r *http.Request) bool {
		return webui.requireSession(r)
	})
	if !config.WebUIEnabled {
		return webui, nil
	}
	if supervisor.workspace.WebUIMigrationError != "" {
		webui.managementError = supervisor.workspace.WebUIMigrationError
		return webui, nil
	}
	credentials, err := NewCredentials(filepath.Join(hostDir, "webui.json"))
	if err != nil {
		webui.managementError = "宿主 WebUI 认证初始化失败，请检查宿主日志"
		return webui, nil
	}
	sessions, err := NewSessions(supervisor.workspace.Root, credentials, config.SecureCookie)
	if err != nil {
		webui.managementError = "宿主 WebUI 认证初始化失败，请检查宿主日志"
		return webui, nil
	}
	webui.credentials = credentials
	webui.sessions = sessions
	webui.auth = newHostAuth(sessions, credentials, resolver)
	root, ok := resolveHostStaticRoot(supervisor.workspace.Root)
	if ok {
		webui.staticRoot = root
		webui.static = NewStaticHandler(root)
	}
	return webui, nil
}

func resolveHostStaticRoot(workspace string) (string, bool) {
	if value := strings.ToLower(strings.TrimSpace(os.Getenv("MAIBOT_WEBUI_USE_LOCAL_DASHBOARD"))); value == "1" || value == "true" || value == "yes" || value == "on" {
		candidate := filepath.Join(maintenanceRoot(), "dashboard", "dist")
		if info, err := os.Stat(filepath.Join(candidate, "index.html")); err == nil && !info.IsDir() {
			return filepath.Clean(candidate), true
		}
	}
	return dashboardstatic.ResolveRoot(workspace)
}

// Management 是否启用管理界面
func (w *WebUI) Management() bool { return w.sessions != nil }

// OpenURL 生成一次性登录链接
func (w *WebUI) OpenURL() (string, error) {
	if w.sessions == nil {
		return "", errors.New("宿主 WebUI 未启用")
	}
	ticket, err := w.sessions.IssueTicket()
	if err != nil {
		return "", err
	}
	return w.config.LocalBaseURL() + "/bots#ticket=" + ticket, nil
}

// TemporaryToken 临时登录密码；固定口令时返回空串
func (w *WebUI) TemporaryToken() string {
	if w.credentials == nil || w.credentials.TokenSource() != "temporary" {
		return ""
	}
	return w.credentials.Token()
}

// Handler 构造宿主 HTTP 路由
func (w *WebUI) Handler() http.Handler {
	router := chi.NewRouter()
	router.Get("/health", func(rw http.ResponseWriter, r *http.Request) {
		httputil2.WriteJSON(rw, http.StatusOK, map[string]any{"status": "ok"})
	})
	router.Get("/robots.txt", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
		rw.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = rw.Write([]byte(robotsTXT))
	})
	router.HandleFunc("/maim/*", w.handleMaimGateway)
	router.HandleFunc("/api/host/bots/*", w.handleBotProxy)
	router.HandleFunc("/api/files/bots/*", w.handleTransferProxy)
	if w.Management() {
		w.mountManagement(router)
	} else if w.managementError != "" {
		router.Get("/api/host/mode", func(rw http.ResponseWriter, r *http.Request) {
			httputil2.WriteJSON(rw, http.StatusServiceUnavailable, map[string]any{"detail": w.managementError})
		})
	}
	w.mountStatic(router)
	return w.antiCrawler.middleware(hostCORS(w.protect(router)))
}

var hostDevelopmentOrigins = map[string]struct{}{"http://localhost:7999": {}, "http://127.0.0.1:7999": {}}

func hostCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
		if _, allowed := hostDevelopmentOrigins[origin]; allowed {
			rw.Header().Set("Access-Control-Allow-Origin", origin)
			rw.Header().Set("Access-Control-Allow-Credentials", "true")
			rw.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			rw.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-MaiBot-Host, Range, Accept")
			rw.Header().Set("Access-Control-Expose-Headers", "Content-Disposition, Content-Range")
		}
		if r.Method == http.MethodOptions {
			rw.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

// protect 管理请求标识与关闭期拒绝
func (w *WebUI) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasPrefix(path, "/api/") {
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				hostOperation := strings.HasPrefix(path, "/api/host/") && !strings.Contains(path, "/proxy/")
				if hostOperation && r.Header.Get("x-maibot-host") != "1" {
					httputil2.WriteJSON(rw, http.StatusForbidden, map[string]any{"detail": "缺少管理请求标识"})
					return
				}
			}
			select {
			case <-w.stoppingCh:
				httputil2.WriteJSON(rw, http.StatusServiceUnavailable, map[string]any{"detail": "宿主正在关闭"})
				return
			default:
			}
			rw.Header().Set("Cache-Control", "no-store")
		}
		rw.Header().Set("Referrer-Policy", "no-referrer")
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		rw.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(rw, r)
	})
}

func (w *WebUI) requireSession(r *http.Request) bool {
	if w.sessions == nil {
		return false
	}
	cookie, err := r.Cookie(w.sessions.CookieName)
	if err != nil {
		return false
	}
	return w.sessions.Valid(cookie.Value)
}

func (w *WebUI) mountManagement(router chi.Router) {
	w.auth.mount(router)
	router.Get("/api/host/mode", w.handleHostMode)
	router.Post("/api/host/selection", w.handleHostSelection)
	router.Post("/api/host/session", w.handleHostLogin)
	router.Get("/api/host/session", w.handleHostSession)
	router.Delete("/api/host/session", w.handleHostLogout)
	router.Get("/api/host/desktop-pet/session", w.handleDesktopPetSession)
	router.Get("/api/host/bots", w.handleHostBots)
	router.Post("/api/host/bots", w.handleHostCreateBot)
	router.Patch("/api/host/bots/{bot_id}", w.handleHostRenameBot)
	router.Delete("/api/host/bots/{bot_id}", w.handleHostArchiveBot)
	router.Post("/api/host/bots/{bot_id}/clone", w.handleHostCloneBot)
	router.Post("/api/host/bots/{bot_id}/{action}", w.handleHostBotAction)
	router.Get("/api/host/maintenance/update", w.handleUpdateStatus)
	router.Post("/api/host/maintenance/update/check", w.handleUpdateCheck)
	router.Post("/api/host/maintenance/update", w.handleUpdateApply)
	router.Get("/api/host/maintenance/plugin-dependencies", w.handlePluginDependencies)
	router.Post("/api/host/maintenance/plugin-dependencies", w.handleInstallPluginDependencies)
	router.Get("/api/host/settings", w.handleCommonSettings)
	router.Put("/api/host/settings", w.handleSaveCommonSettings)
	router.Patch("/api/host/settings/appearance", w.handleSaveAppearance)
	w.mountDataTransfer(router)
}

func (w *WebUI) hostModePayload(r *http.Request, authenticated bool, requested string) map[string]any {
	statuses, _ := w.supervisor.Statuses()
	var selectedID any
	selectedRunning := false
	candidate := requested
	if candidate == "" && r != nil {
		if cookie, err := r.Cookie(w.sessions.SelectName); err == nil {
			candidate = cookie.Value
		}
	}
	for _, status := range statuses {
		if status.BotID == candidate {
			selectedID = status.BotID
			selectedRunning = status.Status == "running"
			break
		}
	}
	if candidate == "" && len(statuses) > 0 {
		selectedID = statuses[0].BotID
		selectedRunning = statuses[0].Status == "running"
	}
	if authenticated {
		return map[string]any{
			"host": true, "authenticated": true,
			"selected_bot_id": selectedID, "selected_bot_running": selectedRunning,
		}
	}
	return map[string]any{
		"host": true, "authenticated": false,
		"selected_bot_id": nil, "selected_bot_running": false,
	}
}

func (w *WebUI) handleHostMode(rw http.ResponseWriter, r *http.Request) {
	authenticated := w.requireSession(r)
	requested := strings.TrimSpace(r.URL.Query().Get("bot_id"))
	payload := w.hostModePayload(r, authenticated, requested)
	if !authenticated || requested == "" && r.URL.Query().Has("bot_id") {
		payload["selected_bot_id"] = nil
		payload["selected_bot_running"] = false
	}
	httputil2.WriteJSON(rw, http.StatusOK, payload)
}

func (w *WebUI) handleHostSelection(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	var body struct {
		BotID *string `json:"bot_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil2.WriteJSON(rw, http.StatusUnprocessableEntity, map[string]any{"detail": "请求体格式错误"})
		return
	}
	requested := ""
	clear := body.BotID == nil
	if body.BotID != nil {
		requested = strings.TrimSpace(*body.BotID)
		if _, err := w.supervisor.workspace.Registry.GetBot(requested); err != nil {
			httputil2.WriteJSON(rw, http.StatusNotFound, map[string]any{"detail": "未找到 Bot"})
			return
		}
	}
	payload := w.hostModePayload(r, true, requested)
	if clear {
		http.SetCookie(rw, &http.Cookie{
			Name: w.sessions.SelectName, Value: "", MaxAge: -1, HttpOnly: true,
			SameSite: http.SameSiteLaxMode, Path: "/", Secure: w.config.SecureCookie,
		})
		payload["selected_bot_id"] = nil
		payload["selected_bot_running"] = false
	} else {
		http.SetCookie(rw, &http.Cookie{
			Name: w.sessions.SelectName, Value: requested, HttpOnly: true,
			SameSite: http.SameSiteLaxMode, Path: "/", Secure: w.config.SecureCookie,
		})
	}
	httputil2.WriteJSON(rw, http.StatusOK, payload)
}

func (w *WebUI) handleHostLogin(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil2.WriteJSON(rw, http.StatusUnprocessableEntity, map[string]any{"detail": "请求体格式错误"})
		return
	}
	session, ok := w.sessions.Exchange(strings.TrimSpace(body.Ticket), r.Header.Get("Origin"))
	if !ok {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "登录链接已失效，请使用 WebUI 密码登录"})
		return
	}
	w.sessions.SetCookie(rw, session)
	httputil2.WriteJSON(rw, http.StatusOK, map[string]any{"authenticated": true})
}

func (w *WebUI) handleHostSession(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	httputil2.WriteJSON(rw, http.StatusOK, map[string]any{"authenticated": true})
}

func (w *WebUI) handleHostLogout(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	if cookie, err := r.Cookie(w.sessions.CookieName); err == nil {
		w.sessions.Revoke(cookie.Value)
	}
	http.SetCookie(rw, &http.Cookie{
		Name: w.sessions.CookieName, Value: "", MaxAge: -1, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Path: "/",
	})
	httputil2.WriteJSON(rw, http.StatusOK, map[string]any{"authenticated": false})
}

const desktopPetSessionHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>桌宠连接 · MaiBot</title></head>
<body><p>桌宠已连接，可返回桌宠窗口</p></body></html>`

func (w *WebUI) handleDesktopPetSession(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	host := r.Host
	if cookie, err := r.Cookie(w.sessions.CookieName); err == nil {
		if loginOrigin := w.sessions.LoginOrigin(cookie.Value); loginOrigin != "" {
			if _, development := hostDevelopmentOrigins[loginOrigin]; !development {
				authority := loginOrigin
				if index := strings.Index(loginOrigin, "://"); index >= 0 {
					authority = loginOrigin[index+3:]
				}
				hostAuthority := host
				if index := strings.LastIndex(hostAuthority, ":"); index > 0 && !strings.HasSuffix(hostAuthority, "]") {
					hostAuthority = hostAuthority[:index]
				}
				authorityHost := authority
				if index := strings.LastIndex(authorityHost, ":"); index > 0 && !strings.HasSuffix(authorityHost, "]") {
					authorityHost = authorityHost[:index]
				}
				if strings.EqualFold(hostAuthority, authorityHost) {
					host = authority
				}
			}
		}
	}
	if !validWebUIHost(host) {
		httputil2.WriteJSON(rw, http.StatusBadRequest, map[string]any{"detail": "无效的 WebUI 地址"})
		return
	}
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Content-Security-Policy",
		"default-src 'none'; connect-src 'self' ipc: http://ipc.localhost ws://"+host+" wss://"+host+
			"; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = rw.Write([]byte(desktopPetSessionHTML))
}

var webUIHostPattern = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+|\[[0-9A-Fa-f:.]+\])(?::[0-9]{1,5})?$`)

func validWebUIHost(host string) bool {
	if !webUIHostPattern.MatchString(host) {
		return false
	}
	if _, port, err := net.SplitHostPort(host); err == nil && port == "0" {
		return false
	}
	return true
}

func (w *WebUI) handleHostBots(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	statuses, err := w.supervisor.Statuses()
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	httputil2.WriteJSON(rw, http.StatusOK, map[string]any{
		"bots": statuses, "workspace": w.supervisor.workspace.Root,
	})
}

func (w *WebUI) handleHostCreateBot(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil2.WriteJSON(rw, http.StatusUnprocessableEntity, map[string]any{"detail": "请求体格式错误"})
		return
	}
	record, err := w.maintenance.CreateBot(strings.TrimSpace(body.Name))
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "正在执行") || strings.Contains(err.Error(), "正在更新") {
			status = http.StatusConflict
		} else if strings.Contains(err.Error(), "名称") {
			status = http.StatusUnprocessableEntity
		}
		httputil2.WriteJSON(rw, status, map[string]any{"detail": err.Error()})
		return
	}
	status, err := w.supervisor.Status(record.BotID)
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	httputil2.WriteJSON(rw, http.StatusCreated, status)
}

func (w *WebUI) handleHostRenameBot(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil2.WriteJSON(rw, http.StatusUnprocessableEntity, map[string]any{"detail": "请求体格式错误"})
		return
	}
	botID := chi.URLParam(r, "bot_id")
	result, err := w.maintenance.ManageBot(botID, "rename", body.Name)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if strings.Contains(err.Error(), "未找到") {
			status = http.StatusNotFound
		} else if strings.Contains(err.Error(), "正在执行") || strings.Contains(err.Error(), "正在更新") {
			status = http.StatusConflict
		}
		httputil2.WriteJSON(rw, status, map[string]any{"detail": err.Error()})
		return
	}
	httputil2.WriteJSON(rw, http.StatusOK, result)
}

// handleHostCloneBot 复制配置与插件并登记为新 Bot
func (w *WebUI) handleHostCloneBot(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil2.WriteJSON(rw, http.StatusUnprocessableEntity, map[string]any{"detail": "请求体格式错误"})
		return
	}
	result, err := w.maintenance.ManageBot(chi.URLParam(r, "bot_id"), "clone", body.Name)
	if err != nil {
		status := http.StatusConflict
		if strings.Contains(err.Error(), "未找到") {
			status = http.StatusNotFound
		} else if strings.Contains(err.Error(), "名称") {
			status = http.StatusUnprocessableEntity
		}
		httputil2.WriteJSON(rw, status, map[string]any{"detail": err.Error()})
		return
	}
	httputil2.WriteJSON(rw, http.StatusOK, result)
}

// handleHostArchiveBot 归档并移除登记；外部目录只注销
func (w *WebUI) handleHostArchiveBot(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	result, err := w.maintenance.ManageBot(chi.URLParam(r, "bot_id"), "archive", "")
	if err != nil {
		status := http.StatusConflict
		if strings.Contains(err.Error(), "未找到") {
			status = http.StatusNotFound
		}
		httputil2.WriteJSON(rw, status, map[string]any{"detail": err.Error()})
		return
	}
	httputil2.WriteJSON(rw, http.StatusOK, result)
}

func (w *WebUI) handleHostBotAction(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	botID := chi.URLParam(r, "bot_id")
	action := chi.URLParam(r, "action")
	var status BotStatus
	var err error
	switch action {
	case "start":
		status, err = w.supervisor.Start(botID)
	case "stop":
		status, err = w.supervisor.Stop(botID)
	case "restart":
		status, err = w.supervisor.Restart(botID)
	default:
		httputil2.WriteJSON(rw, http.StatusNotFound, map[string]any{"detail": "未知 Bot 操作"})
		return
	}
	if err != nil {
		code := http.StatusConflict
		if strings.Contains(err.Error(), "未找到") {
			code = http.StatusNotFound
		}
		httputil2.WriteJSON(rw, code, map[string]any{"detail": err.Error()})
		return
	}
	httputil2.WriteJSON(rw, http.StatusOK, status)
}

// handleBotProxy 把业务请求转发到对应 Bot 的进程间通道
func (w *WebUI) handleBotProxy(rw http.ResponseWriter, r *http.Request) {
	matches := botProxyPath.FindStringSubmatch(r.URL.Path)
	if matches == nil {
		httputil2.WriteJSON(rw, http.StatusNotFound, map[string]any{"detail": "未找到接口"})
		return
	}
	botID := matches[1]
	path := strings.TrimPrefix(matches[2], "/")
	if !allowedProxyPath(path) {
		httputil2.WriteJSON(rw, http.StatusNotFound, map[string]any{"detail": "此路径不属于 Bot 业务接口"})
		return
	}
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请先登录 WebUI"})
		return
	}
	w.forwardToBot(rw, r, botID, path)
}

var transferPathPattern = regexp.MustCompile(`^/api/files/bots/([0-9a-f]{32})/transfers/([A-Za-z0-9_-]{1,256})$`)

// handleTransferProxy 文件传输由 Bot 的限时凭证鉴权，不依赖浏览器登录
func (w *WebUI) handleTransferProxy(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPut {
		httputil2.WriteJSON(rw, http.StatusMethodNotAllowed, map[string]any{"detail": "方法不允许"})
		return
	}
	matches := transferPathPattern.FindStringSubmatch(r.URL.Path)
	if matches == nil {
		httputil2.WriteJSON(rw, http.StatusNotFound, map[string]any{"detail": "无效的文件传输凭证"})
		return
	}
	w.forwardToBot(rw, r, matches[1], "api/files/transfers/"+matches[2])
}

func allowedProxyPath(path string) bool {
	if strings.Contains(path, "\\") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	if !strings.HasPrefix(path, "api/") {
		return false
	}
	for _, prefix := range unsafeProxyPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return false
		}
	}
	return true
}

func (w *WebUI) forwardToBot(rw http.ResponseWriter, r *http.Request, botID, path string) {
	endpoint, err := w.supervisor.ProxyEndpoint(botID)
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusServiceUnavailable, map[string]any{"detail": err.Error()})
		return
	}
	if isWebSocketUpgrade(r) {
		w.forwardWebSocket(rw, r, path, endpoint)
		return
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialEndpoint(endpoint)
		},
		DisableCompression: true,
		Proxy:              nil,
	}
	defer transport.CloseIdleConnections()
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.Out.URL.Scheme = "http"
			request.Out.URL.Host = "bot"
			request.Out.URL.Path = "/" + path
			request.Out.Host = "bot"
			stripProxyRequestHeaders(request.Out.Header)
		},
		Transport: transport,
		ModifyResponse: func(response *http.Response) error {
			stripProxyResponseHeaders(response.Header)
			return rewriteResponseLocation(response, botID)
		},
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, proxyErr error) {
			httputil2.WriteJSON(writer, http.StatusBadGateway, map[string]any{"detail": "无法连接此 Bot，请检查启动状态和日志"})
		},
	}
	proxy.ServeHTTP(rw, r)
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket")
}

var errHostSessionRevoked = errors.New("WebUI 登录已失效，请重新登录")

func (w *WebUI) forwardWebSocket(rw http.ResponseWriter, r *http.Request, path, endpoint string) {
	headers := http.Header{}
	connectionTokens := connectionHeaderTokens(r.Header)
	for name, values := range r.Header {
		lower := strings.ToLower(name)
		if _, private := proxyPrivateHeaders[lower]; private || connectionTokens[lower] ||
			strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "sec-websocket-") ||
			lower == "upgrade" || lower == "connection" {
			continue
		}
		for _, value := range values {
			headers.Add(name, value)
		}
	}
	requestURL := "http://bot/" + path
	if r.URL.RawQuery != "" {
		requestURL += "?" + r.URL.RawQuery
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialEndpoint(endpoint)
		},
	}
	defer transport.CloseIdleConnections()
	upstream, _, err := websocket.Dial(r.Context(), requestURL, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: transport}, HTTPHeader: headers,
	})
	if err != nil {
		connection, acceptErr := websocket.Accept(rw, r, &websocket.AcceptOptions{InsecureSkipVerify: true, OriginPatterns: []string{"*"}})
		if acceptErr == nil {
			_ = connection.Close(websocket.StatusTryAgainLater, "Bot WebSocket 尚未就绪")
		}
		return
	}
	client, err := websocket.Accept(rw, r, &websocket.AcceptOptions{InsecureSkipVerify: true, OriginPatterns: []string{"*"}})
	if err != nil {
		_ = upstream.Close(websocket.StatusGoingAway, "")
		return
	}
	// 对齐 Python：宿主 uvicorn 的浏览器侧上限为 100 MiB；到 Bot 的 aiohttp
	// 代理使用 max_msg_size=0（不限制），coder 需传 -1 才是无限。
	upstream.SetReadLimit(-1)
	client.SetReadLimit(104_857_600)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	relay := func(source, target *websocket.Conn) error {
		for {
			messageType, data, err := source.Read(ctx)
			if err != nil {
				return err
			}
			if !w.requireSession(r) {
				return errHostSessionRevoked
			}
			if err := target.Write(ctx, messageType, data); err != nil {
				return err
			}
		}
	}
	done := make(chan error, 2)
	go func() { done <- relay(client, upstream) }()
	go func() { done <- relay(upstream, client) }()
	relayErr := <-done
	cancel()
	code := websocket.StatusGoingAway
	if relayErr == errHostSessionRevoked {
		code = 4001
	} else if status := websocket.CloseStatus(relayErr); status == 1008 {
		code = 1008
	}
	_ = upstream.Close(code, "")
	_ = client.Close(code, "")
}

var proxyHopHeaders = map[string]struct{}{
	"connection": {}, "keep-alive": {}, "proxy-authenticate": {}, "proxy-authorization": {},
	"te": {}, "trailer": {}, "transfer-encoding": {}, "upgrade": {},
}

var proxyPrivateHeaders = map[string]struct{}{
	"host": {}, "cookie": {}, "authorization": {}, "origin": {}, "forwarded": {},
}

// stripProxyRequestHeaders 去掉逐跳与宿主私有请求头
func stripProxyRequestHeaders(header http.Header) {
	connectionTokens := connectionHeaderTokens(header)
	for name := range header {
		lower := strings.ToLower(name)
		_, hop := proxyHopHeaders[lower]
		_, private := proxyPrivateHeaders[lower]
		if hop || private || connectionTokens[lower] ||
			strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "sec-websocket-") {
			header.Del(name)
		}
	}
}

// stripProxyResponseHeaders 去掉 Cookie 与 CORS 响应头，由宿主统一管理浏览器会话
func stripProxyResponseHeaders(header http.Header) {
	connectionTokens := connectionHeaderTokens(header)
	for name := range header {
		lower := strings.ToLower(name)
		_, hop := proxyHopHeaders[lower]
		if hop || connectionTokens[lower] || lower == "set-cookie" || lower == "content-length" ||
			strings.HasPrefix(lower, "access-control-") {
			header.Del(name)
		}
	}
}

func connectionHeaderTokens(header http.Header) map[string]bool {
	tokens := map[string]bool{}
	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			token = strings.ToLower(strings.TrimSpace(token))
			if token != "" {
				tokens[token] = true
			}
		}
	}
	return tokens
}

// rewriteResponseLocation 把 Bot 内部跳转改回宿主代理前缀
func rewriteResponseLocation(response *http.Response, botID string) error {
	if response == nil {
		return nil
	}
	location := response.Header.Get("Location")
	if location == "" {
		return nil
	}
	parsed, err := url.Parse(location)
	if err != nil {
		return errors.New("Bot 返回了不支持的跳转地址")
	}
	if parsed.Scheme != "" || parsed.Host != "" {
		if parsed.Scheme != "http" || parsed.Host != "bot" {
			return errors.New("Bot 返回了不支持的跳转地址")
		}
	}
	target := strings.TrimPrefix(parsed.Path, "/")
	if !allowedProxyPath(target) {
		return errors.New("Bot 返回了不支持的跳转地址")
	}
	rewritten := proxyPrefix + botID + "/proxy/" + target
	if parsed.RawQuery != "" {
		rewritten += "?" + parsed.RawQuery
	}
	if parsed.Fragment != "" {
		rewritten += "#" + parsed.Fragment
	}
	response.Header.Set("Location", rewritten)
	return nil
}

func (w *WebUI) mountStatic(router chi.Router) {
	if w.staticRoot != "" {
		router.Handle("/*", w.static)
		return
	}
	if w.Management() {
		router.HandleFunc("/*", func(rw http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
				http.NotFound(rw, r)
				return
			}
			http.Error(rw, "当前 WebUI 资源不可用，请安装新版资源或构建 dashboard", http.StatusServiceUnavailable)
		})
	}
}

// settingsSnapshot 公共设置快照，对齐 settings.py 的 snapshot
func (w *WebUI) settingsSnapshot() (map[string]any, error) {
	path := hostConfigPath(w.supervisor.workspace.Root)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	config, err := LoadHostConfig(w.supervisor.workspace.Root)
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if err := toml.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	webui := map[string]any{
		"enabled": config.WebUIEnabled, "hosts": config.Hosts, "port": config.Port,
		"secure_cookie": config.SecureCookie, "anti_crawler_mode": config.AntiCrawlerMode,
		"allowed_ips": config.AllowedIPs, "trusted_proxies": config.TrustedProxies,
		"trust_xff": config.TrustXFF, "public_base_url": config.PublicBaseURL,
		"ssl_certfile": config.SSLCertFile, "ssl_keyfile": config.SSLKeyFile,
	}
	shared := maibotcfg.SharedWebUIDefaults()
	if section, ok := document["webui"].(map[string]any); ok {
		for key := range shared {
			if value, exists := section[key]; exists {
				shared[key] = value
			}
		}
	}
	for key, value := range shared {
		webui[key] = value
	}
	retention := maibotcfg.SharedRetentionDefaults()
	if section, ok := document["retention"].(map[string]any); ok {
		validated, err := maibotcfg.ValidateRetention(section)
		if err != nil {
			return nil, err
		}
		retention = validated
	}
	return map[string]any{
		"revision":         hex.EncodeToString(sum[:]),
		"webui":            webui,
		"retention":        retention,
		"restart_required": !sameHostConfig(config, w.config),
	}, nil
}

func sameHostConfig(left, right HostConfig) bool {
	if left.WebUIEnabled != right.WebUIEnabled || left.Port != right.Port ||
		left.SecureCookie != right.SecureCookie || left.AntiCrawlerMode != right.AntiCrawlerMode ||
		left.AllowedIPs != right.AllowedIPs || left.TrustedProxies != right.TrustedProxies ||
		left.TrustXFF != right.TrustXFF || left.PublicBaseURL != right.PublicBaseURL ||
		left.SSLCertFile != right.SSLCertFile || left.SSLKeyFile != right.SSLKeyFile {
		return false
	}
	if len(left.Hosts) != len(right.Hosts) {
		return false
	}
	for index := range left.Hosts {
		if left.Hosts[index] != right.Hosts[index] {
			return false
		}
	}
	return true
}

func (w *WebUI) handleCommonSettings(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	snapshot, err := w.settingsSnapshot()
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	httputil2.WriteJSON(rw, http.StatusOK, snapshot)
}

func (w *WebUI) handleSaveCommonSettings(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	lock, err := LockFilePath(filepath.Join(w.supervisor.workspace.HostDirectory(), "model-config.lock"))
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusConflict, map[string]any{"detail": "公共设置正在被其他操作修改"})
		return
	}
	defer lock.Release()
	var body struct {
		Revision  string         `json:"revision"`
		WebUI     map[string]any `json:"webui"`
		Retention map[string]any `json:"retention"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil2.WriteJSON(rw, http.StatusUnprocessableEntity, map[string]any{"detail": "请求体格式错误"})
		return
	}
	snapshot, err := w.settingsSnapshot()
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if snapshot["revision"] != body.Revision {
		httputil2.WriteJSON(rw, http.StatusConflict, map[string]any{"detail": "公共设置已被修改，请重新加载后保存"})
		return
	}
	expected, _ := snapshot["webui"].(map[string]any)
	if len(body.WebUI) != len(expected) {
		httputil2.WriteJSON(rw, http.StatusUnprocessableEntity, map[string]any{"detail": "公共网络设置字段不完整或含未知字段"})
		return
	}
	for key := range expected {
		if _, exists := body.WebUI[key]; !exists {
			httputil2.WriteJSON(rw, http.StatusUnprocessableEntity, map[string]any{"detail": "公共网络设置字段不完整或含未知字段"})
			return
		}
	}
	retention, err := maibotcfg.ValidateRetention(body.Retention)
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
		return
	}
	shared, err := maibotcfg.ValidateSharedWebUI(body.WebUI)
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
		return
	}
	config, err := hostConfigFromWebUIMap(body.WebUI)
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
		return
	}
	if config.SSLCertFile != "" {
		if _, err := tls.LoadX509KeyPair(config.SSLCertFile, config.SSLKeyFile); err != nil {
			httputil2.WriteJSON(rw, http.StatusUnprocessableEntity, map[string]any{"detail": "SSL 证书或私钥无效: " + err.Error()})
			return
		}
	}
	if err := SaveHostConfig(w.supervisor.workspace.Root, config); err != nil {
		httputil2.WriteJSON(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if err := updateHostDocument(w.supervisor.workspace.Root, func(document map[string]any) error {
		webui, _ := document["webui"].(map[string]any)
		if webui == nil {
			webui = map[string]any{}
			document["webui"] = webui
		}
		for key, value := range shared {
			webui[key] = value
		}
		document["retention"] = retention
		return nil
	}); err != nil {
		httputil2.WriteJSON(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	updated, err := w.settingsSnapshot()
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	httputil2.WriteJSON(rw, http.StatusOK, updated)
}

func (w *WebUI) handleSaveAppearance(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	lock, err := LockFilePath(filepath.Join(w.supervisor.workspace.HostDirectory(), "model-config.lock"))
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusConflict, map[string]any{"detail": "公共设置正在被其他操作修改"})
		return
	}
	defer lock.Release()
	var body struct {
		WebUIStyle *int `json:"webui_style"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.WebUIStyle == nil {
		httputil2.WriteJSON(rw, http.StatusUnprocessableEntity, map[string]any{"detail": "webui_style 必须是 0 到 2 的整数"})
		return
	}
	if *body.WebUIStyle < 0 || *body.WebUIStyle > 2 {
		httputil2.WriteJSON(rw, http.StatusUnprocessableEntity, map[string]any{"detail": "webui_style 必须是 0 到 2 的整数"})
		return
	}
	if err := updateHostDocument(w.supervisor.workspace.Root, func(document map[string]any) error {
		webui, _ := document["webui"].(map[string]any)
		if webui == nil {
			webui = map[string]any{}
			document["webui"] = webui
		}
		webui["webui_style"] = int64(*body.WebUIStyle)
		return nil
	}); err != nil {
		httputil2.WriteJSON(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	snapshot, err := w.settingsSnapshot()
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	httputil2.WriteJSON(rw, http.StatusOK, snapshot)
}

func updateHostDocument(root string, mutate func(map[string]any) error) error {
	path := hostConfigPath(root)
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var document map[string]any
	if err := toml.Unmarshal(raw, &document); err != nil {
		return err
	}
	if err := mutate(document); err != nil {
		return err
	}
	encoded, err := toml.Marshal(document)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func hostConfigFromWebUIMap(values map[string]any) (HostConfig, error) {
	return validateHostConfig(map[string]any{"webui": values}, "公共设置")
}

// ListenAndServe 在配置的地址上监听
func (w *WebUI) ListenAndServe(ctx context.Context) error {
	handler := w.Handler()
	w.server = &http.Server{Handler: handler}
	var tlsConfig *tls.Config
	if w.config.SSLCertFile != "" || w.config.SSLKeyFile != "" {
		certificate, err := tls.LoadX509KeyPair(w.config.SSLCertFile, w.config.SSLKeyFile)
		if err != nil {
			return fmt.Errorf("加载 WebUI TLS 证书失败: %w", err)
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	}
	hosts := w.config.BindHosts()
	listeners := make([]net.Listener, 0, len(hosts))
	for _, host := range hosts {
		address := net.JoinHostPort(host, fmt.Sprint(w.config.Port))
		listener, err := net.Listen(hostListenNetwork(host), address)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return err
		}
		if tlsConfig != nil {
			listener = tls.NewListener(listener, tlsConfig)
		}
		listeners = append(listeners, listener)
	}
	w.listeners = listeners
	errCh := make(chan error, len(listeners))
	for _, listener := range listeners {
		go func(ln net.Listener) {
			if err := w.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}(listener)
	}
	select {
	case <-ctx.Done():
		return w.Close()
	case err := <-errCh:
		return err
	}
}

// hostListenNetwork 显式区分 IPv4/IPv6，避免 wildcard 监听同时占用 IPv6 地址
func hostListenNetwork(host string) string {
	address := net.ParseIP(strings.TrimSpace(host))
	if address == nil {
		return "tcp"
	}
	if address.To4() != nil {
		return "tcp4"
	}
	return "tcp6"
}

// Close 关闭宿主 WebUI
func (w *WebUI) Close() error {
	for _, listener := range w.listeners {
		_ = listener.Close()
	}
	if w.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		defer cancel()
		return w.server.Shutdown(ctx)
	}
	return nil
}
