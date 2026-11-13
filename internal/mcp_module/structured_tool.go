package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ToolResultInvoker 可选能力：返回保留 structuredContent/content_items 的完整工具结果
// 供 Agent 侧在保持文本输出的同时读取结构化字段
type ToolResultInvoker interface {
	InvokeToolResult(ctx context.Context, argumentsJSON string) (ToolExecutionResult, error)
}

// InvokeToolResult 直接调用底层 MCP 连接，避免 eino 适配器把整个 CallToolResult 当字符串返回
func (t renamedTool) InvokeToolResult(ctx context.Context, argumentsJSON string) (ToolExecutionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.conn == nil || t.conn.cli == nil {
		if t.InvokableTool == nil {
			return ToolExecutionResult{ToolName: t.name, ErrorMessage: "MCP 工具未连接"}, fmt.Errorf("MCP 工具 %q 未连接", t.name)
		}
		raw, err := t.InvokableTool.InvokableRun(ctx, argumentsJSON)
		result := ToolExecutionResult{
			ToolName: t.name,
			Success:  err == nil,
			Content:  raw,
			Metadata: map[string]any{"server_name": t.serverName},
		}
		if err != nil {
			result.ErrorMessage = err.Error()
		}
		return result, err
	}

	arguments := map[string]any{}
	if trimmed := strings.TrimSpace(argumentsJSON); trimmed != "" && trimmed != "{}" {
		if err := json.Unmarshal([]byte(trimmed), &arguments); err != nil {
			return ToolExecutionResult{ToolName: t.name}, fmt.Errorf("MCP 工具 %q 参数不是 JSON 对象: %w", t.name, err)
		}
	}
	result, err := t.conn.callTool(ctx, t.original, arguments)
	if err != nil {
		return result, err
	}
	if result.ToolName == "" {
		result.ToolName = t.name
	}
	if result.Success && strings.TrimSpace(result.Content) == "" && result.StructuredContent != nil {
		if encoded, encodeErr := json.Marshal(result.StructuredContent); encodeErr == nil {
			result.Content = string(encoded)
		}
	}
	if !result.Success && strings.TrimSpace(result.ErrorMessage) == "" {
		result.ErrorMessage = "工具执行失败。"
	}
	return result, nil
}
