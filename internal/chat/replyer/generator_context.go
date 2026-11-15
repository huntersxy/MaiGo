package replyer

import (
	"fmt"
	"strings"

	"MaiGo/internal/learners"
	"MaiGo/internal/llm_models"
)

// ShouldKeepReplyerHistoryMessage 对齐 _should_keep_replyer_history_message：
// replyer 只接收真实聊天上下文，排除通知、模型输出、参考与工具结果
func ShouldKeepReplyerHistoryMessage(message ChatMessage) bool {
	if message.IsNotify {
		return false
	}
	switch strings.TrimSpace(message.SourceKind) {
	case "model_output", "tool", "tool_result", "reference", "chat_history", "sub_agent":
		return false
	}
	return true
}

// filterReplyerHistory 对齐 filtered_history 列表推导
func filterReplyerHistory(history []ChatMessage) []ChatMessage {
	filtered := make([]ChatMessage, 0, len(history))
	for _, message := range history {
		if ShouldKeepReplyerHistoryMessage(message) {
			filtered = append(filtered, message)
		}
	}
	return filtered
}

// requestMessagesToContextItems 把提示装配结果转换为 ContextItem，供模型端口与 Hook 使用
func requestMessagesToContextItems(messages []RequestMessage) ([]llm.ContextItem, error) {
	items := make([]llm.ContextItem, 0, len(messages))
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		parts := contextPartsFromRequestParts(role, message)
		if len(parts) == 0 {
			continue
		}
		switch role {
		case "system":
			items = append(items, llm.NewSystemMessageItem(parts...))
		case "user":
			items = append(items, llm.NewUserMessageItem(parts...))
		case "assistant":
			items = append(items, llm.NewAssistantMessageItem("", parts...))
		default:
			items = append(items, llm.NewUserMessageItem(parts...))
		}
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("replyer 请求为空")
	}
	return items, nil
}

func contextPartsFromRequestParts(role string, message RequestMessage) []llm.ContextContentPart {
	parts := make([]llm.ContextContentPart, 0, len(message.Parts)+1)
	appendText := func(text string) {
		if text == "" {
			return
		}
		parts = append(parts, llm.ContextContentPart{Type: llm.ContextPartText, Text: text})
	}
	if len(message.Parts) == 0 {
		appendText(message.Content)
		return parts
	}
	for _, part := range message.Parts {
		switch part.Kind {
		case "text":
			appendText(part.Text)
		case "image":
			if role != "user" || part.Image == nil {
				continue
			}
			format := normalizeContextImageFormat(part.Image.Format)
			if format == "" || part.Image.Base64 == "" {
				continue
			}
			parts = append(parts, llm.ContextContentPart{
				Type:        llm.ContextPartImage,
				ImageFormat: format,
				ImageBase64: part.Image.Base64,
			})
		default:
			appendText(part.Text)
		}
	}
	return parts
}

func normalizeContextImageFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpg", "jpeg":
		return "jpeg"
	case "png":
		return "png"
	case "webp":
		return "webp"
	case "gif":
		return "gif"
	default:
		return ""
	}
}

// contextItemsToRequestMessages 是 requestMessagesToContextItems 的反向投影，供测试与旧调用方
func contextItemsToRequestMessages(items []llm.ContextItem) []RequestMessage {
	messages := make([]RequestMessage, 0, len(items))
	for _, item := range items {
		var role string
		switch item.Type {
		case llm.ContextItemSystemMessage:
			role = "system"
		case llm.ContextItemUserMessage:
			role = "user"
		case llm.ContextItemAssistantMessage:
			role = "assistant"
		default:
			continue
		}
		parts := make([]RequestPart, 0, len(item.Parts))
		var textBuilder strings.Builder
		for _, part := range item.Parts {
			switch part.Type {
			case llm.ContextPartText:
				textBuilder.WriteString(part.Text)
				parts = append(parts, RequestPart{Kind: "text", Text: part.Text})
			case llm.ContextPartImage:
				image := RequestImage{Format: part.NormalizedImageFormat(), Base64: part.ImageBase64}
				parts = append(parts, RequestPart{Kind: "image", Image: &image})
			case llm.ContextPartRefusal:
				textBuilder.WriteString(part.Refusal)
				parts = append(parts, RequestPart{Kind: "text", Text: part.Refusal})
			}
		}
		messages = append(messages, RequestMessage{Role: role, Content: textBuilder.String(), Parts: parts})
	}
	return messages
}

// expressionContextMessages 把 replyer 历史投影成表达选择器需要的 learners.ContextMessage
func expressionContextMessages(history []ChatMessage) []learners.ContextMessage {
	out := make([]learners.ContextMessage, 0, len(history))
	for _, message := range history {
		text := strings.TrimSpace(firstNonEmpty(message.Content, message.VisibleReply, buildTextFromParts(message.Parts)))
		sourceKind := strings.TrimSpace(message.SourceKind)
		if sourceKind == "" {
			switch message.Role {
			case "assistant":
				sourceKind = "assistant"
			case "user":
				sourceKind = "user"
			}
		}
		out = append(out, learners.ContextMessage{
			MessageID:  message.MessageID,
			SourceKind: sourceKind,
			Text:       text,
			IsBotSelf:  message.IsBotSelf,
			Platform:   message.Platform,
			UserID:     message.SenderID,
			Name:       message.SenderName,
			SessionID:  "",
		})
	}
	return out
}
