package host

// 宿主对外 HTTP：WebUI 静态资源、登录、Bot 管理与按 Bot 路由的业务代理。
// 对齐 src/host/webui.py、proxy.py、webui_auth.py 与 settings.py。

import (
	"context"
	"crypto/sha256"
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

	"github.com/go-chi/chi/v5"

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
	supervisor  *Supervisor
	sessions    *Sessions
	credentials *Credentials
	stoppingCh  chan struct{}
	config      HostConfig
	staticRoot  string
	server      *http.Server
	listeners   []net.Listener
}

// NewWebUI 构造宿主 WebUI
func NewWebUI(supervisor *Supervisor, config HostConfig, stopping chan struct{}) (*WebUI, error) {
	hostDir := supervisor.workspace.HostDirectory()
	webui := &WebUI{
		supervisor: supervisor,
		config:     config,
		stoppingCh: stopping,
	}
	if !config.WebUIEnabled {
		return webui, nil
	}
	credentials, err := NewCredentials(filepath.Join(hostDir, "webui.json"))
	if err != nil {
		return nil, fmt.Errorf("宿主 WebUI 认证初始化失败: %w", err)
	}
	sessions, err := NewSessions(supervisor.workspace.Root, credentials, config.SecureCookie)
	if err != nil {
		return nil, fmt.Errorf("宿主 WebUI 会话初始化失败: %w", err)
	}
	webui.credentials = credentials
	webui.sessions = sessions
	root, ok := dashboardstatic.ResolveRoot(supervisor.workspace.Root)
	if ok {
		webui.staticRoot = root
	}
	return webui, nil
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
	router.Get("/robots.txt", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = rw.Write([]byte("User-agent: *\nDisallow: /\n"))
	})
	router.HandleFunc("/api/host/bots/*", w.handleBotProxy)
	router.HandleFunc("/api/files/bots/*", w.handleTransferProxy)
	if w.Management() {
		w.mountManagement(router)
	}
	w.mountStatic(router)
	return w.protect(router)
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
		}
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
	router.Post("/api/host/bots/{bot_id}/{action}", w.handleHostBotAction)
	router.Get("/api/host/settings", w.handleCommonSettings)
	router.Put("/api/host/settings", w.handleSaveCommonSettings)
	router.Patch("/api/host/settings/appearance", w.handleSaveAppearance)
}

func (w *WebUI) hostModePayload(r *http.Request, authenticated bool, requested string) map[string]any {
	statuses, _ := w.supervisor.Statuses()
	selectedID := ""
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
	return map[string]any{
		"host": true, "authenticated": authenticated,
		"selected_bot_id": selectedID, "selected_bot_running": selectedRunning,
	}
}

func (w *WebUI) handleHostMode(rw http.ResponseWriter, r *http.Request) {
	authenticated := w.requireSession(r)
	payload := w.hostModePayload(r, authenticated, strings.TrimSpace(r.URL.Query().Get("bot_id")))
	if !authenticated {
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
	if body.BotID != nil {
		requested = strings.TrimSpace(*body.BotID)
		if requested != "" {
			if _, err := w.supervisor.workspace.Registry.GetBot(requested); err != nil {
				httputil2.WriteJSON(rw, http.StatusNotFound, map[string]any{"detail": "未找到 Bot"})
				return
			}
		}
	}
	if requested == "" {
		http.SetCookie(rw, &http.Cookie{
			Name: w.sessions.SelectName, Value: "", MaxAge: -1, HttpOnly: true,
			SameSite: http.SameSiteLaxMode, Path: "/", Secure: w.config.SecureCookie,
		})
	} else {
		http.SetCookie(rw, &http.Cookie{
			Name: w.sessions.SelectName, Value: requested, HttpOnly: true,
			SameSite: http.SameSiteLaxMode, Path: "/", Secure: w.config.SecureCookie,
		})
	}
	httputil2.WriteJSON(rw, http.StatusOK, w.hostModePayload(r, true, requested))
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
	if host == "" {
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
	record, err := CreateBot(w.supervisor.workspace, strings.TrimSpace(body.Name))
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
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
	if _, err := w.supervisor.workspace.Registry.RenameBot(botID, body.Name); err != nil {
		status := http.StatusUnprocessableEntity
		if strings.Contains(err.Error(), "未找到") {
			status = http.StatusNotFound
		}
		httputil2.WriteJSON(rw, status, map[string]any{"detail": err.Error()})
		return
	}
	status, err := w.supervisor.Status(botID)
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	httputil2.WriteJSON(rw, http.StatusOK, status)
}

// handleHostArchiveBot 停止并移除登记；不删除目录
func (w *WebUI) handleHostArchiveBot(rw http.ResponseWriter, r *http.Request) {
	if !w.requireSession(r) {
		httputil2.WriteJSON(rw, http.StatusUnauthorized, map[string]any{"detail": "请登录 WebUI"})
		return
	}
	botID := chi.URLParam(r, "bot_id")
	bot, err := w.supervisor.workspace.Registry.GetBot(botID)
	if err != nil {
		httputil2.WriteJSON(rw, http.StatusNotFound, map[string]any{"detail": "未找到 Bot"})
		return
	}
	if _, err := w.supervisor.Stop(botID); err != nil {
		httputil2.WriteJSON(rw, http.StatusConflict, map[string]any{"detail": err.Error()})
		return
	}
	if err := w.supervisor.workspace.Registry.RemoveBot(botID); err != nil {
		httputil2.WriteJSON(rw, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	httputil2.WriteJSON(rw, http.StatusOK, map[string]any{"directory": bot.Directory})
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
			rewriteResponseLocation(request.Out.Response, botID)
		},
		Transport: transport,
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, proxyErr error) {
			httputil2.WriteJSON(writer, http.StatusBadGateway, map[string]any{"detail": "无法连接此 Bot，请检查启动状态和日志"})
		},
	}
	proxy.ServeHTTP(rw, r)
}

// stripProxyRequestHeaders 去掉逐跳与宿主私有请求头
func stripProxyRequestHeaders(header http.Header) {
	for _, name := range []string{
		"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer",
		"Transfer-Encoding", "Upgrade", "Host", "Cookie", "Authorization", "Origin", "Forwarded",
	} {
		header.Del(name)
	}
	for name := range header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "sec-websocket-") {
			header.Del(name)
		}
	}
}

