package model_client

// Context Item 与 eino AgenticMessage 的投影
// 本文件提供：
//   - 历史/请求消息 → Context Items（chat/gemini/插件请求与失败快照）
//   - 模型输出 Context Items → AgenticMessage（插件响应）
// eino 不表达的 audio/video/file 以文本占位，避免伪造多模态内容

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// ContextItemsFromMessages 将 eino 消息投影为 Context Items
func ContextItemsFromMessages(messages []*schema.AgenticMessage) ([]ContextItem, error) {
	items := make([]ContextItem, 0, len(messages))
	callTurns := make(map[string]string)
	// Gemini 的 thought_signature 可能在块级 Extra、也可能在消息级索引里
	signatureIndex := geminiSignatureIndex(messages)
	for _, message := range messages {
		if message == nil {
			continue
		}
		switch message.Role {
		case schema.AgenticRoleTypeSystem:
			parts := make([]ContextContentPart, 0)
			for _, block := range message.ContentBlocks {
				if block == nil || block.UserInputText == nil || block.UserInputText.Text == "" {
					continue
				}
				parts = append(parts, ContextContentPart{Type: ContextPartText, Text: block.UserInputText.Text})
			}
			if len(parts) == 0 {
				continue
			}
			item := NewSystemMessageItem(parts...)
			items = append(items, item)
		case schema.AgenticRoleTypeUser:
			userItems, err := projectUserMessage(message, callTurns)
			if err != nil {
				return nil, err
			}
			items = append(items, userItems...)
		case schema.AgenticRoleTypeAssistant:
			assistantItems := projectAssistantMessage(message, callTurns, signatureIndex)
			items = append(items, assistantItems...)
		default:
			continue
		}
	}
	// 与 MaiBot 在 _build_client_request 前调用 normalize_context_images 一致，
	// 保证 chat/gemini/plugin 与失败快照消费同一份图片尺寸治理结果
	return normalizeContextImages(items), nil
}

func projectUserMessage(message *schema.AgenticMessage, callTurns map[string]string) ([]ContextItem, error) {
	logicalTurnID := strings.ReplaceAll(NewContextUUID(), "-", "")
	items := make([]ContextItem, 0, len(message.ContentBlocks))
	textParts := make([]ContextContentPart, 0, len(message.ContentBlocks))
	flushText := func() {
		if len(textParts) == 0 {
			return
		}
		item := NewUserMessageItem(textParts...)
		item.Meta.LogicalTurnID = logicalTurnID
		items = append(items, item)
		textParts = nil
	}
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		switch {
		case block.UserInputText != nil:
			if block.UserInputText.Text == "" {
				continue
			}
			textParts = append(textParts, ContextContentPart{Type: ContextPartText, Text: block.UserInputText.Text})
		case block.UserInputImage != nil:
			part, ok, err := projectUserImage(block.UserInputImage)
			if err != nil {
				return nil, err
			}
			if ok {
				textParts = append(textParts, part)
			}
		case block.UserInputAudio != nil:
			audio := block.UserInputAudio
			textParts = append(textParts, ContextContentPart{
				Type:  ContextPartText,
				Text:  mediaPlaceholderText("音频", audio.URL, audio.Base64Data, audio.FileID),
				Media: &ContextMedia{Kind: "audio", MIMEType: audio.MIMEType, Base64Data: audio.Base64Data, URL: audio.URL, FileID: audio.FileID},
			})
		case block.UserInputVideo != nil:
			video := block.UserInputVideo
			textParts = append(textParts, ContextContentPart{
				Type:  ContextPartText,
				Text:  mediaPlaceholderText("视频", video.URL, video.Base64Data, video.FileID),
				Media: &ContextMedia{Kind: "video", MIMEType: video.MIMEType, Base64Data: video.Base64Data, URL: video.URL, FileID: video.FileID},
			})
		case block.UserInputFile != nil:
			file := block.UserInputFile
			name := file.Name
			if name == "" {
				name = "未命名文件"
			}
			textParts = append(textParts, ContextContentPart{
				Type:  ContextPartText,
				Text:  "[文件：" + name + "]",
				Media: &ContextMedia{Kind: "file", MIMEType: file.MIMEType, Base64Data: file.Base64Data, URL: file.URL, FileID: file.FileID, Name: name},
			})
		case block.FunctionToolResult != nil:
			flushText()
			result := block.FunctionToolResult
			if result.CallID == "" {
				return nil, fmt.Errorf("工具结果缺少调用 ID")
			}
			turnID := callTurns[result.CallID]
			if turnID == "" {
				turnID = logicalTurnID
			}
			item := NewFunctionCallOutputItem(turnID, result.CallID, contextToolResultText(result), result.Name)
			items = append(items, item)
		}
	}
	flushText()
	return items, nil
}

