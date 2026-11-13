package websocket

import (
	"context"
	"fmt"
	webuichat "maibot-go/internal/webui/routers/chat"
	webuiplugin "maibot-go/internal/webui/routers/plugin"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/coder/websocket"
	"go.uber.org/zap"

	"maibot-go/internal/common/logger"
	"maibot-go/internal/maisaka/monitor"
	"maibot-go/internal/plugin_market"
	"maibot-go/internal/webui/authutil"
)

func (d Deps) handleWSLogs(w http.ResponseWriter, r *http.Request) {
	sessionToken := authutil.HandshakeSession(d.AdminAuth, r, d.SessionTokenValid)
	if sessionToken == "" {
		// 同统一 WS：先握手再以 4001 关闭，前端才能跳回登录页
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns: []string{"*"},
		})
		if err != nil {
			zap.L().Debug("WebUI 日志 WebSocket 升级失败", zap.Error(err))
			return
		}
		_ = conn.Close(wsAuthFailedCloseCode, "认证失败，请重新登录")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		zap.L().Debug("WebUI 日志 WebSocket 升级失败", zap.Error(err))
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := r.Context()
	sendMu := sync.Mutex{}
	writeText := func(payload string) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return conn.Write(ctx, websocket.MessageText, []byte(payload))
	}

	for _, entry := range logger.RecentWebUIEntries(100) {
		raw, err := sonic.MarshalString(entry)
		if err != nil {
			continue
		}
		if err := writeText(raw); err != nil {
			return
		}
	}

	logCh, unsub := logger.SubscribeWebUI()
	defer unsub()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			// 会话失效后前端不会主动重连，这里复核后以 4001 关闭
			if !d.sessionTokenValid(sessionToken) {
				_ = conn.Close(wsAuthFailedCloseCode, "认证失败，请重新登录")
				return
			}
			if strings.TrimSpace(string(data)) == "ping" {
				_ = writeText("pong")
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-readDone:
			return
		case entry, ok := <-logCh:
			if !ok {
				return
			}
			raw, err := sonic.MarshalString(entry)
			if err != nil {
				continue
			}
			if err := writeText(raw); err != nil {
				return
			}
		}
	}
}

// wsAuthFailedCloseCode 对齐 Python websocket.close(code=4001, reason="认证失败，请重新登录")；
// 前端据此跳回 /auth，直接回 HTTP 401 浏览器只会得到 1006，不会触发重新登录
const wsAuthFailedCloseCode = websocket.StatusCode(4001)

func (d Deps) handleWSUnified(w http.ResponseWriter, r *http.Request) {
	sessionToken := authutil.HandshakeSession(d.AdminAuth, r, d.SessionTokenValid)
	authorized := sessionToken != ""
	if !authorized {
		// 先完成握手再关闭，客户端才能读到 4001；否则只能看到握手失败
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns: []string{"*"},
		})
		if err != nil {
			zap.L().Debug("WebUI 统一 WebSocket 升级失败", zap.Error(err))
			return
		}
		_ = conn.Close(wsAuthFailedCloseCode, "认证失败，请重新登录")
		zap.L().Warn("统一 WebSocket 连接被拒绝：认证失败")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		zap.L().Debug("WebUI 统一 WebSocket 升级失败", zap.Error(err))
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := r.Context()
	connectionID := time.Now().Format("20060102150405.000")
	session := &unifiedWSSession{
		deps:          d,
		conn:          conn,
		ctx:           ctx,
		sessionToken:  sessionToken,
		subs:          make(map[string]struct{}),
		writeMu:       sync.Mutex{},
		plannerDelta:  monitor.NewPlannerDeltaEncoder(),
	}

	defer session.stopAllStreams()

	_ = session.writeJSON(map[string]any{
		"op":     "event",
		"domain": "system",
		"event":  "ready",
		"data": map[string]any{
			"connection_id": connectionID,
			"timestamp":     float64(time.Now().UnixNano()) / 1e9,
		},
	})

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			session.closeAllChatSessions()
			session.stopAllStreams()
			return
		}
		// 会话过期后前端不会主动重连，这里每条消息复核一次，失效即以 4001 关闭
		if !session.sessionStillValid() {
			zap.L().Info("统一 WebSocket 会话已失效，关闭连接等待重新登录")
			session.closeAllChatSessions()
			session.stopAllStreams()
			_ = conn.Close(wsAuthFailedCloseCode, "认证失败，请重新登录")
			return
		}
		var message map[string]any
		if err := sonic.Unmarshal(data, &message); err != nil {
			_ = session.sendResponse(nil, false, nil, map[string]any{
				"code":    "invalid_message",
				"message": "消息必须是 JSON 对象",
			})
			continue
		}
		if err := session.handleMessage(message); err != nil {
			return
		}
	}
}

