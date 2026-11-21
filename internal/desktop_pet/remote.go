package desktoppet

// 远端桌宠连接与主程序状态，对齐 desktop_pet/remote_server.py 的 RemotePeer 与 service.py 的 remote 分支。

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/coder/websocket"

	"MaiGo/internal/services/schedules"
)

// RemotePeer 一台已认证的远端桌宠连接；只有该对象接触 WebSocket。
type RemotePeer struct {
	conn          *websocket.Conn
	clientID      string
	connectionID  string
	capabilities  map[string]bool
	writeMu       sync.Mutex
	closed        atomic.Bool
	requests      atomic.Int32
	imageRequests atomic.Int32
}

var (
	defaultServiceMu sync.RWMutex
	defaultService   *Service
)

// SetDefaultService 注册进程内桌宠服务，供 WebUI 远端入口使用
func SetDefaultService(service *Service) {
	defaultServiceMu.Lock()
	defaultService = service
	defaultServiceMu.Unlock()
}

// DefaultService 返回进程内桌宠服务
func DefaultService() *Service {
	defaultServiceMu.RLock()
	defer defaultServiceMu.RUnlock()
	return defaultService
}

// NewRemotePeer 构造远端连接
func NewRemotePeer(conn *websocket.Conn, clientID string, capabilities map[string]bool) *RemotePeer {
	return &RemotePeer{
		conn: conn, clientID: clientID, capabilities: capabilities,
		connectionID: newConnectionID(),
	}
}

func newConnectionID() string {
	var buffer [32]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(buffer[:])
}

// BeginRequest 登记一次普通远端请求
func (p *RemotePeer) BeginRequest() error {
	if p == nil || p.closed.Load() {
		return errIPCClosed
	}
	if p.requests.Add(1) > 2 {
		p.requests.Add(-1)
		return errors.New("远端桌宠请求过多，请稍后重试")
	}
	return nil
}

// EndRequest 结束普通远端请求
func (p *RemotePeer) EndRequest() {
	if p != nil {
		p.requests.Add(-1)
	}
}

// BeginImageRequest 登记一次图片读取
func (p *RemotePeer) BeginImageRequest() error {
	if p == nil || p.closed.Load() {
		return errIPCClosed
	}
	if p.imageRequests.Add(1) > 8 {
		p.imageRequests.Add(-1)
		return errors.New("图片读取请求过多，请稍后重试")
	}
	return nil
}

// EndImageRequest 结束图片读取
func (p *RemotePeer) EndImageRequest() {
	if p != nil {
		p.imageRequests.Add(-1)
	}
}

// ClientID 返回客户端声明的 ID
func (p *RemotePeer) ClientID() string { return p.clientID }

// ConnectionID 返回连接标识
func (p *RemotePeer) ConnectionID() string { return p.connectionID }

// Capabilities 返回客户端能力
func (p *RemotePeer) Capabilities() map[string]bool { return p.capabilities }

// Send 写入一条 JSON 文本消息
func (p *RemotePeer) Send(ctx context.Context, command map[string]any) error {
	if p == nil || p.closed.Load() {
		return errIPCClosed
	}
	encoded, err := jsonMarshalNoNewline(command)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.closed.Load() {
		return errIPCClosed
	}
	if err := p.conn.Write(writeCtx, websocket.MessageText, encoded); err != nil {
		return errIPCClosed
	}
	return nil
}

// Close 结束连接
func (p *RemotePeer) Close(reason string) {
	if p == nil || p.closed.Swap(true) {
		return
	}
	_ = p.conn.Close(websocket.StatusPolicyViolation, truncateCloseReason(reason))
}

// MarkDisconnected 标记对端已断开
func (p *RemotePeer) MarkDisconnected() { p.closed.Store(true) }

func truncateCloseReason(reason string) string {
	if len(reason) <= 35 {
		return reason
	}
	return reason[:35]
}

