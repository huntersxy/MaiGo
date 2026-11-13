package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
)

// ErrNotConfigured 无可用 MCP 管理器或未加载目标工具
var ErrNotConfigured = errors.New("mcp: 未配置或未加载 MCP 工具")

// CallTool 按 MCP 服务器名与原工具名调用，返回 JSON 对象结果
//
// 暴露名同时兼容 MaiBot 裸名、重名前缀名与旧 mcp-{server}-{tool} 名
func (m *Manager) CallTool(ctx context.Context, serverName, originalToolName string, arguments map[string]any) (map[string]any, error) {
	raw, err := m.InvokeTool(ctx, serverName, originalToolName, arguments)
	if err != nil {
		return nil, err
	}
	return decodeToolResultMap(raw)
}

// CallToolResult 按服务器与工具名调用并保留结构化结果
//
// 与 CallTool 的 JSON 兼容接口不同，这里不丢弃 structuredContent、content_items 与会话元数据。
func (m *Manager) CallToolResult(ctx context.Context, serverName, originalToolName string, arguments map[string]any) (ToolExecutionResult, error) {
	name := strings.TrimSpace(originalToolName)
	if m == nil || name == "" {
		return ToolExecutionResult{ToolName: name, ContentItems: []ToolContentItem{}}, ErrNotConfigured
	}
	server := strings.TrimSpace(serverName)
	m.mu.RLock()
	var route ToolRoute
	var ok bool
	if server != "" {
		route, ok = routeForServerTool(m.routes, server, name)
	} else {
		route, ok = m.routeByName[name]
	}
	conn := m.connections[route.ServerName]
	m.mu.RUnlock()
	if !ok || conn == nil {
		message := fmt.Sprintf("MCP 工具 %q 未找到", name)
		if server != "" {
			message = fmt.Sprintf("MCP 服务器 %q 未提供工具 %q", server, name)
		}
		return ToolExecutionResult{
			ToolName:     name,
			ServerName:   server,
			ErrorMessage: message,
			ContentItems: []ToolContentItem{},
		}, newError(ErrorKindNotFound, server, "tools/call", message, nil)
	}
	result, err := conn.callTool(ctx, route.Original, arguments)
	if result.ToolName == "" {
		result.ToolName = name
	}
	return result, err
}

// InvokeTool 按服务器与工具名调用，返回工具原始文本结果
func (m *Manager) InvokeTool(ctx context.Context, serverName, originalToolName string, arguments map[string]any) (string, error) {
	if m == nil {
		return "", ErrNotConfigured
	}
	name := strings.TrimSpace(originalToolName)
	if name == "" {
		return "", ErrNotConfigured
	}
	exposed, tools := m.resolveExposedName(name, serverName)
	if exposed == "" {
		return "", fmt.Errorf("%w: %q", ErrNotConfigured, name)
	}
	return invokeExposedTool(ctx, tools, exposed, arguments)
}

// InvokeToolByExposedName 按暴露名调用，供 Agent 侧统一工具入口使用
func (m *Manager) InvokeToolByExposedName(ctx context.Context, exposedName string, arguments map[string]any) (string, error) {
	if m == nil {
		return "", ErrNotConfigured
	}
	trimmed := strings.TrimSpace(exposedName)
	if trimmed == "" {
		return "", ErrNotConfigured
	}
	exposed, tools := m.resolveExposedName(trimmed, "")
	if exposed == "" {
		return "", fmt.Errorf("%w: %q", ErrNotConfigured, trimmed)
	}
	return invokeExposedTool(ctx, tools, exposed, arguments)
}

// resolveExposedName 解析路由并返回快照后的工具列表
func (m *Manager) resolveExposedName(name, serverName string) (string, []tool.BaseTool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tools := make([]tool.BaseTool, len(m.tools))
	copy(tools, m.tools)

	if server := strings.TrimSpace(serverName); server != "" {
		if route, ok := routeForServerTool(m.routes, server, name); ok {
			return route.Exposed, tools
		}
		// 未经过 Load 的路由（诊断/测试直接注入工具）回退到旧前缀名
		return prefixedToolName(server, name), tools
	}
	if route, ok := m.routeByName[name]; ok {
		return route.Exposed, tools
	}
	if IsMCPToolName(name) {
		return name, tools
	}
	return "", tools
}

func invokeExposedTool(ctx context.Context, tools []tool.BaseTool, exposedName string, arguments map[string]any) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	argsJSON := "{}"
	if len(arguments) > 0 {
		encoded, err := sonic.MarshalString(arguments)
		if err != nil {
			return "", fmt.Errorf("mcp: 序列化工具参数失败: %w", err)
		}
		argsJSON = encoded
	}
	invokable, err := findInvokableTool(tools, ctx, exposedName)
	if err != nil {
		return "", err
	}
	out, err := invokable.InvokableRun(ctx, argsJSON)
	if err != nil {
		return "", fmt.Errorf("mcp: 调用工具 %q 失败: %w", exposedName, err)
	}
	return strings.TrimSpace(out), nil
}

func findInvokableTool(tools []tool.BaseTool, ctx context.Context, exposedName string) (tool.InvokableTool, error) {
	for _, item := range tools {
		if item == nil {
			continue
		}
		info, err := item.Info(ctx)
		if err != nil || info == nil {
			continue
		}
		if info.Name != exposedName {
			continue
		}
		invokable, ok := item.(tool.InvokableTool)
		if !ok {
			return nil, fmt.Errorf("mcp: 工具 %q 不可调用", exposedName)
		}
		return invokable, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrNotConfigured, exposedName)
}

func decodeToolResultMap(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, NewError("MCP 工具返回为空")
	}
	var data map[string]any
	if err := sonic.UnmarshalString(raw, &data); err != nil {
		return nil, NewError("MCP 工具返回了无效 JSON")
	}
	return data, nil
}
