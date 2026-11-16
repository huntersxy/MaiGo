package replyer

// Replyer 工具监控详情，对齐 build_reply_monitor_detail

import (
	"fmt"
	"strings"

	"MaiGo/internal/llm_models"
)

// BuildReplyMonitorDetail 构建 reply 工具统一监控详情结构
func BuildReplyMonitorDetail(result ReplyGenerationResult) map[string]any {
	detail := map[string]any{}
	promptText := strings.TrimSpace(result.Completion.RequestPrompt)
	reasoningText := strings.TrimSpace(result.Completion.ReasoningText)
	outputText := strings.TrimSpace(result.Completion.ResponseText)

	if len(result.RequestMessages) > 0 {
		detail["request_messages"] = llm.SerializeContextItemsSnapshot(result.RequestMessages)
	} else if promptText != "" {
		detail["prompt_text"] = promptText
	}
	if result.RequestMessageCount > 0 {
		detail["request_messages_sanitized"] = len(result.RequestMessages) > 0
		detail["request_message_count"] = result.RequestMessageCount
	}
	if reasoningText != "" {
		detail["reasoning_text"] = reasoningText
	}
	if outputText != "" {
		detail["output_text"] = outputText
	}
	if len(result.OutputItems) > 0 {
		detail["output_items"] = llm.SerializeContextItemsSnapshot(result.OutputItems)
	}
	if len(result.GenerationAttempts) > 0 {
		detail["generation_attempts"] = serializeReplyGenerationAttempts(result.GenerationAttempts)
	}
	if strings.TrimSpace(result.PromptHTMLURI) != "" {
		detail["prompt_html_uri"] = strings.TrimSpace(result.PromptHTMLURI)
	}

	metrics := map[string]any{}
	if modelName := strings.TrimSpace(result.Completion.ModelName); modelName != "" {
		metrics["model_name"] = modelName
	}
	if result.Completion.PromptTokens > 0 {
		metrics["prompt_tokens"] = result.Completion.PromptTokens
	}
	if result.Completion.CompletionTokens > 0 {
		metrics["completion_tokens"] = result.Completion.CompletionTokens
	}
	if result.Completion.TotalTokens > 0 {
		metrics["total_tokens"] = result.Completion.TotalTokens
	}
	metrics["prompt_ms"] = result.Metrics.PromptMS
	metrics["llm_ms"] = result.Metrics.LLMMS
	metrics["overall_ms"] = result.Metrics.OverallMS
	detail["metrics"] = metrics

	extraSections := make([]map[string]any, 0, 3)
	if content := formatSelectedExpressions(result); content != "" {
		extraSections = append(extraSections, map[string]any{"title": "已选表达方式", "content": content})
	}
	if len(result.Metrics.StageLogs) > 0 {
		extraSections = append(extraSections, map[string]any{"title": "阶段日志", "content": strings.Join(result.Metrics.StageLogs, "\n")})
	}
	if message := strings.TrimSpace(result.ErrorMessage); message != "" {
		extraSections = append(extraSections, map[string]any{"title": "错误信息", "content": message})
	}
	if len(extraSections) > 0 {
		detail["extra_sections"] = extraSections
	}
	return detail
}

func formatSelectedExpressions(result ReplyGenerationResult) string {
	if len(result.SelectedExpressionDetails) > 0 {
		detailMap := map[int]map[string]any{}
		for _, expression := range result.SelectedExpressionDetails {
			if id, ok := integerValue(expression["id"]); ok {
				detailMap[id] = expression
			}
		}
		type orderedDetail struct {
			detail     map[string]any
			fallbackID *int
		}
		ordered := make([]orderedDetail, 0, len(result.SelectedExpressionDetails)+len(result.SelectedExpressionIDs))
		seen := map[int]struct{}{}
		for _, expressionID := range result.SelectedExpressionIDs {
			id := expressionID
			detail, exists := detailMap[expressionID]
			if !exists {
				ordered = append(ordered, orderedDetail{fallbackID: &id})
				continue
			}
			ordered = append(ordered, orderedDetail{detail: detail, fallbackID: &id})
			seen[expressionID] = struct{}{}
		}
		for _, detail := range result.SelectedExpressionDetails {
			id, ok := integerValue(detail["id"])
			if ok {
				if _, exists := seen[id]; exists {
					continue
				}
				ordered = append(ordered, orderedDetail{detail: detail, fallbackID: &id})
				continue
			}
			ordered = append(ordered, orderedDetail{detail: detail})
		}
		lines := make([]string, 0, len(ordered))
		for _, item := range ordered {
			if line := formatSelectedExpressionLine(item.detail, item.fallbackID); line != "" {
				lines = append(lines, line)
			}
		}
		if len(lines) > 0 {
			return strings.Join(lines, "\n")
		}
	}
	if len(result.SelectedExpressionIDs) > 0 {
		parts := make([]string, 0, len(result.SelectedExpressionIDs))
		for _, id := range result.SelectedExpressionIDs {
			parts = append(parts, fmt.Sprint(id))
		}
		return strings.Join(parts, ", ")
	}
	return ""
}

func formatSelectedExpressionLine(expression map[string]any, fallbackID *int) string {
	var expressionID *int
	if id, ok := integerValue(expression["id"]); ok {
		expressionID = &id
	} else {
		expressionID = fallbackID
	}
	situation := strings.TrimSpace(stringValue(expression["situation"]))
	style := strings.TrimSpace(stringValue(expression["style"]))
	prefix := ""
	if expressionID != nil {
		prefix = fmt.Sprint(*expressionID) + "："
	}
	switch {
	case situation != "" && style != "":
		return "[" + prefix + "] " + situation + " -> " + style
	case situation != "":
		return prefix + situation
	case style != "":
		return prefix + style
	case expressionID != nil:
		return fmt.Sprint(*expressionID)
	default:
		return ""
	}
}

func integerValue(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}
