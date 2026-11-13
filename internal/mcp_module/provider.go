package mcp

// MCP 工具 Provider，对齐 src/mcp_module/provider.py

import (
	"context"

	"maibot-go/internal/core/tooling"
)

// ProviderName MCP 工具 Provider 名称
const ProviderName = maiBotProviderName

// ProviderType MCP 工具 Provider 类型
const ProviderType = maiBotProviderName

// Provider 基于进程级 Service 的轻量工具 Provider
//
// 与 Python MCPToolProvider 一致：不拥有共享连接，Close 不释放 Service。
type Provider struct {
	service *Service
}

var _ tooling.ToolProvider = (*Provider)(nil)

// NewProvider 创建 MCP 工具 Provider
func NewProvider(service *Service) *Provider {
	return &Provider{service: service}
}

// ProviderName 实现 ToolProvider
func (p *Provider) ProviderName() string { return ProviderName }

// ProviderType 实现 ToolProvider
func (p *Provider) ProviderType() string { return ProviderType }

// ListTools 列出全部 MCP 工具
func (p *Provider) ListTools(ctx context.Context, availability *tooling.ToolAvailabilityContext) ([]tooling.ToolSpec, error) {
	if p == nil || p.service == nil {
		return nil, nil
	}
	return p.service.ToolSpecs(), nil
}

// Invoke 执行指定 MCP 工具，并保留聊天执行上下文用于审计
func (p *Provider) Invoke(ctx context.Context, invocation tooling.ToolInvocation, execution *tooling.ToolExecutionContext) (tooling.ToolExecutionResult, error) {
	if p == nil || p.service == nil {
		return tooling.ToolExecutionResult{
			ToolName:     invocation.ToolName,
			ErrorMessage: "MCP 共享服务尚未连接任何服务器",
		}, nil
	}
	serverName, _ := invocation.Metadata["mcp_server_name"].(string)
	result, err := p.service.CallToolResult(ctx, serverName, invocation.ToolName, invocation.Arguments)
	if err != nil && result.ToolName == "" {
		return tooling.ToolExecutionResult{
			ToolName:     invocation.ToolName,
			ErrorMessage: err.Error(),
		}, err
	}
	converted := tooling.ToolExecutionResult{
		ToolName:          firstNonEmpty(result.ToolName, invocation.ToolName),
		Success:           result.Success,
		Content:           result.Content,
		ErrorMessage:      result.ErrorMessage,
		StructuredContent: result.StructuredContent,
		Metadata:          cloneMetadata(result.Metadata),
	}
	if len(result.ContentItems) > 0 {
		converted.ContentItems = make([]tooling.ToolContentItem, 0, len(result.ContentItems))
		for _, item := range result.ContentItems {
			converted.ContentItems = append(converted.ContentItems, tooling.ToolContentItem{
				ContentType: item.ContentType,
				Text:        item.Text,
				Data:        item.Data,
				MimeType:    item.MIMEType,
				URI:         item.URI,
				Name:        item.Name,
				Description: item.Description,
				Metadata:    cloneMetadata(item.Metadata),
			})
		}
	}
	// 对齐 call_tool_invocation：审计字段只在缺失时补齐
	if execution != nil {
		if converted.Metadata == nil {
			converted.Metadata = map[string]any{}
		}
		if _, exists := converted.Metadata["chat_session_id"]; !exists {
			converted.Metadata["chat_session_id"] = execution.SessionID
		}
		if _, exists := converted.Metadata["chat_stream_id"]; !exists {
			converted.Metadata["chat_stream_id"] = execution.StreamID
		}
	}
	return converted, err
}

// Close Runtime 不拥有共享连接，关闭 Provider 时不释放 Service
func (p *Provider) Close(ctx context.Context) error { return nil }

func cloneMetadata(source map[string]any) map[string]any {
	if len(source) == 0 {
		return nil
	}
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
