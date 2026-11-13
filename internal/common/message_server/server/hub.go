package server

import (
	"context"
	"fmt"
	"sync"

	"github.com/coder/websocket"
)

// connHub 按 platform 维护单连接（新连接顶替旧连接）
type connHub struct {
	mu    sync.RWMutex
	conns map[string]*platformConn
}

type platformConn struct {
	platform string
	ws       *websocket.Conn
}

func newConnHub() *connHub {
	return &connHub{conns: make(map[string]*platformConn)}
}

// Attach 注册平台连接，若已有连接则关闭旧连接
func (h *connHub) Attach(platform string, ws *websocket.Conn) *platformConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	if prev, ok := h.conns[platform]; ok && prev.ws != nil {
		_ = prev.ws.Close(websocket.StatusNormalClosure, "新的连接已建立")
	}
	pc := &platformConn{platform: platform, ws: ws}
	h.conns[platform] = pc
	return pc
}

// Detach 移除连接（仅当仍是当前连接时）
func (h *connHub) Detach(platform string, ws *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur, ok := h.conns[platform]
	if !ok || cur.ws != ws {
		return
	}
	delete(h.conns, platform)
}

// HasPlatform 是否有在线适配器
func (h *connHub) HasPlatform(platform string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[platform]
	return ok
}

// SendToPlatform 向指定平台连接写入 JSON 文本帧
func (h *connHub) SendToPlatform(ctx context.Context, platform string, payload []byte) error {
	h.mu.RLock()
	pc, ok := h.conns[platform]
	h.mu.RUnlock()
	if !ok || pc.ws == nil {
		return fmt.Errorf("maimmsg: 平台 %q 无连接", platform)
	}
	return pc.ws.Write(ctx, websocket.MessageText, payload)
}
