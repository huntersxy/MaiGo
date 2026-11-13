package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcpprotocol "github.com/mark3labs/mcp-go/mcp"
)

// SamplingContent 归一化后的 Sampling 内容项
type SamplingContent struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Data      string `json:"data,omitempty"`
	MIMEType  string `json:"mime_type,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	// StructuredContent 保留 mcp-go 内容块未建模的 tool_result.structuredContent 原始对象
	StructuredContent any            `json:"structured_content,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
}

// SamplingMessage 归一化后的 Sampling 消息；工具结果以 role=tool 单独成条
type SamplingMessage struct {
	Role     string            `json:"role"`
	Contents []SamplingContent `json:"contents"`
	ToolCall string            `json:"tool_call_id,omitempty"`
}

// SamplingTool 服务端允许模型使用的工具定义
type SamplingTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

// SamplingToolCall 模型返回的工具调用
type SamplingToolCall struct {
	CallID    string         `json:"call_id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// SamplingRequest 归一化后的宿主采样请求，供上层 LLM 适配器消费
type SamplingRequest struct {
	TaskName     string            `json:"task_name"`
	SystemPrompt string            `json:"system_prompt"`
	Messages     []SamplingMessage `json:"messages"`
	Temperature  *float64          `json:"temperature,omitempty"`
	MaxTokens    int               `json:"max_tokens"`
	ToolChoice   string            `json:"tool_choice"`
	Tools        []SamplingTool    `json:"tools,omitempty"`
}

// SamplingResponse 宿主 LLM 生成结果
type SamplingResponse struct {
	Text      string             `json:"text"`
	Model     string             `json:"model"`
	ToolCalls []SamplingToolCall `json:"tool_calls,omitempty"`
}

// SamplingGenerator 宿主 LLM 调用入口；由 Agent 侧注入真实模型，测试可注入桩实现
type SamplingGenerator func(ctx context.Context, request SamplingRequest) (SamplingResponse, error)

// SamplingBridge 把 MCP Sampling 请求桥接到宿主 LLM，对应 MaiBot MCPHostLLMBridge
type SamplingBridge struct {
	taskName string
	generate SamplingGenerator
}

// NewSamplingBridge 创建 Sampling 桥接；generate 为 nil 时 CreateMessage 返回错误
func NewSamplingBridge(taskName string, generate SamplingGenerator) *SamplingBridge {
	name := strings.TrimSpace(taskName)
	if name == "" {
		name = DefaultSamplingTaskName
	}
	return &SamplingBridge{taskName: name, generate: generate}
}

// CreateMessage 实现 SamplingHandler
func (b *SamplingBridge) CreateMessage(ctx context.Context, request mcpprotocol.CreateMessageRequest) (*mcpprotocol.CreateMessageResult, error) {
	return b.CreateMessageWithStructured(ctx, request, nil)
}

// CreateMessageWithStructured 在标准 Sampling 请求之外接收 tool_result 的 structuredContent
// mcp-go 的 ToolResultContent 未保留该字段，host 解析原始 JSON-RPC 参数后从这里补充
func (b *SamplingBridge) CreateMessageWithStructured(ctx context.Context, request mcpprotocol.CreateMessageRequest, structured map[string]any) (*mcpprotocol.CreateMessageResult, error) {
	return b.CreateMessageWithTemperature(ctx, request, structured, coerceTemperature(request.Temperature))
}

// CreateMessageWithTemperature 额外接收 temperature 是否出现的原始信息：
// Python 缺省 None 使用模型默认温度，显式 0 则传 0；mcp-go 的 float64 无法区分两者。
func (b *SamplingBridge) CreateMessageWithTemperature(ctx context.Context, request mcpprotocol.CreateMessageRequest, structured map[string]any, temperature *float64) (*mcpprotocol.CreateMessageResult, error) {
	if b == nil || b.generate == nil {
		return nil, fmt.Errorf("MCP Sampling 未配置宿主大模型")
	}
	params := request.CreateMessageParams
	toolChoiceMode := normalizeToolChoiceMode(params.ToolChoice)
	tools := buildSamplingTools(params.Tools, toolChoiceMode)
	systemPrompt := buildSamplingSystemPrompt(params.SystemPrompt, toolChoiceMode, tools)
	messages, err := buildSamplingMessagesWithStructured(params.Messages, structured)
	if err != nil {
		return nil, err
	}

	maxTokens := params.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	samplingRequest := SamplingRequest{
		TaskName:     b.taskName,
		SystemPrompt: systemPrompt,
		Messages:     messages,
		Temperature:  temperature,
		MaxTokens:    maxTokens,
		ToolChoice:   toolChoiceMode,
		Tools:        tools,
	}
	response, err := b.generate(ctx, samplingRequest)
	if err != nil {
		return nil, fmt.Errorf("MCP Sampling 调用失败: %w", err)
	}
	if toolChoiceMode == "required" && len(tools) > 0 && len(response.ToolCalls) == 0 {
		return nil, fmt.Errorf("Sampling 要求必须调用工具，但模型未返回任何工具调用")
	}
	return buildCreateMessageResult(response, len(tools) > 0, b.taskName), nil
}

func normalizeToolChoiceMode(raw *mcpprotocol.ToolChoice) string {
	if raw == nil {
		return "auto"
	}
	mode := strings.ToLower(strings.TrimSpace(string(raw.Mode)))
	switch mode {
	case "required", "none":
		return mode
	default:
		return "auto"
	}
}

func coerceTemperature(value float64) *float64 {
	// mcp-go 用 float64 表示 temperature，缺省与显式 0 无法区分；
	// MaiBot 对显式 0 会保留，这里统一透传 0，避免模型端误用默认温度
	out := value
	return &out
}

func buildSamplingSystemPrompt(rawPrompt, toolChoiceMode string, tools []SamplingTool) string {
	parts := make([]string, 0, 2)
	if trimmed := strings.TrimSpace(rawPrompt); trimmed != "" {
		parts = append(parts, trimmed)
	}
	if toolChoiceMode == "required" && len(tools) > 0 {
		parts = append(parts, "本轮回答必须至少调用一个工具；不要直接结束回答。")
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func buildSamplingTools(rawTools []mcpprotocol.Tool, toolChoiceMode string) []SamplingTool {
	if toolChoiceMode == "none" || len(rawTools) == 0 {
		return nil
	}
	out := make([]SamplingTool, 0, len(rawTools))
	for _, rawTool := range rawTools {
		name := strings.TrimSpace(rawTool.Name)
		if name == "" {
			continue
		}
		schema := toolInputSchema(rawTool)
		title := strings.TrimSpace(rawTool.Title)
		description := strings.TrimSpace(rawTool.Description)
		if description == "" {
			description = title
		}
		if description == "" {
			description = "工具 " + name
		}
		out = append(out, SamplingTool{
			Name:        name,
			Title:       title,
			Description: description,
			InputSchema: schema,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toolInputSchema(rawTool mcpprotocol.Tool) map[string]any {
	if len(rawTool.RawInputSchema) > 0 {
		var schema map[string]any
		if err := json.Unmarshal(rawTool.RawInputSchema, &schema); err == nil && len(schema) > 0 {
			delete(schema, "$schema")
			return schema
		}
	}
	encoded, err := json.Marshal(rawTool.InputSchema)
	if err != nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil || len(schema) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	delete(schema, "$schema")
	return schema
}

func toolOutputSchema(rawTool mcpprotocol.Tool) map[string]any {
	if len(rawTool.RawOutputSchema) > 0 {
		var schema map[string]any
		if err := json.Unmarshal(rawTool.RawOutputSchema, &schema); err == nil && len(schema) > 0 {
			delete(schema, "$schema")
			return schema
		}
	}
	encoded, err := json.Marshal(rawTool.OutputSchema)
	if err != nil {
		return nil
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil || len(schema) == 0 {
		return nil
	}
	delete(schema, "$schema")
	return schema
}

func buildSamplingMessages(rawMessages []mcpprotocol.SamplingMessage) ([]SamplingMessage, error) {
	out := make([]SamplingMessage, 0, len(rawMessages))
	for _, rawMessage := range rawMessages {
		role := strings.ToLower(strings.TrimSpace(string(rawMessage.Role)))
		contents := samplingContents(rawMessage.Content)
		switch role {
		case "assistant":
			message := SamplingMessage{Role: "assistant"}
			for _, content := range contents {
				switch content.Type {
				case "tool_use":
					arguments := normalizeToolArguments(content.Metadata["input"])
					// convertSamplingBlocks 已把工具名放进 Text、调用 ID 放进 Data，这里只补参数
					message.Contents = append(message.Contents, SamplingContent{
						Type:     "tool_use",
						Data:     content.Data,
						Text:     content.Text,
						Metadata: map[string]any{"input": arguments},
					})
				default:
					message.Contents = append(message.Contents, content)
				}
			}
			out = append(out, message)
		case "user":
			pending := SamplingMessage{Role: "user"}
			for _, content := range contents {
				if content.Type == "tool_result" {
					if len(pending.Contents) > 0 {
						out = append(out, pending)
						pending = SamplingMessage{Role: "user"}
					}
					out = append(out, SamplingMessage{
						Role:     "tool",
						ToolCall: content.ToolUseID,
						Contents: []SamplingContent{content},
					})
					continue
				}
				pending.Contents = append(pending.Contents, content)
			}
			if len(pending.Contents) > 0 {
				out = append(out, pending)
			}
		default:
			return nil, fmt.Errorf("不支持的 MCP Sampling 消息角色: %s", role)
		}
	}
	return out, nil
}

// buildSamplingMessagesWithStructured 在归一化消息后补充 tool_result 的结构化结果
// 对齐 MaiBot _summarize_tool_result_content：结构化内容以 JSON 文本追加到工具结果摘要
func buildSamplingMessagesWithStructured(rawMessages []mcpprotocol.SamplingMessage, structured map[string]any) ([]SamplingMessage, error) {
	out, err := buildSamplingMessages(rawMessages)
	if err != nil {
		return nil, err
	}
	if len(structured) == 0 {
		return out, nil
	}
	for messageIndex := range out {
		for contentIndex := range out[messageIndex].Contents {
			content := &out[messageIndex].Contents[contentIndex]
			if content.Type != "tool_result" || content.ToolUseID == "" {
				continue
			}
			value, ok := structured[content.ToolUseID]
			if !ok || value == nil {
				continue
			}
			content.StructuredContent = value
			encoded, encodeErr := json.Marshal(value)
			if encodeErr != nil {
				return nil, fmt.Errorf("序列化 tool_result structuredContent 失败: %w", encodeErr)
			}
			text := strings.TrimSpace(string(encoded))
			if text == "" || text == "null" || text == "{}" {
				continue
			}
			switch {
			case strings.TrimSpace(content.Text) == "":
				content.Text = text
			case content.IsError && strings.TrimSpace(content.Text) == "工具执行失败。":
				// 有结构化结果时按 Python 语义使用带冒号的失败前缀
				content.Text = "工具执行失败：\n" + text
			default:
				content.Text = strings.TrimSpace(content.Text) + "\n" + text
			}
		}
	}
	return out, nil
}

func samplingContents(raw any) []SamplingContent {
	switch value := raw.(type) {
	case nil:
		return nil
	case []mcpprotocol.Content:
		return convertSamplingBlocks(value)
	case mcpprotocol.Content:
		return convertSamplingBlocks([]mcpprotocol.Content{value})
	case []any:
		items := make([]mcpprotocol.Content, 0, len(value))
		for _, item := range value {
			if content, ok := item.(mcpprotocol.Content); ok {
				items = append(items, content)
			}
		}
		return convertSamplingBlocks(items)
	default:
		return convertSamplingBlocks([]mcpprotocol.Content{})
	}
}

func convertSamplingBlocks(blocks []mcpprotocol.Content) []SamplingContent {
	out := make([]SamplingContent, 0, len(blocks))
	for _, block := range blocks {
		switch content := block.(type) {
		case mcpprotocol.TextContent:
			if strings.TrimSpace(content.Text) == "" {
				continue
			}
			out = append(out, SamplingContent{Type: "text", Text: content.Text})
		case mcpprotocol.ImageContent:
			format := normalizeImageFormat(content.MIMEType)
			if strings.TrimSpace(content.Data) == "" || format == "" {
				// 对齐 MaiBot：数据缺失或格式不受支持时降级为文本提示
				out = append(out, SamplingContent{
					Type: "text",
					Text: fmt.Sprintf("[图片内容：mime_type=%s，当前客户端无法直接透传]", fallback(content.MIMEType, "unknown")),
				})
				continue
			}
			out = append(out, SamplingContent{Type: "image", Data: content.Data, MIMEType: content.MIMEType, Text: format})
		case mcpprotocol.AudioContent:
			out = append(out, SamplingContent{Type: "audio", Data: content.Data, MIMEType: content.MIMEType})
		case mcpprotocol.ToolUseContent:
			arguments := normalizeToolArguments(content.Input)
			out = append(out, SamplingContent{
				Type:     "tool_use",
				Text:     content.Name,
				Data:     content.ID,
				Metadata: map[string]any{"input": arguments},
			})
		case mcpprotocol.ToolResultContent:
			items := buildContentItems(content.Content)
			texts := make([]string, 0, len(items))
			for _, item := range items {
				if text := strings.TrimSpace(item.HistoryText()); text != "" {
					texts = append(texts, text)
				}
			}
			summary := strings.Join(texts, "\n")
			if content.IsError {
				summary = summarizeFailedToolResult(summary)
			}
			out = append(out, SamplingContent{
				Type:      "tool_result",
				ToolUseID: content.ToolUseID,
				Text:      summary,
				IsError:   content.IsError,
			})
		}
	}
	return out
}

// summarizeFailedToolResult 对齐 MaiBot _summarize_tool_result_content 的失败前缀
func summarizeFailedToolResult(text string) string {
	if strings.TrimSpace(text) == "" {
		return "工具执行失败。"
	}
	return "工具执行失败：\n" + text
}

func normalizeImageFormat(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return "png"
	case "image/jpeg", "image/jpg":
		return "jpeg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	default:
		return ""
	}
}

func normalizeToolArguments(raw any) map[string]any {
	switch value := raw.(type) {
	case nil:
		return map[string]any{}
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			out[key] = item
		}
		return out
	default:
		return map[string]any{"value": value}
	}
}

func buildCreateMessageResult(response SamplingResponse, toolsEnabled bool, taskName string) *mcpprotocol.CreateMessageResult {
	modelName := strings.TrimSpace(response.Model)
	if modelName == "" {
		modelName = taskName
	}
	text := response.Text
	if !toolsEnabled {
		return &mcpprotocol.CreateMessageResult{
			SamplingMessage: mcpprotocol.SamplingMessage{
				Role:    mcpprotocol.RoleAssistant,
				Content: mcpprotocol.TextContent{Type: "text", Text: text},
			},
			Model:      modelName,
			StopReason: "endTurn",
		}
	}

	blocks := make([]mcpprotocol.Content, 0, len(response.ToolCalls)+1)
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, mcpprotocol.TextContent{Type: "text", Text: text})
	}
	for _, toolCall := range response.ToolCalls {
		blocks = append(blocks, mcpprotocol.ToolUseContent{
			Type:  "tool_use",
			ID:    toolCall.CallID,
			Name:  toolCall.Name,
			Input: toolCall.Arguments,
		})
	}
	if len(blocks) == 0 {
		blocks = append(blocks, mcpprotocol.TextContent{Type: "text", Text: ""})
	}
	var content any = blocks
	if len(blocks) == 1 {
		content = blocks[0]
	}
	stopReason := "endTurn"
	if len(response.ToolCalls) > 0 {
		stopReason = "toolUse"
	}
	return &mcpprotocol.CreateMessageResult{
		SamplingMessage: mcpprotocol.SamplingMessage{
			Role:    mcpprotocol.RoleAssistant,
			Content: content,
		},
		Model:      modelName,
		StopReason: stopReason,
	}
}