func projectAssistantMessage(message *schema.AgenticMessage, callTurns map[string]string, signatureIndex map[string]string) []ContextItem {
	logicalTurnID := strings.ReplaceAll(NewContextUUID(), "-", "")
	items := make([]ContextItem, 0, len(message.ContentBlocks))
	textParts := make([]ContextContentPart, 0, len(message.ContentBlocks))
	flushText := func() {
		if len(textParts) == 0 {
			return
		}
		item := NewAssistantMessageItem(logicalTurnID, textParts...)
		items = append(items, item)
		textParts = nil
	}
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		switch {
		case block.Reasoning != nil:
			text := strings.TrimSpace(block.Reasoning.Text)
			replay := projectChatReasoningReplay(block.Extra)
			if text == "" && replay == nil {
				continue
			}
			flushText()
			item := NewReasoningItem(logicalTurnID, text)
			if replay != nil {
				item.Replay = replay
				if text == "" {
					// OPAQUE reasoning 只承载 Provider 回放，不展示为思考正文
					item.Representation = ""
				}
			}
			items = append(items, item)
		case block.AssistantGenText != nil:
			if block.AssistantGenText.Text == "" {
				continue
			}
			textParts = append(textParts, ContextContentPart{Type: ContextPartText, Text: block.AssistantGenText.Text})
		case block.AssistantGenImage != nil:
			part, ok, err := projectAssistantImage(block.AssistantGenImage)
			if err == nil && ok {
				textParts = append(textParts, part)
			}
		case block.FunctionToolCall != nil:
			flushText()
			call := block.FunctionToolCall
			args := parseProjectedToolArguments(call.Arguments)
			toolCall := NewContextToolCall(call.CallID, call.Name, args)
			callTurns[call.CallID] = logicalTurnID
			item := NewFunctionCallItem(logicalTurnID, toolCall)
			if signature := strings.TrimSpace(signatureIndex[call.CallID]); signature != "" {
				item.ProviderData = map[string]any{geminiExtraThoughtSignatureKey: signature}
			}
			items = append(items, item)
		case block.ServerToolCall != nil:
			flushText()
			items = append(items, projectServerToolCall(block.ServerToolCall, logicalTurnID))
		case block.ServerToolResult != nil:
			flushText()
			items = append(items, projectServerToolResult(block.ServerToolResult, logicalTurnID))
		case block.MCPToolCall != nil:
			flushText()
			items = append(items, projectMCPToolCall(block.MCPToolCall, logicalTurnID))
		case block.MCPToolResult != nil:
			flushText()
			items = append(items, projectMCPToolResult(block.MCPToolResult, logicalTurnID))
		case block.MCPListToolsResult != nil:
			flushText()
			items = append(items, projectMCPListToolsResult(block.MCPListToolsResult, logicalTurnID))
		case block.MCPToolApprovalRequest != nil:
			flushText()
			items = append(items, projectMCPApprovalRequest(block.MCPToolApprovalRequest, logicalTurnID))
		case block.MCPToolApprovalResponse != nil:
			flushText()
			items = append(items, projectMCPApprovalResponse(block.MCPToolApprovalResponse, logicalTurnID))
		default:
			if block.Type == "" {
				continue
			}
			flushText()
			items = append(items, projectProviderOpaqueBlock(block, logicalTurnID))
		}
	}
	flushText()
	return items
}

func projectUserImage(image *schema.UserInputImage) (ContextContentPart, bool, error) {
	if image.Base64Data == "" {
		if image.URL != "" {
			return ContextContentPart{Type: ContextPartText, Text: "[图片链接: " + image.URL + "]"}, true, nil
		}
		if image.FileID != "" {
			return ContextContentPart{Type: ContextPartText, Text: "[图片文件: " + image.FileID + "]"}, true, nil
		}
		return ContextContentPart{}, false, nil
	}
	format := snapshotImageExtension(image.MIMEType)
	if !supportedContextImageFormats[format] {
		return ContextContentPart{Type: ContextPartText, Text: "[图片格式不支持: " + format + "]"}, true, nil
	}
	// 对齐 Python ContextImagePart：投影阶段不校验 base64，坏图由 normalize 与客户端降级
	return ContextContentPart{
		Type:        ContextPartImage,
		ImageFormat: format,
		ImageBase64: image.Base64Data,
	}, true, nil
}

func projectAssistantImage(image *schema.AssistantGenImage) (ContextContentPart, bool, error) {
	if image.Base64Data == "" {
		if image.URL != "" {
			return ContextContentPart{Type: ContextPartText, Text: "[图片链接: " + image.URL + "]"}, true, nil
		}
		return ContextContentPart{}, false, nil
	}
	format := snapshotImageExtension(image.MIMEType)
	if !supportedContextImageFormats[format] {
		return ContextContentPart{Type: ContextPartText, Text: "[图片格式不支持: " + format + "]"}, true, nil
	}
	// 对齐 Python ContextImagePart：投影阶段不校验 base64，坏图由 normalize 与客户端降级
	return ContextContentPart{
		Type:        ContextPartImage,
		ImageFormat: format,
		ImageBase64: image.Base64Data,
	}, true, nil
}

