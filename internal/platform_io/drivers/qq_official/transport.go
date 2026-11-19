package qq_official

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"go.uber.org/zap"
)

// Transport QQ 官方正向 WebSocket
type Transport struct {
	mu            sync.Mutex
	client        *Client
	reconnectSec  float64
	onPayload     func(ctx context.Context, op int, event string, data map[string]any, seq *int) error
	onReady       func(ctx context.Context, accountID, botName, sessionID string) error
	onUnready     func(ctx context.Context) error
	conn          *websocket.Conn
	stopCh        chan struct{}
	loopDone      chan struct{}
	running       bool
	dialedOnce    atomic.Bool
	sessionID     string
	seq           *int
	resuming      bool
	heartbeatAck  bool
	heartbeatStop chan struct{}
	accessToken   string
}

func NewTransport(
	client *Client,
	reconnectSec float64,
	onPayload func(ctx context.Context, op int, event string, data map[string]any, seq *int) error,
	onReady func(ctx context.Context, accountID, botName, sessionID string) error,
	onUnready func(ctx context.Context) error,
) *Transport {
	if reconnectSec <= 0 {
		reconnectSec = DefaultReconnectSec
	}
	return &Transport{
		client:       client,
		reconnectSec: reconnectSec,
		onPayload:    onPayload,
		onReady:      onReady,
		onUnready:    onUnready,
	}
}

func (t *Transport) Dialed() bool {
	return t != nil && t.dialedOnce.Load()
}

func (t *Transport) Start(ctx context.Context) error {
	if t == nil {
		return fmt.Errorf("transport 为空")
	}
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return nil
	}
	t.running = true
	t.stopCh = make(chan struct{})
	t.loopDone = make(chan struct{})
	t.mu.Unlock()
	go t.connectionLoop(ctx)
	return nil
}

func (t *Transport) Stop(ctx context.Context) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	if !t.running {
		t.mu.Unlock()
		return nil
	}
	close(t.stopCh)
	conn := t.conn
	t.conn = nil
	t.running = false
	done := t.loopDone
	hbStop := t.heartbeatStop
	t.mu.Unlock()
	if hbStop != nil {
		select {
		case <-hbStop:
		default:
			close(hbStop)
		}
	}
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "stop")
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
	if t.onUnready != nil {
		_ = t.onUnready(ctx)
	}
	return nil
}

func (t *Transport) connectionLoop(parent context.Context) {
	defer close(t.loopDone)
	for {
		t.mu.Lock()
		stopCh := t.stopCh
		delay := t.reconnectSec
		t.mu.Unlock()
		select {
		case <-stopCh:
			return
		case <-parent.Done():
			return
		default:
		}
		closeCode, err := t.connectOnce(parent)
		if err != nil {
			zap.L().Warn("QQ 官方网关连接失败，将重试", zap.Error(err))
		}
		if !t.shouldRetry(closeCode) {
			return
		}
		if closeCode == 4008 {
			if delay < 60 {
				delay = 60
			}
		}
		select {
		case <-stopCh:
			return
		case <-parent.Done():
			return
		case <-time.After(time.Duration(delay * float64(time.Second))):
		}
	}
}

func (t *Transport) shouldRetry(code int) bool {
	if code == 4914 || code == 4915 {
		zap.L().Error("QQ 官方网关不可恢复地断开", zap.Int("code", code))
		return false
	}
	return true
}

func (t *Transport) connectOnce(parent context.Context) (closeCode int, err error) {
	if t.client == nil {
		return 0, fmt.Errorf("QQ client 为空")
	}
	t.dialedOnce.Store(true)
	token, err := t.client.Authorize(parent)
	if err != nil {
		return 0, err
	}
	gatewayURL, err := t.client.GatewayURL(parent)
	if err != nil {
		return 0, err
	}
	t.mu.Lock()
	t.accessToken = token
	t.mu.Unlock()

	dialCtx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, gatewayURL, nil)
	if err != nil {
		return 0, err
	}
	t.mu.Lock()
	t.conn = conn
	stopCh := t.stopCh
	t.mu.Unlock()
	defer func() {
		t.stopHeartbeat()
		t.mu.Lock()
		if t.conn == conn {
			t.conn = nil
		}
		t.mu.Unlock()
		_ = conn.Close(websocket.StatusNormalClosure, "closed")
		if t.onUnready != nil {
			_ = t.onUnready(parent)
		}
	}()

	for {
		select {
		case <-stopCh:
			return 0, nil
		case <-parent.Done():
			return 0, nil
		default:
		}
		readCtx, readCancel := context.WithTimeout(parent, 120*time.Second)
		_, data, readErr := conn.Read(readCtx)
		readCancel()
		if readErr != nil {
			return closeCodeFromErr(readErr), readErr
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			continue
		}
		if err := t.handlePayload(parent, payload); err != nil {
			zap.L().Warn("QQ 网关帧处理失败", zap.Error(err))
		}
	}
}

func closeCodeFromErr(err error) int {
	if err == nil {
		return 0
	}
	var ce websocket.CloseError
	if ok := asCloseError(err, &ce); ok {
		return int(ce.Code)
	}
	return 0
}

func asCloseError(err error, target *websocket.CloseError) bool {
	if err == nil || target == nil {
		return false
	}
	if ce, ok := err.(websocket.CloseError); ok {
		*target = ce
		return true
	}
	if ce, ok := err.(*websocket.CloseError); ok && ce != nil {
		*target = *ce
		return true
	}
	return false
}

