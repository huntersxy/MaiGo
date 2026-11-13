package mcp

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
)

const (
	// defaultConnectionProbeInterval 主动探活周期；mcp-go 仅 SSE 暴露 OnConnectionLost，stdio/HTTP 需要轮询 Ping
	defaultConnectionProbeInterval = 30 * time.Second
	// connectionProbeTimeout 单次 Ping 上限，避免半开连接拖住探活循环
	connectionProbeTimeout = 5 * time.Second
)

// Ping 主动探测单个服务器连接，stdio/streamable_http 没有断连回调时由上层轮询调用
func (c *serverConnection) Ping(ctx context.Context) error {
	if c == nil || c.cli == nil {
		return newError(ErrorKindConnection, "", "ping", "MCP 服务器未连接", nil)
	}
	if err := c.cli.Ping(ctx); err != nil {
		return newError(ErrorKindOf(err), c.cfg.Name, "ping",
			fmt.Sprintf("MCP 服务器 %q 探测失败: %v", c.cfg.Name, err), err)
	}
	return nil
}

type connectionProbeTarget struct {
	name string
	conn *serverConnection
}

// ProbeConnections 主动 Ping 所有已连接服务器；失败时刷新断连状态并返回真实错误
func (m *Manager) ProbeConnections(ctx context.Context) []error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return nil
	}
	targets := make([]connectionProbeTarget, 0, len(m.connectionOrder))
	for _, name := range m.connectionOrder {
		if conn := m.connections[name]; conn != nil {
			targets = append(targets, connectionProbeTarget{name: name, conn: conn})
		}
	}
	m.mu.RUnlock()

	var errs []error
	for _, target := range targets {
		probeCtx, cancel := context.WithTimeout(ctx, connectionProbeTimeout)
		err := target.conn.Ping(probeCtx)
		cancel()
		if err != nil {
			m.connectionLost(target.name, err)
			errs = append(errs, err)
			// 探测失败即尝试重建该服务器连接；重建仍失败时把真实错误留给下一轮状态
			if ctx.Err() == nil {
				if reconnectErr := m.reconnectServer(ctx, target.name); reconnectErr != nil {
					errs = append(errs, fmt.Errorf("MCP 服务器 %q 重连失败: %w", target.name, reconnectErr))
				}
			}
			continue
		}
		m.connectionRestored(target.name)
	}
	return errs
}

// connectionRestored 把主动探测成功的服务器恢复为 connected，仅在状态变化时通知
func (m *Manager) connectionRestored(serverName string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	changed := false
	for index := range m.servers {
		if m.servers[index].Name != serverName || m.servers[index].Connected {
			continue
		}
		m.servers[index].Connected = true
		m.servers[index].Error = ""
		changed = true
		break
	}
	callback := m.statusChange
	m.mu.Unlock()
	if changed {
		zap.L().Info("MCP 服务器连接已恢复", zap.String("server", serverName))
		if callback != nil {
			callback()
		}
	}
}

// WatchConnections 按周期主动探测连接，直到 ctx 取消；用于 stdio/streamable_http 断连发现
func (m *Manager) WatchConnections(ctx context.Context, interval time.Duration) {
	if m == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = defaultConnectionProbeInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if errs := m.ProbeConnections(ctx); len(errs) > 0 {
				zap.L().Warn("MCP 连接主动探测发现断连", zap.Errors("errors", errs))
			}
		}
	}
}

// WatchConnections 按周期探测当前管理器；热重载切换 manager 后自动跟随新实例
func (s *Service) WatchConnections(ctx context.Context, interval time.Duration) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = defaultConnectionProbeInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.mu.RLock()
				manager := s.manager
				s.mu.RUnlock()
				if manager == nil {
					continue
				}
				if errs := manager.ProbeConnections(ctx); len(errs) > 0 {
					zap.L().Warn("MCP 连接主动探测发现断连", zap.Errors("errors", errs))
				}
			}
		}
	}()
}