// AttachRemote 注册远端设备；一次只允许一台
func (s *Service) AttachRemote(peer *RemotePeer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.Enabled || s.cfg.Mode != "remote" || s.closing {
		return errors.New("请先启用桌宠并将运行模式设为 remote")
	}
	if s.remote != nil {
		return errors.New("已有桌宠连接，请先关闭原客户端")
	}
	s.remote = peer
	s.errMsg = ""
	return nil
}

// DetachRemote 断开远端设备并清理在途指令
func (s *Service) DetachRemote(peer *RemotePeer) {
	if s == nil || peer == nil {
		return
	}
	s.mu.Lock()
	if s.remote != peer {
		s.mu.Unlock()
		return
	}
	wasReady := s.ready
	reason := "unexpected"
	if s.closing {
		reason = "stopped"
	}
	s.remote = nil
	s.ready = false
	s.visible = false
	for id, pending := range s.pending {
		select {
		case pending <- callResult{err: errIPCClosed}:
		default:
		}
		delete(s.pending, id)
	}
	s.mu.Unlock()
	s.stopScreenReader()
	s.stopProactiveWake()
	if wasReady {
		s.emitHook(context.Background(), "closed", map[string]any{"reason": reason})
	}
}

// ReceiveRemoteEvent 把远端事件并入本机窗口同一套分发逻辑
func (s *Service) ReceiveRemoteEvent(ctx context.Context, peer *RemotePeer, event map[string]any) error {
	s.mu.RLock()
	current := s.remote
	closing := s.closing
	ready := s.ready
	s.mu.RUnlock()
	if current != peer || closing {
		return errors.New("远端桌宠连接已失效")
	}
	if asString(event["type"]) == "history" {
		if !ready {
			return errIPCNotReady
		}
		go s.publishHistory(ctx, event, peer.Send)
		return nil
	}
	if asString(event["type"]) == "ready" {
		if ready {
			return errors.New("桌宠不能重复上报就绪")
		}
		if err := peer.Send(ctx, map[string]any{"type": "ready", "version": 1}); err != nil {
			return err
		}
	}
	return s.handleEvent(ctx, event)
}

// RemoteChat 中继远端桌宠的图文消息
func (s *Service) RemoteChat(ctx context.Context, peer *RemotePeer, text string, images []ChatRequestImage) (string, error) {
	s.mu.RLock()
	relay := s.chatRelay
	current := s.remote
	ready := s.ready
	s.mu.RUnlock()
	if current == nil || current != peer || !ready {
		return "", errors.New("远端桌宠连接已断开或尚未就绪")
	}
	if relay == nil {
		return "", errors.New("桌宠聊天中继尚未就绪")
	}
	return relay(ctx, "builtin.desktop_pet", text, images)
}

// RemoteTodo 执行远端待办请求
func (s *Service) RemoteTodo(ctx context.Context, action string, payload map[string]any) (map[string]any, error) {
	s.mu.RLock()
	current := s.remote
	ready := s.ready
	s.mu.RUnlock()
	if current == nil || !ready {
		return nil, errors.New("远端桌宠连接已断开或尚未就绪")
	}
	switch action {
	case "list", "image", "add", "update", "complete", "delete":
	default:
		return nil, errors.New("不允许客户端调用此待办操作")
	}
	command := map[string]any{"type": "todo", "action": action}
	for key, value := range payload {
		command[key] = value
	}
	return schedules.Default().Command(ctx, command, "")
}

// writePayload 向当前传输写入已编码的指令
func (s *Service) writePayload(ctx context.Context, encoded []byte) error {
	s.mu.RLock()
	remote := s.remote
	stdin := s.stdin
	s.mu.RUnlock()
	if remote != nil {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := remote.conn.Write(writeCtx, websocket.MessageText, encoded); err != nil {
			return errIPCClosed
		}
		return nil
	}
	if stdin == nil {
		return errIPCNotReady
	}
	encoded = append(encoded, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := stdin.Write(encoded); err != nil {
		return fmt.Errorf("向桌宠窗口写入指令失败: %w", err)
	}
	return nil
}

func jsonMarshalNoNewline(value map[string]any) ([]byte, error) {
	encoded, err := sonic.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("编码桌宠指令失败: %w", err)
	}
	return encoded, nil
}