func parseProjectedToolArguments(raw string) map[string]any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}
	}
	decoded, err := chatDecodeToolArgumentValue(trimmed)
	if err != nil {
		return map[string]any{"unparsed_arguments": raw}
	}
	if mapped, ok := decoded.(map[string]any); ok {
		return mapped
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return map[string]any{"unparsed_arguments": raw}
	}
	return map[string]any{"value": json.RawMessage(encoded)}
}

func contextToolResultText(result *schema.FunctionToolResult) string {
	if result == nil || len(result.Content) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, content := range result.Content {
		if content == nil {
			continue
		}
		if content.Text != nil {
			builder.WriteString(content.Text.Text)
			continue
		}
		switch content.Type {
		case schema.FunctionToolResultContentBlockTypeImage:
			builder.WriteString("[工具返回了图片内容]")
		case schema.FunctionToolResultContentBlockTypeAudio:
			builder.WriteString("[工具返回了音频内容]")
		case schema.FunctionToolResultContentBlockTypeVideo:
			builder.WriteString("[工具返回了视频内容]")
		case schema.FunctionToolResultContentBlockTypeFile:
			builder.WriteString("[工具返回了文件内容]")
		}
	}
	return builder.String()
}

func mediaPlaceholderText(kind, rawURL, base64Data, fileID string) string {
	if rawURL != "" {
		return "[" + kind + "链接: " + rawURL + "]"
	}
	if fileID != "" {
		return "[" + kind + "文件: " + fileID + "]"
	}
	if base64Data != "" {
		return "[" + kind + "内容]"
	}
	return "[" + kind + "内容不可用]"
}

// AgenticMessageFromContextItems 将模型输出 Context Items 还原为 eino 助手消息
func AgenticMessageFromContextItems(items []ContextItem) (*schema.AgenticMessage, error) {
	blocks := make([]*schema.ContentBlock, 0, len(items))
	for _, item := range items {
		switch item.Type {
		case ContextItemReasoning:
			text := GetContextItemText(item)
			if text == "" {
				continue
			}
			blocks = append(blocks, schema.NewContentBlock(&schema.Reasoning{Text: text}))
		case ContextItemAssistantMessage:
			for _, part := range item.Parts {
				switch part.Type {
				case ContextPartText:
					if part.Text == "" {
						continue
					}
					blocks = append(blocks, schema.NewContentBlock(&schema.AssistantGenText{Text: part.Text}))
				case ContextPartImage:
					decoded, err := base64.StdEncoding.DecodeString(part.ImageBase64)
					if err != nil {
						return nil, fmt.Errorf("图片 base64 解码失败: %w", err)
					}
					blocks = append(blocks, schema.NewContentBlock(&schema.AssistantGenImage{
						Base64Data: base64.StdEncoding.EncodeToString(decoded),
						MIMEType:   ContextImageMIME(part.ImageFormat),
					}))
				}
			}
		case ContextItemFunctionCall:
			if item.ToolCall == nil {
				continue
			}
			blocks = append(blocks, schema.NewContentBlock(&schema.FunctionToolCall{
				CallID:    item.ToolCall.CallID,
				Name:      item.ToolCall.FuncName,
				Arguments: string(item.ToolCall.ArgsJSON()),
			}))
		}
	}
	if len(blocks) == 0 {
		return nil, &EmptyResponseError{Message: "响应中既无文本内容也无工具调用"}
	}
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: blocks}, nil
}

func ContextImageMIME(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return "image/png"
	}
}

func NewContextUUID() string {
	return strings.ReplaceAll(NewContextItemMeta("").ItemID, "-", "")
}

// projectChatReasoningReplay 还原附在 reasoning block 上的 Provider 回放片段
func projectChatReasoningReplay(extra map[string]any) *ContextProviderReplayFragment {
	if len(extra) == 0 {
		return nil
	}
	raw, ok := extra[chatReasoningReplayExtraKey].(map[string]any)
	if !ok {
		return nil
	}
	scopeMap, ok := raw["scope"].(map[string]any)
	if !ok {
		return nil
	}
	payloadMap, ok := raw["payload"].(map[string]any)
	if !ok {
		return nil
	}
	scope := ContextProviderScope{
		SchemaVersion:       replayIntValue(scopeMap["schema_version"]),
		ClientType:          strings.TrimSpace(replayStringValue(scopeMap["client_type"])),
		ProviderName:        strings.TrimSpace(replayStringValue(scopeMap["provider_name"])),
		EndpointFingerprint: strings.TrimSpace(replayStringValue(scopeMap["endpoint_fingerprint"])),
		ModelIdentifier:     strings.TrimSpace(replayStringValue(scopeMap["model_identifier"])),
	}
	fragment, err := newContextProviderReplayFragment(scope, payloadMap)
	if err != nil {
		return nil
	}
	return &fragment
}

func replayStringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func replayIntValue(value any) int {
	switch number := value.(type) {
	case int:
		return number
	case int64:
		return int(number)
	case float64:
		return int(number)
	case json.Number:
		parsed, err := number.Int64()
		if err != nil {
			return 0
		}
		return int(parsed)
	default:
		return 0
	}
}
