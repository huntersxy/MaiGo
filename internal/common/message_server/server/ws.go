package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
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

	authorized := true
	if cfg.TokenRequired() {
		token := strings.TrimSpace(r.Header.Get(headerAuthorization))
		if token == "" {
			token = strings.TrimSpace(r.Header.Get(strings.ToLower(headerAuthorization)))
		}
		authorized = s.validToken(token)
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		OriginPatterns:     []string{"*"},
	})
	if err != nil {
		zap.L().Warn("maim_message WS 握手失败", zap.Error(err))
		return
	}
	if !authorized {
		// 对齐 Python 适配器可见行为：握手成功后以 1008 关闭并带上原因
		_ = ws.Close(websocket.StatusPolicyViolation, closeReasonInvalidToken)
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
	// 旧版自定义消息（如 message_id_echo）不进入 MessageBase 解析
	if s.dispatchLegacyCustom(ctx, connPlatform, data) {
		return
	}
	msg, err := parseMessageFrame(data)
	if err != nil {
		zap.L().Warn("maim_message 入站帧解析失败",
			zap.String("platform", connPlatform),
			zap.Error(err))
		return
	}
	// 适配器可能未发送 platform 头，或头与 message_info.platform 不一致；
	// 入站路由按消息自带的平台补齐，对齐 Python 不依赖连接头的处理方式
	if platform := strings.TrimSpace(msg.MessageInfo.Platform); platform != "" && platform != connPlatform {
		s.ensurePlatformRoutes(platform)
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

// dispatchLegacyCustom 分发旧版自定义消息帧
//
// 对齐 maim_message MessageServer.process_message：带 message_type_name 或
// is_custom_message 的帧交给注册的处理器，不进入普通消息链
func (s *Server) dispatchLegacyCustom(ctx context.Context, connPlatform string, data []byte) bool {
	var frame map[string]any
	if err := json.Unmarshal(data, &frame); err != nil {
		return false
	}
	name := frameString(frame["message_type_name"])
	if name == "" {
		if content, ok := frame["content"].(map[string]any); ok && frameString(content["type"]) == "echo" {
			name = "message_id_echo"
		}
	}
	if name == "" {
		return false
	}
	if custom, _ := frame["is_custom_message"].(bool); !custom && frame["message_type_name"] == nil {
		return false
	}
	s.stateMu.Lock()
	handler := s.customHandlers[name]
	if handler == nil {
		handler = s.customHandlers["custom_"+name]
	}
	s.stateMu.Unlock()
	if handler == nil {
		zap.L().Debug("maim_message 未注册的旧版自定义消息",
			zap.String("type", name),
			zap.String("platform", connPlatform))
		return true
	}
	metadata := map[string]any{
		"platform":  connPlatform,
		"timestamp": nowUnixFloat(),
	}
	if err := handler(ctx, frame, metadata); err != nil {
		zap.L().Warn("maim_message 旧版自定义消息处理失败",
			zap.String("type", name),
			zap.Error(err))
	}
	return true
}

func frameString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
