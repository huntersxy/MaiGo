package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	mcptool "github.com/cloudwego/eino-ext/components/tool/mcp"
	"github.com/cloudwego/eino/components/tool"
	mcpprotocol "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

const featureRefreshTimeout = 15 * time.Second

// watchNotifications 注册服务端能力列表变更通知；断连时由 Manager 重建注册表
func (c *serverConnection) watchNotifications(handler func(serverName, method string)) {
	if c == nil || c.cli == nil || handler == nil {
		return
	}
	serverName := c.cfg.Name
	c.cli.OnNotification(func(notification mcpprotocol.JSONRPCNotification) {
		handler(serverName, string(notification.Method))
	})
}

// handleNotification 处理 tools/prompts/resources 列表变更，重新拉取声明并刷新路由
func (m *Manager) handleNotification(serverName, method string) {
	switch method {
	case string(mcpprotocol.MethodNotificationToolsListChanged),
		string(mcpprotocol.MethodNotificationPromptsListChanged),
		string(mcpprotocol.MethodNotificationResourcesListChanged):
	default:
		return
	}
	if err := m.refreshServerCapabilities(serverName, method); err != nil {
		zap.L().Warn("MCP 能力列表变更通知处理失败",
			zap.String("server", serverName),
			zap.String("method", method),
			zap.Error(err))
	}
}

// refreshServerCapabilities 按通知类型重新拉取该服务器的工具/Prompt/Resource 并原子替换
func (m *Manager) refreshServerCapabilities(serverName, method string) error {
	if m == nil {
		return nil
	}
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), featureRefreshTimeout)
	defer cancel()

	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return nil
	}
	conn := m.connections[serverName]
	if conn == nil || conn.cli == nil || !m.serverConnectedLocked(serverName) {
		m.mu.RUnlock()
		return nil
	}
	cfg := conn.cfg
	cli := conn.cli
	capabilities := conn.capabilities
	m.mu.RUnlock()

	var (
		toolDefs  []ToolDefinition
		baseTools []tool.BaseTool
		prompts   []mcpprotocol.Prompt
		resources []mcpprotocol.Resource
		templates []mcpprotocol.ResourceTemplate
	)

	switch method {
	case string(mcpprotocol.MethodNotificationToolsListChanged):
		defs, err := listAllTools(ctx, cli)
		if err != nil {
			return newError(ErrorKindOf(err), serverName, "tools/list",
				fmt.Sprintf("刷新 MCP 工具列表失败: %v", err), err)
		}
		toolDefs = filterToolDefinitions(defs, cfg.ToolNameList)
		baseTools, err = mcptool.GetTools(ctx, &mcptool.Config{Cli: cli, ToolNameList: cfg.ToolNameList})
		if err != nil {
			return newError(ErrorKindOf(err), serverName, "tools/list",
				fmt.Sprintf("刷新 MCP 工具列表失败: %v", err), err)
		}
		baseTools = wrapToolTimeout(baseTools, cfg.ReadTimeout())
	case string(mcpprotocol.MethodNotificationPromptsListChanged):
		if !serverSupportsPrompts(capabilities) {
			return nil
		}
		next, err := listAllPrompts(ctx, cli)
		if err != nil {
			return newError(ErrorKindOf(err), serverName, "prompts/list",
				fmt.Sprintf("刷新 MCP Prompt 列表失败: %v", err), err)
		}
		prompts = next
	case string(mcpprotocol.MethodNotificationResourcesListChanged):
		if !serverSupportsResources(capabilities) {
			return nil
		}
		next, err := listAllResources(ctx, cli)
		if err != nil {
			return newError(ErrorKindOf(err), serverName, "resources/list",
				fmt.Sprintf("刷新 MCP Resource 列表失败: %v", err), err)
		}
		resources = next
		nextTemplates, err := listAllResourceTemplates(ctx, cli)
		if err != nil {
			if !errors.Is(err, mcpprotocol.ErrMethodNotFound) {
				return newError(ErrorKindOf(err), serverName, "resources/templates/list",
					fmt.Sprintf("刷新 MCP Resource Template 列表失败: %v", err), err)
			}
			nextTemplates = nil
		}
		templates = nextTemplates
	}

	m.mu.Lock()
	if m.closed || m.connections[serverName] != conn || !m.serverConnectedLocked(serverName) {
		m.mu.Unlock()
		return nil
	}
	switch method {
	case string(mcpprotocol.MethodNotificationToolsListChanged):
		conn.toolDefs = toolDefs
		conn.tools = baseTools
		if index := indexOfServer(m.servers, serverName); index >= 0 {
			m.servers[index].ToolCount = len(toolDefs)
		}
	case string(mcpprotocol.MethodNotificationPromptsListChanged):
		conn.prompts = prompts
	case string(mcpprotocol.MethodNotificationResourcesListChanged):
		conn.resources = resources
		conn.resourceTemplates = templates
	}
	if err := m.rebuildRegistryLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	callback := m.statusChange
	m.mu.Unlock()

	if callback != nil {
		callback()
	}
	zap.L().Info("MCP 能力列表已按通知刷新",
		zap.String("server", serverName), zap.String("method", method))
	return nil
}