// sessionStillValid 复核握手时的会话令牌是否仍有效
func (s *unifiedWSSession) sessionStillValid() bool {
	if s == nil || s.deps.AdminAuth == nil {
		return true
	}
	return s.deps.sessionTokenValid(s.sessionToken)
}

type unifiedWSSession struct {
	deps Deps
	conn *websocket.Conn
	ctx  context.Context
	// sessionToken 握手依据的会话令牌，每条消息前复核是否仍有效
	sessionToken       string
	subs               map[string]struct{}
	writeMu            sync.Mutex
	logUnsub           func()
	logStreamOff       context.CancelFunc
	monitorUnsub       func()
	monitorStreamOff   context.CancelFunc
	pluginRuntimeUnsub func()
	pluginProgressCh   <-chan pluginmarket.ProgressUpdate
	pluginProgressOff  func()
	chatSessions       map[string]*chatSessionState
	// plannerDelta 连接级 Planner 快照增量编码，对齐 Python 每条连接独立基准
	plannerDelta *monitor.PlannerDeltaEncoder
}

func (s *unifiedWSSession) subKey(domain, topic string) string {
	return domain + ":" + topic
}

func (s *unifiedWSSession) writeJSON(payload map[string]any) error {
	raw, err := sonic.MarshalString(payload)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.conn.Write(s.ctx, websocket.MessageText, []byte(raw))
}

func (s *unifiedWSSession) sendResponse(requestID any, ok bool, data map[string]any, errPayload map[string]any) error {
	msg := map[string]any{
		"op": "response",
		"id": requestID,
		"ok": ok,
	}
	if data != nil {
		msg["data"] = data
	}
	if errPayload != nil {
		msg["error"] = errPayload
	}
	return s.writeJSON(msg)
}

func (s *unifiedWSSession) sendEvent(domain, event, topic string, data map[string]any) error {
	msg := map[string]any{
		"op":     "event",
		"domain": domain,
		"event":  event,
		"data":   data,
	}
	if topic != "" {
		msg["topic"] = topic
	}
	return s.writeJSON(msg)
}

func (s *unifiedWSSession) handleMessage(message map[string]any) error {
	op := strings.TrimSpace(wsAnyString(message["op"]))
	requestID := message["id"]
	switch op {
	case "ping":
		return s.writeJSON(map[string]any{"op": "pong", "ts": float64(time.Now().UnixNano()) / 1e9})
	case "call":
		return s.handleCall(requestID, message)
	case "subscribe":
		return s.handleSubscribe(requestID, message)
	case "unsubscribe":
		return s.handleUnsubscribe(requestID, message)
	default:
		return s.sendResponse(requestID, false, nil, map[string]any{
			"code":    "unsupported_operation",
			"message": "不支持的操作: " + op,
		})
	}
}

// handleCall 分发统一调用请求
func (s *unifiedWSSession) handleCall(requestID any, message map[string]any) error {
	domain := strings.TrimSpace(wsAnyString(message["domain"]))
	if domain == "chat" {
		return s.handleChatCall(requestID, message)
	}
	if domain == monitor.Domain {
		return s.handleMonitorCall(requestID, message)
	}
	return s.sendResponse(requestID, false, nil, map[string]any{
		"code":    "unsupported_domain",
		"message": "不支持的调用域: " + domain,
	})
}