func (t *Transport) handlePayload(ctx context.Context, payload map[string]any) error {
	op, _ := asInt(payload["op"])
	data, _ := asMap(payload["d"])
	switch op {
	case 10:
		return t.handleHello(ctx, data)
	case 0:
		var seqPtr *int
		if s, ok := asInt(payload["s"]); ok {
			seq := s
			t.mu.Lock()
			t.seq = &seq
			t.mu.Unlock()
			seqPtr = &seq
		}
		event := asString(payload["t"])
		return t.handleDispatch(ctx, event, data, seqPtr)
	case 1:
		return t.sendWS(ctx, map[string]any{"op": 1, "d": t.currentSeq()})
	case 11:
		t.mu.Lock()
		t.heartbeatAck = true
		t.mu.Unlock()
		return nil
	case 7, 9:
		// INVALID_SESSION：d=true 允许 Resume；d≠true 时重置会话并重新鉴权
		if op == 9 && !payloadBoolTrue(payload["d"]) {
			t.resetSession()
			if t.client != nil {
				t.client.InvalidateToken()
			}
		}
		t.mu.Lock()
		conn := t.conn
		t.mu.Unlock()
		if conn != nil {
			_ = conn.Close(websocket.StatusNormalClosure, "reconnect")
		}
		return nil
	default:
		return nil
	}
}

func (t *Transport) handleHello(ctx context.Context, data map[string]any) error {
	intervalMS, ok := asFloat(data["heartbeat_interval"])
	if !ok || intervalMS <= 0 {
		return fmt.Errorf("QQ 网关 Hello 缺少心跳间隔")
	}
	interval := intervalMS / 1000
	t.stopHeartbeat()
	t.mu.Lock()
	t.heartbeatAck = true
	t.heartbeatStop = make(chan struct{})
	hbStop := t.heartbeatStop
	sessionID := t.sessionID
	seq := t.seq
	token := t.accessToken
	t.mu.Unlock()
	go t.heartbeatLoop(ctx, interval, hbStop)
	if sessionID != "" && seq != nil {
		t.mu.Lock()
		t.resuming = true
		t.mu.Unlock()
		return t.sendWS(ctx, map[string]any{
			"op": 6,
			"d": map[string]any{
				"token":      "QQBot " + token,
				"session_id": sessionID,
				"seq":        *seq,
			},
		})
	}
	return t.sendWS(ctx, map[string]any{
		"op": 2,
		"d": map[string]any{
			"token":   "QQBot " + token,
			"intents": MessageIntents,
			"shard":   []int{0, 1},
		},
	})
}

func (t *Transport) handleDispatch(ctx context.Context, event string, data map[string]any, seq *int) error {
	switch event {
	case "READY":
		user, _ := asMap(data["user"])
		sessionID := asString(data["session_id"])
		accountID := asString(user["id"])
		botName := asString(user["username"])
		if sessionID == "" || accountID == "" {
			return fmt.Errorf("QQ 网关 READY 缺少账号或会话 ID")
		}
		t.mu.Lock()
		t.sessionID = sessionID
		t.resuming = false
		t.mu.Unlock()
		if t.onReady != nil {
			return t.onReady(ctx, accountID, botName, sessionID)
		}
		return nil
	case "RESUMED":
		t.mu.Lock()
		t.resuming = false
		t.mu.Unlock()
		if t.onReady != nil {
			// account/botName 在 resume 时已有；传空由上层保留
			return t.onReady(ctx, "", "", t.sessionID)
		}
		return nil
	default:
		t.mu.Lock()
		resuming := t.resuming
		t.mu.Unlock()
		if resuming && isSupportedEvent(event) {
			t.mu.Lock()
			t.resuming = false
			t.mu.Unlock()
			if t.onReady != nil {
				_ = t.onReady(ctx, "", "", t.sessionID)
			}
		}
		if t.onPayload != nil {
			return t.onPayload(ctx, 0, event, data, seq)
		}
		return nil
	}
}

func (t *Transport) heartbeatLoop(ctx context.Context, interval float64, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(interval * float64(time.Second))):
		}
		t.mu.Lock()
		ack := t.heartbeatAck
		t.heartbeatAck = false
		conn := t.conn
		t.mu.Unlock()
		if !ack {
			zap.L().Warn("QQ 网关心跳未确认，重新建立连接")
			if conn != nil {
				_ = conn.Close(websocket.StatusNormalClosure, "heartbeat timeout")
			}
			return
		}
		if err := t.sendWS(ctx, map[string]any{"op": 1, "d": t.currentSeq()}); err != nil {
			zap.L().Warn("QQ 网关心跳发送失败", zap.Error(err))
			if conn != nil {
				_ = conn.Close(websocket.StatusNormalClosure, "heartbeat send failed")
			}
			return
		}
	}
}

func (t *Transport) stopHeartbeat() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.heartbeatStop != nil {
		select {
		case <-t.heartbeatStop:
		default:
			close(t.heartbeatStop)
		}
		t.heartbeatStop = nil
	}
}

func (t *Transport) sendWS(ctx context.Context, payload map[string]any) error {
	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("QQ WebSocket 未连接")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, raw)
}

func (t *Transport) currentSeq() any {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seq == nil {
		return nil
	}
	return *t.seq
}

func (t *Transport) resetSession() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessionID = ""
	t.seq = nil
	t.resuming = false
}

func payloadBoolTrue(v any) bool {
	switch typed := v.(type) {
	case bool:
		return typed
	case map[string]any:
		return false
	default:
		return asBool(typed)
	}
}
