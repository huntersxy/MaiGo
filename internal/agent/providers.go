package agent

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"

	"maibot-go/internal/agent/files"
	"maibot-go/internal/computer"
	"maibot-go/internal/files"
)

// FileToolHost 文件工具宿主，端口定义在 files 模块（对应 src/files/provider.py）
type FileToolHost = filesvc.FileToolHost

// AgentToolProvider 对齐 src/agent/provider.py
type AgentToolProvider struct {
	host   AgentToolHost
	status func() map[string]any
}

// NewAgentToolProvider 创建传话与状态工具提供者
func NewAgentToolProvider(host AgentToolHost, status func() map[string]any) *AgentToolProvider {
	return &AgentToolProvider{host: host, status: status}
}

// ProviderName 实现 ToolProvider
func (p *AgentToolProvider) ProviderName() string { return "agent" }

// Tools 实现 ToolProvider
func (p *AgentToolProvider) Tools() []ToolDefinition {
	return toolDefinitions(agentfiles.AgentSpecs(), &agentToolsAdapter{agent: p.host, status: p.status})
}

// ComputerToolProvider 对齐 ComputerToolProvider：工具来自 internal/computer
type ComputerToolProvider struct {
	enabled    bool
	sessionID  string
	statusFunc func() map[string]any
}

// NewComputerToolProvider 创建电脑工具提供者
func NewComputerToolProvider(enabled bool, sessionID string, status func() map[string]any) *ComputerToolProvider {
	return &ComputerToolProvider{enabled: enabled, sessionID: sessionID, statusFunc: status}
}

// ProviderName 实现 ToolProvider
func (p *ComputerToolProvider) ProviderName() string { return "computer" }

// Tools 实现 ToolProvider
func (p *ComputerToolProvider) Tools() []ToolDefinition {
	if !p.enabled {
		return nil
	}
	specs := computer.ListTools(true)
	out := make([]ToolDefinition, 0, len(specs))
	for _, spec := range specs {
		tool := spec.Name
		out = append(out, ToolDefinition{
			Name:        string(tool),
			Description: spec.Description,
			InputSchema: spec.ParametersSchema,
			Handler: func(ctx context.Context, args map[string]any) (ToolResult, error) {
				result, err := computer.Invoke(ctx, computer.InvokeRequest{
					Tool:      tool,
					Arguments: args,
					SessionID: p.sessionID,
				})
				if err != nil && result.Error == "" {
					return ToolResult{IsError: true, Text: err.Error()}, nil
				}
				payload := result.Payload
				if payload == nil {
					payload = map[string]any{}
				}
				if result.Error != "" {
					payload["error"] = result.Error
				}
				payload["success"] = result.Success
				text, marshalErr := sonic.ConfigStd.Marshal(payload)
				if marshalErr != nil {
					return ToolResult{}, marshalErr
				}
				images := make([]ToolImage, 0, len(result.Images))
				for _, image := range result.Images {
					images = append(images, ToolImage{Data: image.Base64, MIMEType: image.MIMEType})
				}
				return ToolResult{Text: string(text), Structured: payload, Images: images, IsError: !result.Success}, nil
			},
		})
	}
	return out
}

// FileToolProvider 对齐 src/files/provider.py
type FileToolProvider struct {
	host FileToolHost
}

// NewFileToolProvider 创建文件工具提供者
func NewFileToolProvider(host FileToolHost) *FileToolProvider {
	return &FileToolProvider{host: host}
}

// ProviderName 实现 ToolProvider
func (p *FileToolProvider) ProviderName() string { return "files" }

// Tools 实现 ToolProvider
func (p *FileToolProvider) Tools() []ToolDefinition {
	specs := filesvc.FileToolSpecs()
	out := make([]ToolDefinition, 0, len(specs))
	for _, spec := range specs {
		spec := spec
		out = append(out, ToolDefinition{
			Name:        spec.Name,
			Description: spec.Description,
			InputSchema: spec.Schema,
			Handler: func(ctx context.Context, args map[string]any) (ToolResult, error) {
				payload, err := filesvc.InvokeFileTool(ctx, spec.Name, args, p.host)
				if err != nil {
					return ToolResult{IsError: true, Text: err.Error()}, nil
				}
				text, marshalErr := sonic.ConfigStd.Marshal(payload)
				if marshalErr != nil {
					return ToolResult{}, marshalErr
				}
				return ToolResult{Text: string(text), Structured: payload}, nil
			},
		})
	}
	return out
}

func toolDefinitions(specs []agentfiles.Spec, host agentfiles.Host) []ToolDefinition {
	out := make([]ToolDefinition, 0, len(specs))
	for _, spec := range specs {
		spec := spec
		out = append(out, ToolDefinition{
			Name:        spec.Name,
			Description: spec.Description,
			InputSchema: spec.Schema,
			Handler: func(ctx context.Context, args map[string]any) (ToolResult, error) {
				result := agentfiles.Invoke(ctx, spec.Name, args, host)
				if result.Failed {
					return ToolResult{IsError: true, Text: result.ErrorMessage}, nil
				}
				images := make([]ToolImage, 0, len(result.Images))
				for _, image := range result.Images {
					images = append(images, ToolImage{Data: image.Data, MIMEType: image.MIMEType})
				}
				return ToolResult{Text: result.Content, Structured: result.Structured, Images: images}, nil
			},
		})
	}
	return out
}

type agentToolsAdapter struct {
	agent  AgentToolHost
	status func() map[string]any
}

func (a *agentToolsAdapter) TellAgent(ctx context.Context, text, target string, images []string) (map[string]any, error) {
	if a.agent == nil {
		return nil, fmt.Errorf("Agent 传话通道未接线")
	}
	return a.agent.TellAgent(ctx, text, target, images)
}

func (a *agentToolsAdapter) AgentStatus(ctx context.Context, includeImage bool) (map[string]any, []agentfiles.Image, error) {
	_ = ctx
	raw := map[string]any{}
	if a.status != nil {
		raw = a.status()
	}
	if includeImage {
		if service := computer.DefaultService(); service != nil && service.Enabled() {
			for key, value := range service.LastFrameSnapshot() {
				raw[key] = value
			}
		}
	}
	payload, images := agentfiles.StatusPayload(raw)
	return payload, images, nil
}

func (a *agentToolsAdapter) AgentChats(ctx context.Context) ([]map[string]any, error) {
	if a.agent == nil {
		return nil, fmt.Errorf("Agent 聊天列表通道未接线")
	}
	return a.agent.ListAgentChats(ctx)
}

func (a *agentToolsAdapter) AgentRecent(ctx context.Context, target string, limit int) (map[string]any, error) {
	if a.agent == nil {
		return nil, fmt.Errorf("Agent 最近消息通道未接线")
	}
	return a.agent.RecentAgentMessages(ctx, target, limit)
}
