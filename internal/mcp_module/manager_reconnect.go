package mcp

import (
	"context"
	"fmt"

	"go.uber.org/zap"
)

// serverConnectedLocked 判断服务器当前是否处于可调用状态，调用方必须持有 Manager 锁
func (m *Manager) serverConnectedLocked(serverName string) bool {
	for index := range m.servers {
		if m.servers[index].Name == serverName {
			return m.servers[index].Connected
		}
	}
	return false
}

// rebuildRegistryLocked 根据当前已连接服务器重建工具、Prompt、Resource 注册表
// 用于断连后撤下失效能力，以及重连后恢复能力；调用方必须持有 Manager 锁
func (m *Manager) rebuildRegistryLocked() error {
	m.tools = nil
	m.routes = nil
	m.routeByName = make(map[string]ToolRoute)
	m.promptToServer = make(map[string]string)
	m.resourceToServer = make(map[string]string)
	m.templateToServer = make(map[string]string)

	for _, serverName := range m.connectionOrder {
		conn := m.connections[serverName]
		if conn == nil || !m.serverConnectedLocked(serverName) {
			continue
		}
		m.registerTools(conn)
		m.registerPrompts(conn)
		m.registerResources(conn)
		m.registerTemplates(conn)
		if index := indexOfServer(m.servers, serverName); index >= 0 {
			m.servers[index].ToolCount = len(conn.toolDefs)
			m.servers[index].ProtocolVersion = conn.protocolVersion
		}
	}
	if len(m.connectionOrder) == 0 {
		return nil
	}
	if err := m.applyToolExposureLocked(); err != nil {
		return err
	}
	return nil
}

func indexOfServer(servers []ServerStatus, name string) int {
	for index := range servers {
		if servers[index].Name == name {
			return index
		}
	}
	return -1
}

// reconnectServer 重建单个服务器的连接与能力声明；探测失败后由 ProbeConnections 调用
func (m *Manager) reconnectServer(ctx context.Context, serverName string) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	conn := m.connections[serverName]
	cfg, configured := m.serverConfigs[serverName]
	if conn == nil || !configured {
		m.mu.Unlock()
		return fmt.Errorf("MCP 服务器 %q 未配置，无法重连", serverName)
	}
	if _, busy := m.reconnecting[serverName]; busy {
		m.mu.Unlock()
		return nil
	}
	m.reconnecting[serverName] = struct{}{}
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.reconnecting, serverName)
		m.mu.Unlock()
	}()

	_ = conn.close()

	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout(cfg))
	defer cancel()
	next, err := connectServer(connectCtx, cfg, m.clientCfg, m.host, m.discoverExtended)
	if err != nil {
		m.setServerError(serverName, fmt.Errorf("MCP 服务器 %q 重连失败: %w", serverName, err))
		return err
	}
	next.watchConnectionLost(m.connectionLostFrom)
	next.watchNotifications(m.handleNotification)

	m.mu.Lock()
	if m.closed || m.connections[serverName] != conn {
		m.mu.Unlock()
		_ = next.close()
		return nil
	}
	m.connections[serverName] = next
	for index := range m.servers {
		if m.servers[index].Name != serverName {
			continue
		}
		m.servers[index].Connected = true
		m.servers[index].Error = ""
		m.servers[index].ProtocolVersion = next.protocolVersion
		m.servers[index].ToolCount = len(next.toolDefs)
		break
	}
	rebuildErr := m.rebuildRegistryLocked()
	callback := m.statusChange
	m.mu.Unlock()

	if rebuildErr != nil {
		zap.L().Warn("MCP 重连后重建能力注册表失败",
			zap.String("server", serverName), zap.Error(rebuildErr))
		return rebuildErr
	}
	zap.L().Info("MCP 服务器重连成功", zap.String("server", serverName))
	if callback != nil {
		callback()
	}
	return nil
}

// setServerError 记录服务器断连/重连失败状态并通知服务层
func (m *Manager) setServerError(serverName string, err error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	for index := range m.servers {
		if m.servers[index].Name != serverName {
			continue
		}
		m.servers[index].Connected = false
		m.servers[index].Error = errorMessage(err, "MCP 连接已断开")
		break
	}
	_ = m.rebuildRegistryLocked()
	callback := m.statusChange
	m.mu.Unlock()
	if callback != nil {
		callback()
	}
}
