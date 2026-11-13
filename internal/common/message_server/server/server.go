package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"maibot-go/internal/common/message_server"
	"maibot-go/internal/common/utils"
	"maibot-go/internal/platform_io"
	"maibot-go/internal/platform_io/drivers/maim_bridge"

	"go.uber.org/zap"
)

// InboundHandler 入站 MessageBase 继续 Platform IO
type InboundHandler func(ctx context.Context, connPlatform string, msg maimmsg.MessageBase) error

// Server 旧版 maim_message WS + 新 API Server
type Server struct {
	cfg     Config
	hub     *connHub
	apiHub  *apiHub
	manager *platformio.Manager
	onMsg   InboundHandler

	customHandlers  map[string]APICustomHandler
	seenAPIMessages map[string]time.Time
	stateMu         sync.Mutex

	apiStats *apiStats
	apiCache *apiMessageCache

	httpServer *http.Server
	apiServer  *http.Server
	mu         sync.Mutex

	// cfgMu 保护可热更新的鉴权配置快照
	cfgMu sync.RWMutex

	// 心跳参数可注入以便测试缩短，零值使用默认 30s/10s
	pingInterval time.Duration
	pongWait     time.Duration
}

// New 创建服务；manager 用于连接时动态绑定收发路由
func New(cfg Config, manager *platformio.Manager, onMsg InboundHandler) *Server {
	server := &Server{
		cfg:             cfg,
		hub:             newConnHub(),
		apiHub:          newAPIHub(),
		manager:         manager,
		onMsg:           onMsg,
		customHandlers:  make(map[string]APICustomHandler),
		seenAPIMessages: make(map[string]time.Time),
		pingInterval:    defaultPingInterval,
		pongWait:        defaultPongWait,
	}
	if cfg.APIStatsEnabled {
		server.apiStats = &apiStats{}
	}
	if cfg.APIMessageCacheEnabled {
		server.apiCache = newAPIMessageCache(cfg.APIMessageCacheTTL, cfg.APIMessageCacheMaxSize, cfg.APIMessageCacheCleanup)
		server.apiHub.cache = server.apiCache
	}
	return server
}

// APIStats 返回新 API Server 的连接与消息统计
func (s *Server) APIStats() map[string]any {
	if s == nil {
		return map[string]any{}
	}
	connections, users := 0, 0
	if s.apiHub != nil {
		connections, users = s.apiHub.connectionCounts()
	}
	stats := s.apiStats.snapshot(users, connections)
	if s.apiCache != nil {
		stats["message_cache"] = s.apiCache.Stats()
	}
	cfg := s.config()
	return map[string]any{
		"success": true,
		"stats":   stats,
		"network": map[string]any{
			"protocol":    "websocket",
			"api_host":    cfg.APIHost,
			"api_port":    cfg.APIPort,
			"api_path":    cfg.APIPath,
			"ssl_enabled": cfg.APISSLEnabled && cfg.APISSLConfig.Enabled(),
		},
	}
}

// SetInboundHandler 设置入站回调（便于在 Adapter 创建后接线）
func (s *Server) SetInboundHandler(handler InboundHandler) {
	s.onMsg = handler
}

// Hub 返回出站写入面，供 maimbridge.Adapter 使用
// 旧版连接优先，新 API Server 作为回退
func (s *Server) Hub() maimbridge.Hub {
	return &hubRouter{legacy: s.hub, api: s.apiHub}
}

// Start 在后台监听旧版 WS 与新 API Server
func (s *Server) Start(ctx context.Context) error {
	cfg := s.config()
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc(wsPath, s.handleWS)

	listener, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		// 对齐 Python port_checker.py:63-84：端口占用时给出可操作的排查建议
		if utils.IsPortConflictError(err) {
			host, port := splitHostPort(cfg.Addr())
			zap.L().Error(utils.BuildPortConflictMessage("maim_message WS 服务", host, port))
			for _, hint := range utils.PortConflictHints(port, "maim_message.ws_port") {
				zap.L().Error(hint)
			}
		}
		return err
	}
	serveListener := net.Listener(listener)
	if cfg.LegacySSLEnabled {
		tlsConfig, tlsErr := buildServerTLSConfig(cfg.LegacySSLConfig, "maim_message WS SSL")
		if tlsErr != nil {
			_ = listener.Close()
			return tlsErr
		}
		serveListener = tls.NewListener(listener, tlsConfig)
	}
	s.httpServer = &http.Server{
		Addr:              cfg.Addr(),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(shutdownCtx)
	}()

	if cfg.APIEnabled {
		if err := s.startAPIServer(ctx); err != nil {
			_ = listener.Close()
			return err
		}
	}

	zap.L().Info("maim_message WS 服务启动",
		zap.String("addr", cfg.Addr()),
		zap.Bool("token_required", cfg.TokenRequired()),
		zap.Bool("ssl_enabled", cfg.LegacySSLEnabled),
		zap.Bool("api_enabled", cfg.APIEnabled))
	err = s.httpServer.Serve(serveListener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"service":"maim_message","ws":"/ws"}`))
}

func (s *Server) inbound(ctx context.Context, connPlatform string, msg maimmsg.MessageBase) error {
	if s.onMsg != nil {
		return s.onMsg(ctx, connPlatform, msg)
	}
	return nil
}

func (s *Server) ensurePlatformRoutes(platform string, identity ...string) {
	if s.manager == nil || platform == "" || platform == "unknown" {
		return
	}
	route := platformio.RouteKey{Platform: platform}
	if len(identity) > 0 {
		route.AccountID = identity[0]
	}
	if len(identity) > 1 {
		route.Scope = identity[1]
	}
	key, err := route.Normalize()
	if err != nil {
		return
	}
	binding := platformio.RouteBinding{
		RouteKey:   key,
		DriverID:   maimbridge.DriverIDMaimMsg,
		DriverKind: platformio.DriverKindLegacy,
	}
	if err := s.manager.BindReceiveRoute(binding); err != nil {
		zap.L().Debug("maim_message 绑定接收路由失败",
			zap.String("platform", platform),
			zap.Error(err))
	}
	if err := s.manager.BindSendRoute(binding); err != nil {
		zap.L().Debug("maim_message 绑定发送路由失败",
			zap.String("platform", platform),
			zap.Error(err))
	}
}

func parseMessageFrame(data []byte) (maimmsg.MessageBase, error) {
	var msg maimmsg.MessageBase
	if err := json.Unmarshal(data, &msg); err != nil {
		return maimmsg.MessageBase{}, fmt.Errorf("JSON 解析失败: %w", err)
	}
	return msg, nil
}
