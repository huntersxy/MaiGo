package model_client

// Context Item 到 Chat Completions messages 的投影
// 对齐 MaiBot _convert_messages：system 只收文本；user 纯文本直接拼接、混合内容用 parts；
// 连续 reasoning/assistant/function_call 折叠为一个 assistant；Provider Activity/Opaque 不进入请求

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
)

func BuildChatMessagesFromContextItems(items []ContextItem, includeTools bool) ([]map[string]any, error) {
	return BuildChatMessagesFromContextItemsWithScope(items, includeTools, nil)
}

// BuildChatMessagesFromContextItemsWithScope 允许回放指定 Provider 范围的原生推理
func BuildChatMessagesFromContextItemsWithScope(items []ContextItem, includeTools bool, reasoningScope *ContextProviderScope) ([]map[string]any, error) {
	messages := make([]map[string]any, 0, len(items))
	// 只接受紧跟在带 tool_calls 的 assistant 之后的工具结果；历史压缩可能留下孤立结果
	pendingCalls := make(map[string]struct{})
	for index := 0; index < len(items); {
		item := items[index]
		switch item.Type {
		case ContextItemSystemMessage:
			clear(pendingCalls)
			content, err := chatSystemContentFromContextItem(item)
			if err != nil {
				return nil, err
			}
			messages = append(messages, map[string]any{"role": "system", "content": content})
			index++
		case ContextItemUserMessage:
			clear(pendingCalls)
			message, err := chatUserMessageFromContextItem(item)
			if err != nil {
				return nil, err
			}
			if message != nil {
				messages = append(messages, message)
			}
			index++
		case ContextItemFunctionCallOutput:
			if includeTools {
				if _, ok := pendingCalls[item.CallID]; ok {
					messages = append(messages, map[string]any{
						"role":         "tool",
						"content":      item.Output,
						"tool_call_id": item.CallID,
					})
					delete(pendingCalls, item.CallID)
				}
			}
			index++
		case ContextItemReasoning, ContextItemAssistantMessage, ContextItemFunctionCall,
			ContextItemProviderActivity, ContextItemProviderOpaque:
			clear(pendingCalls)
			message, next, err := chatAssistantGroupFromContextItems(items, index, includeTools, reasoningScope)
			if err != nil {
				return nil, err
			}
			if message != nil {
				// 只保留紧随其后确实有结果的工具调用；缺失结果时协议要求整组补齐
				outputs := make(map[string]struct{})
				for scan := next; scan < len(items) && items[scan].Type == ContextItemFunctionCallOutput; scan++ {
					if callID := items[scan].CallID; callID != "" {
						outputs[callID] = struct{}{}
					}
				}
				if calls, ok := message["tool_calls"].([]map[string]any); ok {
					kept := calls[:0]
					for _, call := range calls {
						callID, _ := call["id"].(string)
						if _, exists := outputs[callID]; exists {
							kept = append(kept, call)
						}
					}
					if len(kept) == 0 {
						delete(message, "tool_calls")
						if message["content"] == nil {
							if _, hasReasoning := message["reasoning_content"]; !hasReasoning {
								message = nil
							}
						}
					} else {
						message["tool_calls"] = kept
					}
				}
			}
			if message != nil {
				if calls, ok := message["tool_calls"].([]map[string]any); ok {
					for _, call := range calls {
						if callID, _ := call["id"].(string); callID != "" {
							pendingCalls[callID] = struct{}{}
						}
					}
				}
				messages = append(messages, message)
			}
			index = next
		default:
			index++
		}
	}
	return messages, nil
}

func chatSystemContentFromContextItem(item ContextItem) (string, error) {
	var builder strings.Builder
	for _, part := range item.Parts {
		if part.Type != ContextPartText {
			return "", &ParseError{Message: "Chat Completions 的 system 消息仅支持文本内容"}
		}
		builder.WriteString(part.Text)
	}
	return builder.String(), nil
}

