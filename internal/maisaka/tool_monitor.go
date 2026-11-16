package maisaka

import (
	"math"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"MaiGo/internal/maisaka/monitor"
)

// logToolCallSource 记录工具调用来自正文还是推理内容，对应 Python _log_tool_call_source
func (e *ReasoningEngine) logToolCallSource(call ToolCall, index, total int) {
	if e == nil || e.runtime == nil {
		return
	}
	stage := "Planner " + strconv.Itoa(index) + "/" + strconv.Itoa(total)
	source := "response"
	category := "正文工具调用"
	if call.FromReasoning {
		source = "reasoning"
		category = "推理中工具调用"
	}
	zap.L().Info(e.runtime.logPrefix+" ["+category+"] "+stage+": "+
		"工具="+call.Name+" 调用ID="+call.CallID,
		zap.String("tool_call_id", call.CallID),
		zap.String("tool", call.Name),
		zap.String("source", source),
	)
}

// buildToolMonitorResult 构造 planner.finalized 中单个工具的监控结果
func buildToolMonitorResult(call ToolCall, invocation ToolInvocation, result ToolExecutionResult, spec *ToolSpec, durationMS float64, summary string) map[string]any {
	source := "response"
	sourceLabel := "正文调用"
	if call.FromReasoning {
		source = "reasoning"
		sourceLabel = "推理中调用"
	}
	if value, ok := call.ExtraContent["tool_call_source"].(string); ok && strings.TrimSpace(value) != "" {
		source = strings.TrimSpace(value)
		sourceLabel = ""
	}
	if value, ok := call.ExtraContent["tool_call_source_label"].(string); ok && strings.TrimSpace(value) != "" {
		sourceLabel = strings.TrimSpace(value)
	}
	toolTitle := ""
	if spec != nil {
		toolTitle = strings.TrimSpace(spec.Title)
	}
	record := map[string]any{
		"tool_call_id":           call.CallID,
		"tool_name":              call.Name,
		"tool_title":             toolTitle,
		"tool_args":              normalizeMonitorValue(invocation.Arguments),
		"tool_call_source":       source,
		"tool_call_source_label": sourceLabel,
		"success":                result.Success,
		"error_message":          toolResultErrorMessage(result),
		"stop_after_execution":   result.StopAfterExecution,
		"duration_ms":            math.Round(durationMS*100) / 100,
		"summary":                summary,
		"detail":                 normalizeMonitorValue(metadataValue(result.Metadata, "monitor_detail")),
		"card":                   normalizeMonitorValue(metadataValue(result.Metadata, "monitor_card")),
		"sub_cards":              normalizeMonitorValue(metadataValue(result.Metadata, "monitor_sub_cards")),
	}
	if call.Name == "tool_search" && result.Success {
		if structured, ok := metadataValue(result.Metadata, "structured_content").(map[string]any); ok {
			record["matched_tool_names"] = normalizeMonitorValue(structured["matched_tool_names"])
			record["newly_discovered_tool_names"] = normalizeMonitorValue(structured["newly_discovered_tool_names"])
		}
	}
	promptHTMLURI := strings.TrimSpace(monitorMetadataString(result.Metadata, "prompt_html_uri"))
	if promptHTMLURI == "" {
		if detail, ok := record["detail"].(map[string]any); ok {
			promptHTMLURI = monitorMetadataString(detail, "prompt_html_uri")
		}
	}
	if promptHTMLURI != "" {
		record["prompt_html_uri"] = promptHTMLURI
	}
	if images := buildToolMonitorImages(result); len(images) > 0 {
		record["images"] = images
	}
	return record
}

func buildToolMonitorImages(result ToolExecutionResult) []map[string]any {
	items := make([]map[string]any, 0, len(result.Items))
	for _, item := range result.Items {
		itemType := strings.ToLower(strings.TrimSpace(item.Type))
		if itemType != "image" && itemType != "emoji" && itemType != "image_url" {
			continue
		}
		items = append(items, map[string]any{
			"type":      "image",
			"data":      item.Data,
			"mime_type": item.MIMEType,
		})
	}
	for _, message := range result.AppendPostHistory {
		for _, image := range message.Images {
			items = append(items, map[string]any{"type": "image", "data": image.MediaRef})
		}
	}
	if len(items) == 0 {
		return nil
	}
	return monitor.BuildToolResultImages(items)
}

func toolResultErrorMessage(result ToolExecutionResult) string {
	message := monitorMetadataString(result.Metadata, "error_message")
	if message == "" && !result.Success {
		message = result.Message
	}
	return message
}

func metadataValue(metadata map[string]any, key string) any {
	if metadata == nil {
		return nil
	}
	return metadata[key]
}

func monitorMetadataString(metadata map[string]any, key string) string {
	value, _ := metadataValue(metadata, key).(string)
	return strings.TrimSpace(value)
}

// normalizeMonitorValue 把监控值规范化为可 JSON 序列化的结构
func normalizeMonitorValue(value any) any {
	switch typed := value.(type) {
	case nil, string, bool, int, int64, float64:
		return typed
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized[key] = normalizeMonitorValue(item)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for index, item := range typed {
			normalized[index] = normalizeMonitorValue(item)
		}
		return normalized
	case []string:
		normalized := make([]any, len(typed))
		for index, item := range typed {
			normalized[index] = item
		}
		return normalized
	default:
		return value
	}
}