// rewriteResponseLocation 把 Bot 内部跳转改回宿主代理前缀
func rewriteResponseLocation(response *http.Response, botID string) {
	if response == nil {
		return
	}
	location := response.Header.Get("Location")
	if location == "" {
		return
	}
	parsed, err := url.Parse(location)
	if err != nil {
		response.Header.Del("Location")
		return
	}
	if parsed.Scheme != "" || parsed.Host != "" {
		response.Header.Del("Location")
		return
	}
	target := strings.TrimPrefix(parsed.Path, "/")
	if !allowedProxyPath(target) {
		response.Header.Del("Location")
		return
	}
	rewritten := proxyPrefix + botID + "/proxy/" + target
	if parsed.RawQuery != "" {
		rewritten += "?" + parsed.RawQuery
	}
	if parsed.Fragment != "" {
		rewritten += "#" + parsed.Fragment
	}
	response.Header.Set("Location", rewritten)
}

func (w *WebUI) mountStatic(router chi.Router) {
	if w.staticRoot == "" {
		return
	}
	router.Get("/*", dashboardstatic.SPAFallbackHandler(w.staticRoot))
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
	webui := map[string]any{
		"enabled": config.WebUIEnabled, "hosts": config.Hosts, "port": config.Port,
		"secure_cookie": config.SecureCookie, "anti_crawler_mode": config.AntiCrawlerMode,
		"allowed_ips": config.AllowedIPs, "trusted_proxies": config.TrustedProxies,
		"trust_xff": config.TrustXFF, "public_base_url": config.PublicBaseURL,
		"ssl_certfile": config.SSLCertFile, "ssl_keyfile": config.SSLKeyFile,
	}
	return map[string]any{
		"revision":         hex.EncodeToString(sum[:]),
		"webui":            webui,
		"retention":        map[string]any{},
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
	var body struct {
		Revision string         `json:"revision"`
		WebUI    map[string]any `json:"webui"`
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
	if err := SaveHostConfig(w.supervisor.workspace.Root, hostConfigFromWebUIMap(body.WebUI)); err != nil {
		httputil2.WriteJSON(rw, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
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
	if err := SaveHostConfig(w.supervisor.workspace.Root, w.config); err != nil {
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

func hostConfigFromWebUIMap(values map[string]any) HostConfig {
	config := DefaultHostConfig()
	if value, ok := values["enabled"].(bool); ok {
		config.WebUIEnabled = value
	}
	if items, ok := values["hosts"].([]any); ok {
		hosts := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				hosts = append(hosts, strings.TrimSpace(text))
			}
		}
		if len(hosts) > 0 {
			config.Hosts = hosts
		}
	}
	if port, ok := values["port"].(float64); ok && port >= 1 && port <= 65535 {
		config.Port = int(port)
	}
	if value, ok := values["secure_cookie"].(bool); ok {
		config.SecureCookie = value
	}
	if text, ok := values["anti_crawler_mode"].(string); ok {
		if text == "false" || text == "strict" || text == "loose" || text == "basic" {
			config.AntiCrawlerMode = text
		}
	}
	if text, ok := values["allowed_ips"].(string); ok {
		config.AllowedIPs = text
	}
	if text, ok := values["trusted_proxies"].(string); ok {
		config.TrustedProxies = text
	}
	if value, ok := values["trust_xff"].(bool); ok {
		config.TrustXFF = value
	}
	if text, ok := values["public_base_url"].(string); ok {
		config.PublicBaseURL = strings.TrimRight(strings.TrimSpace(text), "/")
	}
	return config
}

// ListenAndServe 在配置的地址上监听
func (w *WebUI) ListenAndServe(ctx context.Context) error {
	handler := w.Handler()
	w.server = &http.Server{Handler: handler}
	hosts := w.config.BindHosts()
	listeners := make([]net.Listener, 0, len(hosts))
	for _, host := range hosts {
		address := net.JoinHostPort(host, fmt.Sprint(w.config.Port))
		listener, err := net.Listen("tcp", address)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return err
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

// Close 关闭宿主 WebUI
func (w *WebUI) Close() error {
	for _, listener := range w.listeners {
		_ = listener.Close()
	}
	if w.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return w.server.Shutdown(ctx)
	}
	return nil
}
