package server

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"go.uber.org/zap"
)

const (
	headerPlatform      = "Platform"
	headerAuthorization = "Authorization"
	wsPath              = "/ws"

	closeReasonInvalidToken = "无效的令牌"
	defaultPingInterval     = 30 * time.Second
	defaultPongWait         = 10 * time.Second
)

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	platform := strings.TrimSpace(r.Header.Get(headerPlatform))
	if platform == "" {
		platform = strings.TrimSpace(r.Header.Get(strings.ToLower(headerPlatform)))
	}
	if platform == "" {
		platform = "unknown"
	}

	if cfg.TokenRequired() {
		token := strings.TrimSpace(r.Header.Get(headerAuthorization))
		if token == "" {
			token = strings.TrimSpace(r.Header.Get(strings.ToLower(headerAuthorization)))
		}
		if !s.validToken(token) {
			http.Error(w, closeReasonInvalidToken, http.StatusUnauthorized)
			return
		}
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		OriginPatterns:     []string{"*"},
	})
	if err != nil {
		zap.L().Warn("maim_message WS 握手失败", zap.Error(err))
		return
	}

	s.hub.Attach(platform, ws)
	s.ensurePlatformRoutes(platform)

	zap.L().Info("maim_message 适配器已连接",
		zap.String("platform", platform),
		zap.String("remote", r.RemoteAddr))

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go s.pingLoop(ctx, ws, platform)

	defer func() {
		s.hub.Detach(platform, ws)
		_ = ws.Close(websocket.StatusNormalClosure, "")
		zap.L().Info("maim_message 适配器已断开", zap.String("platform", platform))
	}()

	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageText, websocket.MessageBinary:
			s.onFrame(ctx, platform, data)
		default:
			continue
		}
	}
}

func (s *Server) onFrame(ctx context.Context, connPlatform string, data []byte) {
	msg, err := parseMessageFrame(data)
	if err != nil {
		zap.L().Warn("maim_message 入站帧解析失败",
			zap.String("platform", connPlatform),
			zap.Error(err))
		return
	}
	if err := s.inbound(ctx, connPlatform, msg); err != nil {
		zap.L().Warn("maim_message 入站投递失败",
			zap.String("platform", connPlatform),
			zap.Error(err))
	}
}

func (s *Server) pingLoop(ctx context.Context, ws *websocket.Conn, platform string) {
	interval := s.pingInterval
	if interval <= 0 {
		interval = defaultPingInterval
	}
	wait := s.pongWait
	if wait <= 0 {
		wait = defaultPongWait
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pongCtx, cancel := context.WithTimeout(ctx, wait)
			err := ws.Ping(pongCtx)
			cancel()
			if err != nil {
				zap.L().Debug("maim_message 心跳失败，关闭连接",
					zap.String("platform", platform),
					zap.Error(err))
				_ = ws.Close(websocket.StatusPolicyViolation, "heartbeat timeout")
				return
			}
		}
	}
}

func (s *Server) validToken(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	cfg := s.config()
	candidate := []byte(token)
	for _, allowed := range cfg.Tokens {
		allowed = strings.TrimSpace(allowed)
		if len(allowed) != len(candidate) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(allowed), candidate) == 1 {
			return true
		}
	}
	return false
}