func chatUserMessageFromContextItem(item ContextItem) (map[string]any, error) {
	onlyText := true
	for _, part := range item.Parts {
		if part.Type != ContextPartText {
			onlyText = false
			break
		}
	}
	if onlyText {
		var builder strings.Builder
		for _, part := range item.Parts {
			builder.WriteString(part.Text)
		}
		return map[string]any{"role": "user", "content": builder.String()}, nil
	}
	parts := make([]map[string]any, 0, len(item.Parts))
	for _, part := range item.Parts {
		switch part.Type {
		case ContextPartText:
			if strings.TrimSpace(part.Text) != "" {
				parts = append(parts, map[string]any{"type": "text", "text": part.Text})
			}
		case ContextPartImage:
			imagePart, ok := buildChatImagePart(&schema.UserInputImage{
				MIMEType:   ContextImageMIME(part.ImageFormat),
				Base64Data: part.ImageBase64,
			})
			if !ok {
				parts = append(parts, map[string]any{"type": "text", "text": chatImageUnavailableText})
				continue
			}
			parts = append(parts, imagePart)
		case ContextPartRefusal:
			return nil, &ParseError{Message: "Chat Completions 的 user 消息暂不支持 refusal 内容"}
		}
	}
	if len(parts) == 0 {
		return map[string]any{"role": "user", "content": ""}, nil
	}
	return map[string]any{"role": "user", "content": parts}, nil
}

func chatAssistantGroupFromContextItems(items []ContextItem, start int, includeTools bool, reasoningScope *ContextProviderScope) (map[string]any, int, error) {
	texts := make([]string, 0)
	reasoningTexts := make([]string, 0)
	toolCalls := make([]map[string]any, 0)
	index := start
	groupTurn := items[start].Meta.LogicalTurnID
	for ; index < len(items); index++ {
		item := items[index]
		if reasoningScope != nil && index > start && item.Meta.LogicalTurnID != groupTurn {
			break
		}
		switch item.Type {
		case ContextItemReasoning:
			if reasoningScope != nil && item.Replay != nil && item.Replay.Scope == *reasoningScope {
				payload, err := item.Replay.Materialize()
				if err != nil {
					return nil, index, &ParseError{Message: "Provider 推理回放片段无效", Err: err}
				}
				if value, ok := payload["reasoning_content"].(string); ok {
					reasoningTexts = append(reasoningTexts, value)
				}
			}
		case ContextItemProviderActivity, ContextItemProviderOpaque:
		case ContextItemAssistantMessage:
			for _, part := range item.Parts {
				if part.Type != ContextPartText {
					return nil, index, &ParseError{Message: "Chat assistant message 仅支持文本片段"}
				}
				texts = append(texts, part.Text)
			}
		case ContextItemFunctionCall:
			if !includeTools || item.ToolCall == nil || strings.TrimSpace(item.ToolCall.FuncName) == "" {
				continue
			}
			callID := item.ToolCall.CallID
			if callID == "" {
				callID = fmt.Sprintf("tool_call_%d", len(toolCalls))
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      item.ToolCall.FuncName,
					"arguments": chatAssistantContextArguments(item.ToolCall),
				},
			})
		default:
			goto done
		}
	}
done:
	content := strings.Join(texts, "")
	if content == "" && len(toolCalls) == 0 {
		return nil, index, nil
	}
	message := map[string]any{"role": "assistant", "content": nil}
	if content != "" {
		message["content"] = content
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	if len(reasoningTexts) > 0 {
		// DeepSeek 在带工具的请求中要求回传先前 assistant 消息的原生推理字段
		message["reasoning_content"] = strings.Join(reasoningTexts, "")
	}
	return message, index, nil
}

func chatAssistantContextArguments(call *ContextToolCall) string {
	if call == nil || len(call.Args) == 0 {
		return "{}"
	}
	encoded, err := sonic.Marshal(call.Args)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}
