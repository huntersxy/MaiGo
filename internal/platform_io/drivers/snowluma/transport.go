package snowluma

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Transport 正向 WebSocket 客户端（coder/websocket）
type Transport struct {
	mu            sync.Mutex
	cfg           ClientConfig
	profile       *ProfileState
	onOpen        func(ctx context.Context) error
	onClose       func(ctx context.Context) error
	onPayload     func(ctx context.Context, payload map[string]any) error
	conn          *websocket.Conn
	pending       map[string]chan map[string]any
	stopCh        chan struct{}
	loopDone      chan struct{}
	running       bool
	dialedOnce    bool
}

func NewTransport(
	onOpen func(ctx context.Context) error,
	onClose func(ctx context.Context) error,
	onPayload func(ctx context.Context, payload map[string]any) error,
) *Transport {
	return &Transport{
		onOpen:    onOpen,
		onClose:   onClose,
		onPayload: onPayload,
		pending:   make(map[string]chan map[string]any),
	}
}

func (t *Transport) Configure(cfg ClientConfig, profile *ProfileState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cfg = cfg
	t.profile = profile
}

func (t *Transport) Dialed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dialedOnce
}

func (t *Transport) Start(ctx context.Context) error {
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
	t.mu.Unlock()
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "stop")
	}
	t.failPending("连接已关闭")
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
	if t.onClose != nil {
		_ = t.onClose(ctx)
	}
	return nil
}

func (t *Transport) CallAction(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	t.mu.Lock()
	conn := t.conn
	cfg := t.cfg
	t.mu.Unlock()
	if conn == nil {
		return nil, fmt.Errorf("对端未连接")
	}
	if params == nil {
		params = map[string]any{}
	}
	echoID := uuid.NewString()
	respCh := make(chan map[string]any, 1)
	t.mu.Lock()
	t.pending[echoID] = respCh
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.pending, echoID)
		t.mu.Unlock()
	}()
	payload := map[string]any{"action": action, "params": params, "echo": echoID}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	writeCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.ActionTimeoutSec*float64(time.Second)))
	defer cancel()
	if err := conn.Write(writeCtx, websocket.MessageText, raw); err != nil {
		return nil, err
	}
	select {
	case resp := <-respCh:
		return resp, nil
	case <-writeCtx.Done():
		return nil, fmt.Errorf("动作超时: %s", action)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *Transport) connectionLoop(parent context.Context) {
	defer close(t.loopDone)
	for {
		t.mu.Lock()
		stopCh := t.stopCh
		cfg := t.cfg
		t.mu.Unlock()
		select {
		case <-stopCh:
			return
		case <-parent.Done():
			return
		default:
		}
		if err := t.dialOnce(parent, cfg); err != nil {
			zap.L().Warn("SnowLuma 适配器连接失败", zap.String("url", cfg.BuildWSURL()), zap.Error(err))
		}
		select {
		case <-stopCh:
			return
		case <-parent.Done():
			return
		case <-time.After(time.Duration(cfg.ReconnectDelaySec * float64(time.Second))):
		}
	}
}

func (t *Transport) dialOnce(parent context.Context, cfg ClientConfig) error {
	connectURL, header := buildConnect(cfg)
	t.mu.Lock()
	t.dialedOnce = true
	t.mu.Unlock()
	dialCtx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, connectURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.conn = conn
	stopCh := t.stopCh
	t.mu.Unlock()
	zap.L().Info("SnowLuma 适配器已连接", zap.String("url", displayURL(cfg)))
	defer func() {
		t.mu.Lock()
		if t.conn == conn {
			t.conn = nil
		}
		t.mu.Unlock()
		_ = conn.Close(websocket.StatusNormalClosure, "closed")
		t.failPending("连接已中断")
		if t.onClose != nil {
			_ = t.onClose(parent)
		}
	}()

	// 先起读循环再跑 onOpen：get_login_info 等 CallAction 依赖 echo 回包
	readDone := make(chan error, 1)
	go func() {
		readDone <- t.readLoop(parent, conn, stopCh, cfg)
	}()
	if t.onOpen != nil {
		if err := t.onOpen(parent); err != nil {
			zap.L().Warn("SnowLuma 连接就绪回调失败", zap.Error(err))
			_ = conn.Close(websocket.StatusNormalClosure, "onOpen failed")
			select {
			case <-readDone:
			case <-time.After(3 * time.Second):
			}
			return err
		}
	}
	select {
	case err := <-readDone:
		return err
	case <-stopCh:
		return nil
	case <-parent.Done():
		return nil
	}
}

func (t *Transport) readLoop(parent context.Context, conn *websocket.Conn, stopCh <-chan struct{}, cfg ClientConfig) error {
	for {
		select {
		case <-stopCh:
			return nil
		case <-parent.Done():
			return nil
		default:
		}
		readCtx, readCancel := context.WithTimeout(parent, time.Duration(cfg.HeartbeatInterval*2*float64(time.Second))+30*time.Second)
		_, data, err := conn.Read(readCtx)
		readCancel()
		if err != nil {
			return err
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			continue
		}
		if echoID := asString(payload["echo"]); echoID != "" {
			t.resolvePending(echoID, payload)
			continue
		}
		if t.onPayload != nil {
			go func(p map[string]any) {
				_ = t.onPayload(parent, p)
			}(payload)
		}
	}
}

func (t *Transport) resolvePending(echoID string, payload map[string]any) {
	t.mu.Lock()
	ch := t.pending[echoID]
	t.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- payload:
	default:
	}
}

func (t *Transport) failPending(reason string) {
	t.mu.Lock()
	pending := t.pending
	t.pending = make(map[string]chan map[string]any)
	t.mu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- map[string]any{"status": "failed", "message": reason}:
		default:
		}
	}
}

func buildConnect(cfg ClientConfig) (string, http.Header) {
	base := cfg.BuildWSURL()
	header := http.Header{}
	if cfg.Token == "" {
		return base, header
	}
	header.Set("Authorization", "Bearer "+cfg.Token)
	u, err := url.Parse(base)
	if err != nil {
		return base + "?access_token=" + url.QueryEscape(cfg.Token), header
	}
	q := u.Query()
	q.Set("access_token", cfg.Token)
	u.RawQuery = q.Encode()
	return u.String(), header
}

func displayURL(cfg ClientConfig) string {
	if cfg.Token == "" {
		return cfg.BuildWSURL()
	}
	return cfg.BuildWSURL() + "?access_token=***"
}
