package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ToolPreview WebUI 测试连接返回的工具摘要，对齐 MaiBot MCPToolPreview
type ToolPreview struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ReadOnly    *bool  `json:"read_only"`
	Destructive *bool  `json:"destructive"`
}

// ConnectionTestResult 一次性连接测试结果，对齐 MaiBot MCPConnectionTestResponse
type ConnectionTestResult struct {
	Success         bool          `json:"success"`
	Error           string        `json:"error"`
	ProtocolVersion string        `json:"protocol_version"`
	Tools           []ToolPreview `json:"tools"`
}

// TestConnectionTimeout 根据 HTTP 超时推算探测上限，对齐 MaiBot webui mcp.py
func TestConnectionTimeout(httpTimeoutSeconds float64) time.Duration {
	if httpTimeoutSeconds <= 0 {
		httpTimeoutSeconds = DefaultHTTPTimeoutSeconds
	}
	seconds := min(30.0, max(5.0, httpTimeoutSeconds+5.0))
	return time.Duration(seconds * float64(time.Second))
}

// TestConnection 建立一次性 MCP 连接、列出工具后断开，不写入 Manager 运行态
//
// 对齐 MaiBot POST /api/webui/mcp/test：只发现工具，不发现 Prompt/Resource
func TestConnection(ctx context.Context, cfg ServerConfig) ConnectionTestResult {
	cfg.Enabled = true
	if err := validateServerConfig(cfg); err != nil {
		return ConnectionTestResult{Success: false, Error: err.Error(), Tools: []ToolPreview{}}
	}

	timeout := TestConnectionTimeout(cfg.HTTPTimeoutSeconds)
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := connectServer(callCtx, cfg, DefaultClientConfig(), HostCallbacks{}, false)
	if err != nil {
		message := err.Error()
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			message = fmt.Sprintf("连接测试超过 %g 秒，已终止", timeout.Seconds())
		}
		return ConnectionTestResult{Success: false, Error: message, Tools: []ToolPreview{}}
	}
	defer func() { _ = conn.close() }()

	tools := make([]ToolPreview, 0, len(conn.toolDefs))
	for _, definition := range conn.toolDefs {
		tools = append(tools, toolPreviewFromDefinition(definition))
	}
	return ConnectionTestResult{
		Success:         true,
		ProtocolVersion: conn.protocolVersion,
		Tools:           tools,
	}
}

func toolPreviewFromDefinition(definition ToolDefinition) ToolPreview {
	title := strings.TrimSpace(definition.Title)
	preview := ToolPreview{
		Name:        definition.Name,
		Title:       title,
		Description: definition.Description,
	}
	if definition.Annotation != nil {
		preview.ReadOnly = definition.Annotation.ReadOnly
		preview.Destructive = definition.Annotation.Destructive
	}
	return preview
}
