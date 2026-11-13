package htmlrender

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/coder/websocket"
)

type cdpError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *cdpError) Error() string {
	if e == nil {
		return ""
	}
	if len(e.Data) > 0 {
		return fmt.Sprintf("CDP 错误 %d: %s (%s)", e.Code, e.Message, string(e.Data))
	}
	return fmt.Sprintf("CDP 错误 %d: %s", e.Code, e.Message)
}

type cdpReply struct {
	result json.RawMessage
	err    error
}

type cdpEnvelope struct {
	ID        int             `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
}

type cdpEventEnvelope struct {
	ID        int             `json:"id"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params"`
	SessionID string          `json:"sessionId"`
	Result    json.RawMessage `json:"result"`
	Error     *cdpError       `json:"error"`
}

type cdpEventHandler func(params []byte) error

// cdpClient 最小 Chrome DevTools Protocol 客户端
//
// 只依赖 coder/websocket，不引入 chromedp；命令按 id 复用同一 WebSocket
type cdpClient struct {
	conn *websocket.Conn

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int
	pending map[int]chan cdpReply

	eventMu  sync.RWMutex
	handlers map[string]map[string][]cdpEventHandler

	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error
}

func dialCDP(ctx context.Context, endpoint string) (*cdpClient, error) {
	conn, _, err := websocket.Dial(ctx, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("连接 CDP WebSocket 失败: %w", err)
	}
	conn.SetReadLimit(64 << 20)
	client := &cdpClient{
		conn:     conn,
		pending:  make(map[int]chan cdpReply),
		handlers: make(map[string]map[string][]cdpEventHandler),
		closed:   make(chan struct{}),
	}
	go client.readLoop()
	return client, nil
}

func (c *cdpClient) readLoop() {
	readCtx := context.Background()
	for {
		_, data, err := c.conn.Read(readCtx)
		if err != nil {
			c.fail(fmt.Errorf("CDP WebSocket 读取失败: %w", err))
			return
		}
		var envelope cdpEventEnvelope
		if err := sonic.Unmarshal(data, &envelope); err != nil {
			continue
		}
		if envelope.ID > 0 {
			var replyErr error
			if envelope.Error != nil {
				replyErr = envelope.Error
			}
			c.deliver(envelope.ID, cdpReply{result: envelope.Result, err: replyErr})
			continue
		}
		if envelope.Method == "" {
			continue
		}
		c.dispatchEvent(envelope.SessionID, envelope.Method, envelope.Params)
	}
}

func (c *cdpClient) deliver(id int, reply cdpReply) {
	c.mu.Lock()
	ch := c.pending[id]
	if ch != nil {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if ch != nil {
		ch <- reply
	}
}

func (c *cdpClient) dispatchEvent(sessionID, method string, params json.RawMessage) {
	c.eventMu.RLock()
	byMethod := c.handlers[sessionID]
	handlers := append([]cdpEventHandler(nil), byMethod[method]...)
	c.eventMu.RUnlock()
	for _, handler := range handlers {
		handler := handler
		go func() {
			_ = handler(params)
		}()
	}
}

func (c *cdpClient) onEvent(sessionID, method string, handler cdpEventHandler) {
	if handler == nil {
		return
	}
	c.eventMu.Lock()
	byMethod := c.handlers[sessionID]
	if byMethod == nil {
		byMethod = make(map[string][]cdpEventHandler)
		c.handlers[sessionID] = byMethod
	}
	byMethod[method] = append(byMethod[method], handler)
	c.eventMu.Unlock()
}

func (c *cdpClient) removeSession(sessionID string) {
	c.eventMu.Lock()
	delete(c.handlers, sessionID)
	c.eventMu.Unlock()
}

func (c *cdpClient) send(ctx context.Context, sessionID, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	select {
	case <-c.closed:
		err := c.closeErr
		c.mu.Unlock()
		if err == nil {
			err = fmt.Errorf("CDP 连接已关闭")
		}
		return nil, err
	default:
	}
	c.nextID++
	id := c.nextID
	ch := make(chan cdpReply, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	var rawParams json.RawMessage
	if params != nil {
		encoded, err := sonic.Marshal(params)
		if err != nil {
			c.dropPending(id)
			return nil, fmt.Errorf("序列化 CDP 参数失败: %w", err)
		}
		rawParams = encoded
	}
	payload, err := sonic.Marshal(cdpEnvelope{
		ID:        id,
		Method:    method,
		Params:    rawParams,
		SessionID: sessionID,
	})
	if err != nil {
		c.dropPending(id)
		return nil, fmt.Errorf("序列化 CDP 请求失败: %w", err)
	}

	c.writeMu.Lock()
	err = c.conn.Write(ctx, websocket.MessageText, payload)
	c.writeMu.Unlock()
	if err != nil {
		c.dropPending(id)
		return nil, fmt.Errorf("发送 CDP 请求失败: %w", err)
	}

	select {
	case reply := <-ch:
		if reply.err != nil {
			var cdpErr *cdpError
			if errors.As(reply.err, &cdpErr) {
				return nil, cdpErr
			}
			return nil, reply.err
		}
		if len(reply.result) == 0 {
			return json.RawMessage("{}"), nil
		}
		return reply.result, nil
	case <-ctx.Done():
		c.dropPending(id)
		return nil, ctx.Err()
	case <-c.closed:
		c.mu.Lock()
		err := c.closeErr
		c.mu.Unlock()
		if err == nil {
			err = fmt.Errorf("CDP 连接已关闭")
		}
		return nil, err
	}
}

func (c *cdpClient) dropPending(id int) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *cdpClient) fail(err error) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closeErr = err
		pending := c.pending
		c.pending = make(map[int]chan cdpReply)
		close(c.closed)
		c.mu.Unlock()
		for _, ch := range pending {
			ch <- cdpReply{err: err}
		}
		_ = c.conn.Close(websocket.StatusGoingAway, "closed")
	})
}

func (c *cdpClient) close() {
	c.fail(fmt.Errorf("CDP 连接关闭"))
}

func (c *cdpClient) alive() bool {
	select {
	case <-c.closed:
		return false
	default:
		return true
	}
}

func (c *cdpClient) command(ctx context.Context, sessionID, method string, params any, out any) error {
	raw, err := c.send(ctx, sessionID, method, params)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := sonic.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("解析 CDP %s 响应失败: %w", method, err)
	}
	return nil
}

func (c *cdpClient) browserVersion(ctx context.Context) (string, string, error) {
	raw, err := c.send(ctx, "", "Browser.getVersion", nil)
	if err != nil {
		return "", "", err
	}
	var out map[string]any
	if err := sonic.Unmarshal(raw, &out); err != nil {
		return "", "", err
	}
	product, _ := out["product"].(string)
	protocol, _ := out["protocolVersion"].(string)
	return product, protocol, nil
}

func sonicUnmarshal(data []byte, out any) error {
	return sonic.Unmarshal(data, out)
}

func cdpTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}
