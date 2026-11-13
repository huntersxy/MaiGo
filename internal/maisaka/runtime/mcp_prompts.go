package runtime

import (
	"context"
	"fmt"
	"strings"

	"maibot-go/internal/mcp_module"

	maibotcfg "maibot-go/internal/config/maibot"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

// mcpRuntimeConfig 汇总当前 generation 应使用的 MCP 服务器与客户端能力配置
//
// 服务器以 WebUI/Store 配置为准；Store 为空时回退 bot_config.toml [mcp].servers，
// 客户端宿主能力（roots/sampling/elicitation）始终读 bot_config.toml [mcp].client
func (a *Agent) mcpRuntimeConfig(ctx context.Context) ([]mcp.ServerConfig, mcp.ClientConfig, error) {
	defer a.startMCPRuntimeWatcher()
	if a == nil || a.mcpStore == nil {
		return nil, mcp.DefaultClientConfig(), nil
	}
	servers, err := a.mcpStore.EnabledConfigs(ctx)
	if err != nil {
		return nil, mcp.ClientConfig{}, err
	}
	clientCfg := mcp.DefaultClientConfig()
	if cfg := a.instanceConfig(); cfg != nil {
		tomlServers, tomlClient := mcp.FromMaiBotConfig(cfg.Bot.Mcp)
		clientCfg = tomlClient
		if len(servers) == 0 {
			servers = tomlServers
		}
	}
	return servers, clientCfg, nil
}

func (a *Agent) instanceConfig() *maibotcfg.InstanceConfig {
	a.instanceMu.RLock()
	defer a.instanceMu.RUnlock()
	if a.instanceHolder == nil {
		return nil
	}
	return a.instanceHolder.Config()
}

// MCPConfigSignature 返回当前有效 MCP 配置签名，供配置热重载判断是否需要重建 generation
func (a *Agent) MCPConfigSignature() string {
	servers, clientCfg, err := a.mcpRuntimeConfig(context.Background())
	if err != nil {
		return ""
	}
	return mcp.ConfigSignature(servers, clientCfg)
}

// mcpHostCallbacks 根据 bot_config 开关构建可注入的宿主能力回调
//
// Sampling 使用 planner 任务模型（缺省时回退 utils、replyer），与 MaiBot sampling.task_name 语义一致
func (a *Agent) mcpHostCallbacks(
	chatModel model.AgenticModel,
	chatName string,
	plannerModel model.AgenticModel,
	plannerName string,
	utilsModel model.AgenticModel,
	utilsName string,
) mcp.HostCallbacks {
	_, clientCfg, err := a.mcpRuntimeConfig(context.Background())
	if err != nil {
		return mcp.HostCallbacks{}
	}
	host := mcp.HostCallbacks{}
	if !clientCfg.EnableSampling {
		return host
	}
	client, name := samplingModel(plannerModel, plannerName, utilsModel, utilsName, chatModel, chatName)
	if client == nil {
		return host
	}
	host.Sampling = mcp.NewSamplingBridge(clientCfg.SamplingTaskName, newSamplingGenerator(client, name))
	return host
}

func samplingModel(
	plannerModel model.AgenticModel,
	plannerName string,
	utilsModel model.AgenticModel,
	utilsName string,
	chatModel model.AgenticModel,
	chatName string,
) (model.AgenticModel, string) {
	if plannerModel != nil {
		return plannerModel, plannerName
	}
	if utilsModel != nil {
		return utilsModel, utilsName
	}
	return chatModel, chatName
}

// newSamplingGenerator 把 MCP Sampling 请求转换为 eino AgenticModel 调用
func newSamplingGenerator(client model.AgenticModel, modelName string) mcp.SamplingGenerator {
	return func(ctx context.Context, request mcp.SamplingRequest) (mcp.SamplingResponse, error) {
		if client == nil {
			return mcp.SamplingResponse{}, fmt.Errorf("MCP Sampling 没有可用的宿主模型")
		}
		messages, toolNames := samplingMessages(request)
		options := make([]model.Option, 0, 1)
		if len(request.Tools) > 0 {
			toolInfos, err := samplingToolInfos(request.Tools)
			if err != nil {
				return mcp.SamplingResponse{}, err
			}
			options = append(options, model.WithTools(toolInfos))
		}
		reply, err := client.Generate(ctx, messages, options...)
		if err != nil {
			return mcp.SamplingResponse{}, fmt.Errorf("MCP Sampling 调用失败: %w", err)
		}
		return samplingResponse(reply, modelName, toolNames), nil
	}
}

func samplingMessages(request mcp.SamplingRequest) ([]*schema.AgenticMessage, map[string]string) {
	messages := make([]*schema.AgenticMessage, 0, len(request.Messages)+1)
	if strings.TrimSpace(request.SystemPrompt) != "" {
		messages = append(messages, schema.SystemAgenticMessage(strings.TrimSpace(request.SystemPrompt)))
	}
	toolNames := make(map[string]string, len(request.Tools))
	for _, tool := range request.Tools {
		toolNames[tool.Name] = tool.Name
	}
	callNames := make(map[string]string, len(request.Messages))
	for _, message := range request.Messages {
		switch message.Role {
		case "user":
			blocks := samplingUserBlocks(message.Contents)
			if len(blocks) == 0 {
				continue
			}
			messages = append(messages, &schema.AgenticMessage{
				Role:          schema.AgenticRoleTypeUser,
				ContentBlocks: blocks,
			})
		case "assistant":
			blocks, names := samplingAssistantBlocks(message.Contents)
			for callID, name := range names {
				callNames[callID] = name
			}
			if len(blocks) == 0 {
				continue
			}
			messages = append(messages, &schema.AgenticMessage{
				Role:          schema.AgenticRoleTypeAssistant,
				ContentBlocks: blocks,
			})
		case "tool":
			name := callNames[message.ToolCall]
			if name == "" {
				name = "tool"
			}
			messages = append(messages, &schema.AgenticMessage{
				Role: schema.AgenticRoleTypeUser,
				ContentBlocks: []*schema.ContentBlock{{
					Type: schema.ContentBlockTypeFunctionToolResult,
					FunctionToolResult: &schema.FunctionToolResult{
						CallID: message.ToolCall,
						Name:   name,
						Content: []*schema.FunctionToolResultContentBlock{{
							Type: schema.FunctionToolResultContentBlockTypeText,
							Text: &schema.UserInputText{Text: samplingContentText(message.Contents)},
						}},
					},
				}},
			})
		}
	}
	return messages, callNames
}

func samplingUserBlocks(contents []mcp.SamplingContent) []*schema.ContentBlock {
	blocks := make([]*schema.ContentBlock, 0, len(contents))
	for _, content := range contents {
		switch content.Type {
		case "text":
			if strings.TrimSpace(content.Text) == "" {
				continue
			}
			blocks = append(blocks, schema.NewContentBlock(&schema.UserInputText{Text: content.Text}))
		case "image":
			if content.Data == "" {
				blocks = append(blocks, schema.NewContentBlock(&schema.UserInputText{
					Text: fmt.Sprintf("[图片内容：mime_type=%s，当前客户端无法直接透传]", defaultString(content.MIMEType, "unknown")),
				}))
				continue
			}
			blocks = append(blocks, schema.NewContentBlock(&schema.UserInputImage{
				Base64Data: content.Data,
				MIMEType:   content.MIMEType,
			}))
		case "audio":
			blocks = append(blocks, schema.NewContentBlock(&schema.UserInputText{
				Text: fmt.Sprintf("[音频内容：mime_type=%s]", defaultString(content.MIMEType, "unknown")),
			}))
		}
	}
	return blocks
}

func samplingAssistantBlocks(contents []mcp.SamplingContent) ([]*schema.ContentBlock, map[string]string) {
	blocks := make([]*schema.ContentBlock, 0, len(contents))
	names := make(map[string]string)
	for _, content := range contents {
		switch content.Type {
		case "text":
			if strings.TrimSpace(content.Text) == "" {
				continue
			}
			blocks = append(blocks, schema.NewContentBlock(&schema.AssistantGenText{Text: content.Text}))
		case "tool_use":
			callID := content.Data
			name := content.Text
			names[callID] = name
			blocks = append(blocks, schema.NewContentBlock(&schema.FunctionToolCall{
				CallID:    callID,
				Name:      name,
				Arguments: argumentsJSON(content.Metadata["input"]),
			}))
		}
	}
	return blocks, names
}

func samplingContentText(contents []mcp.SamplingContent) string {
	parts := make([]string, 0, len(contents))
	for _, content := range contents {
		if text := strings.TrimSpace(content.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func argumentsJSON(raw any) string {
	arguments, ok := raw.(map[string]any)
	if !ok || len(arguments) == 0 {
		return "{}"
	}
	encoded, err := sonic.MarshalString(arguments)
	if err != nil {
		return "{}"
	}
	return encoded
}

func samplingToolInfos(tools []mcp.SamplingTool) ([]*schema.ToolInfo, error) {
	out := make([]*schema.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		schemaValue := tool.InputSchema
		if len(schemaValue) == 0 {
			schemaValue = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		encoded, err := sonic.Marshal(schemaValue)
		if err != nil {
			return nil, fmt.Errorf("转换 MCP Sampling 工具参数失败: %w", err)
		}
		parsed := &jsonschema.Schema{}
		if err := sonic.Unmarshal(encoded, parsed); err != nil {
			return nil, fmt.Errorf("转换 MCP Sampling 工具参数失败: %w", err)
		}
		out = append(out, &schema.ToolInfo{
			Name:        tool.Name,
			Desc:        tool.Description,
			ParamsOneOf: schema.NewParamsOneOfByJSONSchema(parsed),
		})
	}
	return out, nil
}

func samplingResponse(reply *schema.AgenticMessage, modelName string, _ map[string]string) mcp.SamplingResponse {
	response := mcp.SamplingResponse{Model: modelName}
	if reply == nil {
		return response
	}
	var text strings.Builder
	for _, block := range reply.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil {
				text.WriteString(block.AssistantGenText.Text)
			}
		case schema.ContentBlockTypeFunctionToolCall:
			call := block.FunctionToolCall
			if call == nil {
				continue
			}
			arguments := map[string]any{}
			if strings.TrimSpace(call.Arguments) != "" {
				if err := sonic.UnmarshalString(call.Arguments, &arguments); err != nil {
					arguments = map[string]any{"value": call.Arguments}
				}
			}
			response.ToolCalls = append(response.ToolCalls, mcp.SamplingToolCall{
				CallID:    call.CallID,
				Name:      call.Name,
				Arguments: arguments,
			})
		}
	}
	response.Text = strings.TrimSpace(text.String())
	return response
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// MCPPromptSpecs 返回当前 generation 已注册的 MCP Prompt 声明
func (a *Agent) MCPPromptSpecs() []mcp.PromptSpec {
	generation := a.currentGeneration()
	if generation == nil || generation.mcp == nil {
		return nil
	}
	return generation.mcp.PromptSpecs()
}

// MCPGetPrompt 读取当前 generation 的 MCP Prompt 内容
func (a *Agent) MCPGetPrompt(ctx context.Context, promptName string, arguments map[string]string) (*mcp.PromptResult, error) {
	generation := a.currentGeneration()
	if generation == nil || generation.mcp == nil {
		return nil, fmt.Errorf("MCP 尚未就绪")
	}
	return generation.mcp.GetPrompt(ctx, promptName, arguments)
}

// MCPResourceSpecs 返回当前 generation 已注册的 MCP Resource 声明
func (a *Agent) MCPResourceSpecs() []mcp.ResourceSpec {
	generation := a.currentGeneration()
	if generation == nil || generation.mcp == nil {
		return nil
	}
	return generation.mcp.ResourceSpecs()
}

// MCPResourceTemplateSpecs 返回当前 generation 已注册的 MCP Resource Template 声明
func (a *Agent) MCPResourceTemplateSpecs() []mcp.ResourceTemplateSpec {
	generation := a.currentGeneration()
	if generation == nil || generation.mcp == nil {
		return nil
	}
	return generation.mcp.ResourceTemplateSpecs()
}

// MCPReadResource 读取当前 generation 的 MCP Resource 内容
func (a *Agent) MCPReadResource(ctx context.Context, uri string) (*mcp.ResourceReadResult, error) {
	generation := a.currentGeneration()
	if generation == nil || generation.mcp == nil {
		return nil, fmt.Errorf("MCP 尚未就绪")
	}
	return generation.mcp.ReadResource(ctx, uri)
}

// MCPToolRoutes 返回当前 generation 的 MCP 工具暴露名路由
func (a *Agent) MCPToolRoutes() []mcp.ToolRoute {
	generation := a.currentGeneration()
	if generation == nil || generation.mcp == nil {
		return nil
	}
	return generation.mcp.ToolRoutes()
}