func (s *unifiedWSSession) handleSubscribe(requestID any, message map[string]any) error {
	domain := strings.TrimSpace(wsAnyString(message["domain"]))
	topic := strings.TrimSpace(wsAnyString(message["topic"]))
	data, _ := message["data"].(map[string]any)
	if domain == "logs" && topic == "main" {
		replay := 100
		if data != nil {
			replay = coerceReplayLimit(data["replay"], 100)
		}
		s.subs[s.subKey(domain, topic)] = struct{}{}
		if err := s.sendResponse(requestID, true, map[string]any{
			"domain": domain,
			"topic":  topic,
		}, nil); err != nil {
			return err
		}
		entries := logger.RecentWebUIEntries(replay)
		payload := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			payload = append(payload, webUIEntryMap(entry))
		}
		if err := s.sendEvent("logs", "snapshot", "main", map[string]any{"entries": payload}); err != nil {
			return err
		}
		s.ensureLogStream()
		return nil
	}
	if domain == "chat" && topic == "main" {
		limit := 200
		offset := 0
		if data != nil {
			limit = coerceReplayLimit(data["limit"], 200)
			offset = coerceReplayLimit(data["offset"], 0)
		}
		s.subs[s.subKey(domain, topic)] = struct{}{}
		if err := s.sendResponse(requestID, true, map[string]any{
			"domain": domain,
			"topic":  topic,
		}, nil); err != nil {
			return err
		}
		return s.pushChatSessionsSnapshot(limit, offset)
	}
	if domain == webuiplugin.ProgressDomain && topic == webuiplugin.ProgressTopic {
		s.subs[s.subKey(domain, topic)] = struct{}{}
		if err := s.sendResponse(requestID, true, map[string]any{
			"domain": domain,
			"topic":  topic,
		}, nil); err != nil {
			return err
		}
		s.ensurePluginProgressStream()
		return s.sendEvent(webuiplugin.ProgressDomain, "snapshot", webuiplugin.ProgressTopic, map[string]any{
			"progress": webuiplugin.DefaultProgressHub().Snapshot(),
		})
	}
	if domain == pluginRuntimeDomain && topic == pluginRuntimeTopic {
		if s.deps.PluginHost == nil {
			return s.sendResponse(requestID, false, nil, map[string]any{
				"code":    "plugin_runtime_unavailable",
				"message": "插件运行时未接线",
			})
		}
		s.stopPluginRuntimeStream()
		s.subs[s.subKey(domain, topic)] = struct{}{}
		s.pluginRuntimeUnsub = s.deps.PluginHost.SubscribeStatusChanges(func() {
			_ = s.sendEvent(pluginRuntimeDomain, "changed", pluginRuntimeTopic, map[string]any{})
		})
		return s.sendResponse(requestID, true, map[string]any{
			"domain": pluginRuntimeDomain,
			"topic":  pluginRuntimeTopic,
		}, nil)
	}
	if domain == monitor.Domain && topic == monitor.Topic {
		return s.subscribeMonitor(requestID, data)
	}
	return s.sendResponse(requestID, false, nil, map[string]any{
		"code":    "unsupported_subscription",
		"message": "不支持的订阅目标: " + domain + ":" + topic,
	})
}

