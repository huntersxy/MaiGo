package replyer

// Replyer 请求/响应 Context Items 与 eino AgenticMessage 的桥接
// generator 以 Context Items 表达请求和输出，Agent 侧模型客户端仍消费 eino 消息

import (
	"fmt"
	"strings"

	"maibot-go/internal/llm_models"

	"github.com/cloudwego/eino/schema"
)

// ContextItemsToAgenticMessages 把 replyer 请求上下文投影为 eino 消息
// 保留文本与图片原顺序；工具调用/结果按 eino 原生块回放
func ContextItemsToAgenticMessages(items []llm.ContextItem) ([]*schema.AgenticMessage, error) {
	messages := make([]*schema.AgenticMessage, 0, len(items))
	for _, item := range items {
		switch item.Type {
		case llm.ContextItemSystemMessage:
			if text := strings.TrimSpace(llm.GetContextItemText(item)); text != "" {
				messages = append(messages, schema.SystemAgenticMessage(text))
			}
		case llm.ContextItemUserMessage:
			blocks := requestContentBlocks(item.Parts, false)
			if len(blocks) > 0 {
				messages = append(messages, &schema.AgenticMessage{
					Role: schema.AgenticRoleTypeUser, ContentBlocks: blocks,
				})
			}
		case llm.ContextItemAssistantMessage:
			blocks := requestContentBlocks(item.Parts, true)
			if len(blocks) > 0 {
				messages = append(messages, &schema.AgenticMessage{
					Role: schema.AgenticRoleTypeAssistant, ContentBlocks: blocks,
				})
			}
		case llm.ContextItemReasoning:
			text := strings.TrimSpace(llm.GetContextItemText(item))
			if text == "" {
				continue
			}
			block := schema.NewContentBlock(&schema.Reasoning{Text: text})
			if len(messages) > 0 && messages[len(messages)-1].Role == schema.AgenticRoleTypeAssistant {
				messages[len(messages)-1].ContentBlocks = append(messages[len(messages)-1].ContentBlocks, block)
				continue
			}
			messages = append(messages, &schema.AgenticMessage{
				Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{block},
			})
		case llm.ContextItemFunctionCall:
			if item.ToolCall == nil {
				continue
			}
			blocks := []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolCall{
				CallID: item.ToolCall.CallID, Name: item.ToolCall.FuncName,
				Arguments: string(item.ToolCall.ArgsJSON()),
			})}
			messages = append(messages, &schema.AgenticMessage{
				Role: schema.AgenticRoleTypeAssistant, ContentBlocks: blocks,
			})
		case llm.ContextItemFunctionCallOutput:
			output := item.Output
			result := &schema.FunctionToolResult{
				CallID: item.CallID, Name: item.ToolName,
				Content: []*schema.FunctionToolResultContentBlock{{
					Type: schema.FunctionToolResultContentBlockTypeText,
					Text: &schema.UserInputText{Text: output},
				}},
			}
			messages = append(messages, &schema.AgenticMessage{
				Role:          schema.AgenticRoleTypeUser,
				ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(result)},
			})
		case llm.ContextItemProviderActivity, llm.ContextItemProviderOpaque:
			continue
		}
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("replyer 请求上下文为空")
	}
	return messages, nil
}

func requestContentBlocks(parts []llm.ContextContentPart, assistant bool) []*schema.ContentBlock {
	blocks := make([]*schema.ContentBlock, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case llm.ContextPartText:
			if part.Text == "" {
				continue
			}
			if assistant {
				blocks = append(blocks, schema.NewContentBlock(&schema.AssistantGenText{Text: part.Text}))
			} else {
				blocks = append(blocks, schema.NewContentBlock(&schema.UserInputText{Text: part.Text}))
			}
		case llm.ContextPartRefusal:
			if part.Refusal == "" {
				continue
			}
			blocks = append(blocks, schema.NewContentBlock(&schema.UserInputText{Text: part.Refusal}))
		case llm.ContextPartImage:
			if part.ImageBase64 == "" {
				continue
			}
			if assistant {
				blocks = append(blocks, schema.NewContentBlock(&schema.AssistantGenImage{
					Base64Data: part.ImageBase64, MIMEType: replyerContextImageMIME(part.ImageFormat),
				}))
			} else {
				blocks = append(blocks, schema.NewContentBlock(&schema.UserInputImage{
					Base64Data: part.ImageBase64, MIMEType: replyerContextImageMIME(part.ImageFormat),
				}))
			}
		}
	}
	return blocks
}

func replyerContextImageMIME(format string) string {
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

// OutputItemsFromAgenticMessage 把模型原生 eino 响应还原为输出 Context Items
func OutputItemsFromAgenticMessage(message *schema.AgenticMessage) ([]llm.ContextItem, error) {
	if message == nil {
		return nil, fmt.Errorf("replyer 模型返回了空响应")
	}
	items, err := llm.ContextItemsFromMessages([]*schema.AgenticMessage{message})
	if err != nil {
		return nil, fmt.Errorf("投影 replyer 模型输出失败: %w", err)
	}
	return items, nil
}
