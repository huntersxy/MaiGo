package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"maibot-go/internal/common/message_server"

	"github.com/coder/websocket"
	"go.uber.org/zap"
)

const (
	apiEnvelopeVersion = 1

	apiHeaderUUID     = "x-uuid"
	apiHeaderAPIKey   = "x-apikey"
	apiHeaderPlatform = "x-platform"

	apiCloseReasonAuthFailed = "认证失败"
	apiMessageCacheTTL       = time.Hour
)

// apiEnvelope 新 API Server 的帧信封
type apiEnvelope struct {
	Ver     int             `json:"ver"`
	MsgID   string          `json:"msg_id"`
	Type    string          `json:"type"`
	Meta    map[string]any  `json:"meta,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// RegisterCustomHandler 注册 custom_* 类型处理器；name 未带前缀时自动补齐
func (s *Server) RegisterCustomHandler(name string, handler APICustomHandler) {
	name = trimSpace(name)
	if name == "" || handler == nil {
		return
	}
	if !strings.HasPrefix(name, "custom_") {
		name = "custom_" + name
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.customHandlers == nil {
		s.customHandlers = make(map[string]APICustomHandler)
	}
	s.customHandlers[name] = handler
}

func (s *Server) handleAPIWS(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	query := r.URL.Query()
	platform := trimSpace(query.Get("platform"))
	if platform == "" {
		platform = trimSpace(r.Header.Get(apiHeaderPlatform))
	}
	if platform == "" {
		platform = "unknown"
	}
	s.apiStats.incAuthRequest()
	apiKey := trimSpace(query.Get("api_key"))
	if apiKey == "" {
		apiKey = trimSpace(r.Header.Get(apiHeaderAPIKey))
	}
	if cfg.APIKeyRequired() && !s.validAPIKey(apiKey) {
		s.apiStats.incAuthResult(false)
		http.Error(w, apiCloseReasonAuthFailed, http.StatusUnauthorized)
		return
	}
	s.apiStats.incAuthResult(true)
	uuid := trimSpace(r.Header.Get(apiHeaderUUID))
	if uuid == "" {
		uuid = randomHex(16)
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		OriginPatterns:     []string{"*"},
	})
	if err != nil {
		zap.L().Warn("maim_message API WS 握手失败", zap.Error(err))
		return
	}
	if limit := cfg.APIMaxMessageSize; limit > 0 {
		ws.SetReadLimit(limit)
	}

	identity := cfg.APIIdentityForKey(apiKey)
	conn := &apiConn{
		uuid:      uuid,
		userID:    cfg.APIUserIDOrDefault(),
		platform:  platform,
		accountID: identity.AccountID,
		scope:     identity.Scope,
		apiKey:    apiKey,
		ws:        ws,
	}
	s.apiHub.attach(conn)
	s.ensurePlatformRoutes(platform, identity.AccountID, identity.Scope)
	s.replayAPICache(r.Context(), conn)
	zap.L().Info("maim_message API 适配器已连接",
		zap.String("platform", platform),
		zap.String("uuid", uuid),
		zap.String("remote", r.RemoteAddr))

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go s.pingLoop(ctx, ws, platform)

	defer func() {
		s.apiHub.detach(conn)
		_ = ws.Close(websocket.StatusNormalClosure, "")
		zap.L().Info("maim_message API 适配器已断开", zap.String("platform", platform))
	}()

	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageText, websocket.MessageBinary:
			s.onAPIFrame(ctx, conn, data)
		default:
			continue
		}
	}
}

func (s *Server) onAPIFrame(ctx context.Context, conn *apiConn, data []byte) {
	var envelope apiEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		zap.L().Warn("maim_message API 帧解析失败",
			zap.String("platform", conn.platform),
			zap.Error(err))
		return
	}
	envelope.Type = trimSpace(envelope.Type)
	envelope.MsgID = trimSpace(envelope.MsgID)
	s.apiStats.incMessage()

	// 对端 ACK 用于从消息缓存移除已确认消息
	if envelope.Type == "sys_ack" {
		if s.apiCache != nil {
			if acked, ok := envelope.Meta["acked_msg_id"].(string); ok && trimSpace(acked) != "" {
				s.apiCache.Ack(trimSpace(acked))
			}
		}
		return
	}

	if envelope.MsgID != "" {
		s.sendAPIAck(ctx, conn, envelope.MsgID)
	}
	if envelope.MsgID != "" && s.apiMessageSeen(envelope.MsgID) {
		s.apiStats.incDuplicate()
		return
	}
	if envelope.MsgID != "" {
		s.rememberAPIMessage(envelope.MsgID)
	}

	metadata := map[string]any{
		"api_key":      conn.apiKey,
		"platform":     conn.platform,
		"uuid":         conn.uuid,
		"message_type": envelope.Type,
		"timestamp":    nowUnixFloat(),
	}

	s.apiStats.beginHandler()
	defer s.apiStats.endHandler()
	switch {
	case envelope.Type == "sys_std":
		s.handleAPIStandardMessage(ctx, conn, envelope.Payload)
	case strings.HasPrefix(envelope.Type, "custom_"):
		s.apiStats.incCustomMessage()
		s.handleAPICustomMessage(ctx, conn, envelope.Type, envelope.Payload, metadata)
	case strings.HasPrefix(envelope.Type, "sys_"):
		return
	default:
		zap.L().Debug("maim_message API 忽略未知帧类型",
			zap.String("type", envelope.Type),
			zap.String("uuid", conn.uuid))
	}
}

func (s *Server) handleAPIStandardMessage(ctx context.Context, conn *apiConn, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		// 非对象 payload 按文本包装，对齐 Python 的 str(payload) 分支
		apiMsg := maimmsg.APIMessageBase{
			MessageInfo: maimmsg.APIBaseMessageInfo{
				Platform:  conn.platform,
				MessageID: strconv.FormatInt(time.Now().Unix(), 10),
				Time:      nowUnixFloat(),
			},
			MessageSegment: maimmsg.SegText(strings.TrimSpace(string(raw))),
			MessageDim:     maimmsg.MessageDim{APIKey: conn.apiKey, Platform: conn.platform},
		}
		s.dispatchAPIInbound(ctx, conn, apiMsg)
		return
	}
	apiMsg := maimmsg.APIMessageBase{}
	_, hasInfo := payload["message_info"]
	_, hasSegment := payload["message_segment"]
	if hasInfo && hasSegment {
		if err := apiMsg.FromMap(payload); err != nil {
			zap.L().Warn("maim_message API 消息解析失败", zap.Error(err))
			return
		}
	} else {
		apiMsg = maimmsg.APIMessageBase{
			MessageInfo: maimmsg.APIBaseMessageInfo{
				Platform:  conn.platform,
				MessageID: strconv.FormatInt(time.Now().Unix(), 10),
				Time:      nowUnixFloat(),
			},
			MessageSegment: maimmsg.SegText(strings.TrimSpace(fmt.Sprint(payload))),
			MessageDim:     maimmsg.MessageDim{APIKey: conn.apiKey, Platform: conn.platform},
		}
	}
	s.dispatchAPIInbound(ctx, conn, apiMsg)
}

func (s *Server) dispatchAPIInbound(ctx context.Context, conn *apiConn, apiMsg maimmsg.APIMessageBase) {
	platform := trimSpace(apiMsg.MessageDim.Platform)
	if platform == "" {
		platform = trimSpace(apiMsg.MessageInfo.Platform)
	}
	if platform == "" {
		platform = conn.platform
	}
	apiKey := trimSpace(apiMsg.MessageDim.APIKey)
	if apiKey == "" {
		apiKey = conn.apiKey
	}
	s.apiHub.rememberKey(platform, apiKey)
	legacy := maimmsg.MessageConverter{}.FromAPIReceive(apiMsg)
	if trimSpace(legacy.MessageInfo.Platform) == "" {
		legacy.MessageInfo.Platform = platform
	}
	if legacy.MessageInfo.AdditionalConfig == nil {
		legacy.MessageInfo.AdditionalConfig = map[string]any{}
	}
	if conn.accountID != "" {
		if _, exists := legacy.MessageInfo.AdditionalConfig["platform_io_account_id"]; !exists {
			legacy.MessageInfo.AdditionalConfig["platform_io_account_id"] = conn.accountID
		}
	}
	if conn.scope != "" {
		if _, exists := legacy.MessageInfo.AdditionalConfig["platform_io_scope"]; !exists {
			legacy.MessageInfo.AdditionalConfig["platform_io_scope"] = conn.scope
		}
	}
	if err := s.inbound(ctx, platform, legacy); err != nil {
		zap.L().Warn("maim_message API 入站投递失败",
			zap.String("platform", platform),
			zap.Error(err))
	}
}

func (s *Server) handleAPICustomMessage(ctx context.Context, conn *apiConn, msgType string, raw json.RawMessage, metadata map[string]any) {
	s.stateMu.Lock()
	handler := s.customHandlers[msgType]
	s.stateMu.Unlock()
	if handler == nil {
		zap.L().Debug("maim_message API 未注册的自定义类型",
			zap.String("type", msgType),
			zap.String("uuid", conn.uuid))
		return
	}
	payload := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			payload["raw"] = string(raw)
		}
	}
	if err := handler(ctx, payload, metadata); err != nil {
		zap.L().Warn("maim_message API 自定义处理器失败",
			zap.String("type", msgType),
			zap.Error(err))
	}
}

func (s *Server) sendAPIAck(ctx context.Context, conn *apiConn, ackedMsgID string) {
	payload := map[string]any{
		"status":           "received",
		"server_timestamp": nowUnixFloat(),
	}
	meta := map[string]any{
		"uuid":         conn.uuid,
		"acked_msg_id": ackedMsgID,
		"timestamp":    nowUnixFloat(),
	}
	envelope := map[string]any{
		"ver":     apiEnvelopeVersion,
		"msg_id":  randomHex(16),
		"type":    "sys_ack",
		"meta":    meta,
		"payload": payload,
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return
	}
	if err := conn.write(ctx, raw); err != nil {
		zap.L().Debug("maim_message API ACK 写入失败", zap.Error(err))
	}
}

func (s *Server) validAPIKey(apiKey string) bool {
	apiKey = trimSpace(apiKey)
	if apiKey == "" {
		return false
	}
	cfg := s.config()
	candidate := []byte(apiKey)
	for _, allowed := range cfg.APIKeys {
		allowed = trimSpace(allowed)
		if len(allowed) != len(candidate) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(allowed), candidate) == 1 {
			return true
		}
	}
	if _, ok := cfg.APIKeyIdentities[apiKey]; ok {
		return true
	}
	return false
}

func (s *Server) replayAPICache(ctx context.Context, conn *apiConn) {
	if s.apiCache == nil || conn == nil {
		return
	}
	for _, entry := range s.apiCache.PendingByTarget(conn.uuid) {
		if err := conn.write(ctx, entry.Payload); err != nil {
			zap.L().Debug("maim_message API 缓存重放失败",
				zap.String("uuid", conn.uuid),
				zap.String("msg_id", entry.MsgID),
				zap.Error(err))
		}
	}
}

func (s *Server) apiMessageSeen(msgID string) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	seenAt, ok := s.seenAPIMessages[msgID]
	if !ok {
		return false
	}
	if time.Since(seenAt) > apiMessageCacheTTL {
		delete(s.seenAPIMessages, msgID)
		return false
	}
	return true
}

func (s *Server) rememberAPIMessage(msgID string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.seenAPIMessages == nil {
		s.seenAPIMessages = make(map[string]time.Time)
	}
	s.seenAPIMessages[msgID] = time.Now()
}

func newEnvelopeMsgID() string {
	return "msg_" + randomHex(6) + "_" + strconv.FormatInt(time.Now().Unix(), 10)
}

func nowUnixFloat() float64 {
	return float64(time.Now().UnixNano()) / float64(time.Second)
}

func randomHex(byteLen int) string {
	if byteLen <= 0 {
		byteLen = 8
	}
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf)
}

func trimSpace(raw string) string {
	return strings.TrimSpace(raw)
}