func (s *unifiedWSSession) handleUnsubscribe(requestID any, message map[string]any) error {
	domain := strings.TrimSpace(wsAnyString(message["domain"]))
	topic := strings.TrimSpace(wsAnyString(message["topic"]))
	if domain == "" || topic == "" {
		return s.sendResponse(requestID, false, nil, map[string]any{
			"code":    "invalid_unsubscribe",
			"message": "退订请求缺少 domain 或 topic",
		})
	}
	delete(s.subs, s.subKey(domain, topic))
	if domain == "logs" && topic == "main" {
		s.stopLogStream()
	}
	if domain == monitor.Domain && topic == monitor.Topic {
		s.stopMonitorStream()
	}
	if domain == pluginRuntimeDomain && topic == pluginRuntimeTopic {
		s.stopPluginRuntimeStream()
	}
	if domain == webuiplugin.ProgressDomain && topic == webuiplugin.ProgressTopic {
		s.stopPluginProgressStream()
	}
	return s.sendResponse(requestID, true, map[string]any{
		"domain": domain,
		"topic":  topic,
	}, nil)
}

func (s *unifiedWSSession) ensureLogStream() {
	if s.logStreamOff != nil {
		return
	}
	streamCtx, cancel := context.WithCancel(s.ctx)
	s.logStreamOff = cancel
	logCh, unsub := logger.SubscribeWebUI()
	s.logUnsub = unsub
	go func() {
		defer unsub()
		for {
			select {
			case <-streamCtx.Done():
				return
			case entry, ok := <-logCh:
				if !ok {
					return
				}
				if _, subscribed := s.subs[s.subKey("logs", "main")]; !subscribed {
					continue
				}
				_ = s.sendEvent("logs", "entry", "main", map[string]any{
					"entry": webUIEntryMap(entry),
				})
			}
		}
	}()
}

func (s *unifiedWSSession) stopLogStream() {
	if s.logStreamOff != nil {
		s.logStreamOff()
		s.logStreamOff = nil
	}
	if s.logUnsub != nil {
		s.logUnsub()
		s.logUnsub = nil
	}
}

func (s *unifiedWSSession) stopAllStreams() {
	s.stopLogStream()
	s.stopMonitorStream()
	s.stopPluginRuntimeStream()
	s.stopPluginProgressStream()
}

func (s *unifiedWSSession) ensurePluginProgressStream() {
	if s.pluginProgressOff != nil {
		return
	}
	streamCtx, cancel := context.WithCancel(s.ctx)
	s.pluginProgressOff = cancel
	ch, unsub := webuiplugin.DefaultProgressHub().Subscribe()
	s.pluginProgressCh = ch
	go func() {
		defer unsub()
		for {
			select {
			case <-streamCtx.Done():
				return
			case update, ok := <-ch:
				if !ok {
					return
				}
				if _, subscribed := s.subs[s.subKey(webuiplugin.ProgressDomain, webuiplugin.ProgressTopic)]; !subscribed {
					continue
				}
				_ = s.sendEvent(webuiplugin.ProgressDomain, "update", webuiplugin.ProgressTopic, map[string]any{
					"progress": update,
				})
			}
		}
	}()
}

func (s *unifiedWSSession) stopPluginProgressStream() {
	if s.pluginProgressOff != nil {
		s.pluginProgressOff()
		s.pluginProgressOff = nil
	}
	s.pluginProgressCh = nil
}

const (
	pluginRuntimeDomain = "plugin_runtime"
	pluginRuntimeTopic  = "main"
)

func (s *unifiedWSSession) stopPluginRuntimeStream() {
	if s.pluginRuntimeUnsub != nil {
		s.pluginRuntimeUnsub()
		s.pluginRuntimeUnsub = nil
	}
}

func (s *unifiedWSSession) pushChatSessionsSnapshot(limit, offset int) error {
	if s.deps.Memory == nil {
		return s.sendEvent("chat", "sessions.snapshot", "main", map[string]any{
			"sessions":  []any{},
			"total":     0,
			"timestamp": float64(time.Now().UnixNano()) / 1e9,
			"note":      "记忆管理器未初始化",
		})
	}
	result, err := webuichat.ListChatSessions(s.ctx, s.deps.Memory, limit, offset)
	if err != nil {
		return s.sendEvent("chat", "sessions.snapshot", "main", map[string]any{
			"sessions":  []any{},
			"total":     0,
			"timestamp": float64(time.Now().UnixNano()) / 1e9,
			"error":     err.Error(),
		})
	}
	sessions := make([]map[string]any, 0, len(result.Items))
	for _, item := range result.Items {
		sessions = append(sessions, map[string]any{
			"session_id":       item.SessionID,
			"conversation_key": item.ConversationKey,
			"kind":             item.Kind,
			"target_id":        item.TargetID,
			"name":             item.Name,
			"active":           item.Active,
			"blocked":          item.Blocked,
		})
	}
	return s.sendEvent("chat", "sessions.snapshot", "main", map[string]any{
		"sessions":  sessions,
		"total":     result.Total,
		"timestamp": float64(time.Now().UnixNano()) / 1e9,
	})
}

