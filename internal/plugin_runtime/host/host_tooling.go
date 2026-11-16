package host

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/core/tooling"
)

// pluginToolProvider 把插件 TOOL 组件接入 core/tooling 统一注册表
type pluginToolProvider struct {
	host *Host
}

func newPluginToolProvider(host *Host) *pluginToolProvider {
	return &pluginToolProvider{host: host}
}

func (p *pluginToolProvider) ProviderName() string { return "plugin_runtime" }

func (p *pluginToolProvider) ProviderType() string { return "plugin" }

func (p *pluginToolProvider) ListTools(_ context.Context, availability *tooling.ToolAvailabilityContext) ([]tooling.ToolSpec, error) {
	if p == nil || p.host == nil || p.host.components == nil {
		return nil, nil
	}
	filter := ComponentFilter{}
	if availability != nil {
		filter = ComponentFilter{
			SessionID:   availability.SessionID,
			IsGroupChat: availability.IsGroupChat,
			GroupID:     availability.GroupID,
			Platform:    availability.Platform,
		}
	}
	entries, err := p.host.components.ComponentsByType(ComponentTypeTool, true, filter)
	if err != nil {
		return nil, err
	}
	specs := make([]tooling.ToolSpec, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		spec := tooling.NewToolSpec(entry.Name, entry.Description)
		spec.ParametersSchema = entry.BuildParametersSchema()
		spec.ProviderName = entry.PluginID
		spec.ProviderType = "plugin"
		spec.Metadata = map[string]any{
			"plugin_id":      entry.PluginID,
			"component_name": entry.Name,
			"invoke_method":  entry.InvokeMethod,
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func (p *pluginToolProvider) Invoke(ctx context.Context, invocation tooling.ToolInvocation, execution *tooling.ToolExecutionContext) (tooling.ToolExecutionResult, error) {
	if p == nil || p.host == nil {
		return tooling.ToolExecutionResult{}, fmt.Errorf("插件工具运行时未初始化")
	}
	execCtx := pluginToolExecutionContext(execution)
	execCtx.SessionID = firstNonEmptyText(execCtx.SessionID, invocation.SessionID)
	execCtx.StreamID = firstNonEmptyText(execCtx.StreamID, invocation.StreamID)
	name := strings.TrimSpace(invocation.ToolName)
	if name == "" {
		name = metadataText(invocation.Metadata, "component_name")
	}
	result := p.host.executePluginToolDirect(ctx, name, invocation.Arguments, execCtx)
	return pluginToolResultToCore(result), nil
}

func (p *pluginToolProvider) Close(context.Context) error { return nil }

func pluginToolExecutionContext(execution *tooling.ToolExecutionContext) ToolExecutionContext {
	if execution == nil {
		return ToolExecutionContext{}
	}
	return ToolExecutionContext{
		SessionID:   execution.SessionID,
		StreamID:    execution.StreamID,
		IsGroupChat: execution.IsGroupChat,
		GroupID:     execution.GroupID,
		UserID:      execution.UserID,
		Platform:    execution.Platform,
	}
}

func pluginToolResultToCore(result ToolExecutionResult) tooling.ToolExecutionResult {
	output := tooling.ToolExecutionResult{
		ToolName:           result.ToolName,
		Success:            result.Success,
		Content:            result.Content,
		ErrorMessage:       result.ErrorMessage,
		StructuredContent:  result.StructuredContent,
		Metadata:           cloneAnyMap(result.Metadata),
		StopAfterExecution: result.StopAfterExecution,
	}
	if len(result.ContentItems) > 0 {
		output.ContentItems = make([]tooling.ToolContentItem, 0, len(result.ContentItems))
		for _, item := range result.ContentItems {
			output.ContentItems = append(output.ContentItems, tooling.ToolContentItem{
				ContentType: item.ContentType,
				Text:        item.Text,
				Data:        item.Data,
				MimeType:    item.MimeType,
				URI:         item.URI,
				Name:        item.Name,
				Description: item.Description,
				Metadata:    cloneAnyMap(item.Metadata),
			})
		}
	}
	return output
}