func (s *unifiedWSSession) ensureMonitorStream() {
	if s.monitorStreamOff != nil {
		return
	}
	streamCtx, cancel := context.WithCancel(s.ctx)
	s.monitorStreamOff = cancel
	evCh, unsub := monitor.Subscribe()
	s.monitorUnsub = unsub
	go func() {
		defer unsub()
		for {
			select {
			case <-streamCtx.Done():
				return
			case ev, ok := <-evCh:
				if !ok {
					return
				}
				if _, subscribed := s.subs[s.subKey(monitor.Domain, monitor.Topic)]; !subscribed {
					continue
				}
				_ = s.sendMonitorEvent(ev)
			}
		}
	}()
}

// sendMonitorEvent 按连接级增量编码后下发监控事件
//
// planner.progress/finalized 首次发送完整快照，之后只发送变化字段；编码与写出在同一
// 临界区内完成，保证基准顺序与实际下发顺序一致。
func (s *unifiedWSSession) sendMonitorEvent(ev monitor.Event) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	data := ev.Data
	if ev.Event == "planner.reset" {
		if s.plannerDelta != nil {
			s.plannerDelta.Reset()
		}
	} else if s.plannerDelta != nil {
		encoded, err := s.plannerDelta.Encode(map[string]any{
			"domain": monitor.Domain,
			"event":  ev.Event,
			"data":   ev.Data,
		})
		if err != nil {
			// 增量依赖上一帧，坏帧不能跳过，直接断开让前端重连后从完整快照恢复
			zap.L().Warn("Planner 增量编码失败，关闭监控连接", zap.String("event", ev.Event), zap.Error(err))
			return err
		}
		if event, ok := encoded["event"].(string); ok {
			ev.Event = event
		}
		if encodedData, ok := encoded["data"].(map[string]any); ok {
			data = encodedData
		}
	}
	msg := map[string]any{
		"op":     "event",
		"domain": monitor.Domain,
		"event":  ev.Event,
		"topic":  monitor.Topic,
		"data":   data,
	}
	raw, err := sonic.MarshalString(msg)
	if err != nil {
		return err
	}
	return s.conn.Write(s.ctx, websocket.MessageText, []byte(raw))
}

func (s *unifiedWSSession) stopMonitorStream() {
	if s.monitorStreamOff != nil {
		s.monitorStreamOff()
		s.monitorStreamOff = nil
	}
	if s.monitorUnsub != nil {
		s.monitorUnsub()
		s.monitorUnsub = nil
	}
}

func webUIEntryMap(entry logger.WebUIEntry) map[string]any {
	return map[string]any{
		"id":        entry.ID,
		"timestamp": entry.Timestamp,
		"level":     entry.Level,
		"module":    entry.Module,
		"message":   entry.Message,
	}
}

func coerceReplayLimit(value any, defaultLimit int) int {
	switch v := value.(type) {
	case float64:
		return clampReplay(int(v), defaultLimit)
	case int:
		return clampReplay(v, defaultLimit)
	case int64:
		return clampReplay(int(v), defaultLimit)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return clampReplay(parsed, defaultLimit)
		}
	}
	return clampReplay(defaultLimit, defaultLimit)
}

func clampReplay(limit, defaultLimit int) int {
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > 500 {
		limit = 500
	}
	return limit
}

func wsAnyString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}
