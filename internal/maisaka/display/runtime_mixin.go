package display

import (
	"fmt"
	"sort"
	"strings"
)

// BuildCycleTimeRecordsText 构建循环最外层面板展示的阶段耗时文本
func BuildCycleTimeRecordsText(timeRecords map[string]float64) string {
	if len(timeRecords) == 0 {
		return "流程耗时：无"
	}
	labels := map[string]string{"planner": "Planner", "tool_calls": "工具执行"}
	parts := make([]string, 0, len(timeRecords))
	for _, key := range []string{"planner", "tool_calls"} {
		if duration, ok := timeRecords[key]; ok {
			parts = append(parts, fmt.Sprintf("%s %.2f s", labels[key], duration))
		}
	}
	extraKeys := make([]string, 0)
	for key := range timeRecords {
		if key != "planner" && key != "tool_calls" {
			extraKeys = append(extraKeys, key)
		}
	}
	sort.Strings(extraKeys)
	for _, key := range extraKeys {
		label := labels[key]
		if label == "" {
			label = key
		}
		parts = append(parts, fmt.Sprintf("%s %.2f s", label, timeRecords[key]))
	}
	if len(parts) == 0 {
		return "流程耗时：无"
	}
	return "流程耗时：" + strings.Join(parts, " | ")
}

// FilterRedundantToolResults 过滤掉已经在详情卡片中展示过的工具摘要
func FilterRedundantToolResults(toolResults []string, toolDetailResults []map[string]any) []string {
	detailed := map[string]struct{}{}
	for _, detail := range toolDetailResults {
		inner, ok := detail["detail"].(map[string]any)
		if !ok || len(inner) == 0 {
			continue
		}
		if summary, ok := detail["summary"].(string); ok && strings.TrimSpace(summary) != "" {
			detailed[strings.TrimSpace(summary)] = struct{}{}
		}
	}
	filtered := make([]string, 0, len(toolResults))
	for _, result := range toolResults {
		trimmed := strings.TrimSpace(result)
		if trimmed == "" {
			continue
		}
		if _, exists := detailed[trimmed]; exists {
			continue
		}
		filtered = append(filtered, trimmed)
	}
	return filtered
}

// BuildToolMetricsText 将工具监控 metrics 转换为便于 CLI 阅读的文本
func BuildToolMetricsText(metrics map[string]any) string {
	lines := make([]string, 0, 3)
	if modelName, ok := metrics["model_name"].(string); ok && strings.TrimSpace(modelName) != "" {
		lines = append(lines, "模型："+strings.TrimSpace(modelName))
	}
	promptTokens, promptOK := toInt(metrics["prompt_tokens"])
	completionTokens, completionOK := toInt(metrics["completion_tokens"])
	totalTokens, totalOK := toInt(metrics["total_tokens"])
	if promptOK || completionOK || totalOK {
		lines = append(lines, fmt.Sprintf("Token：输入 %s / 输出 %s / 总计 %s",
			FormatTokenCount(promptTokens), FormatTokenCount(completionTokens), FormatTokenCount(totalTokens)))
	}
	timingParts := make([]string, 0, 3)
	for _, key := range []string{"prompt_ms", "llm_ms", "overall_ms"} {
		if value, ok := toFloat(metrics[key]); ok {
			timingParts = append(timingParts, fmt.Sprintf("%s %.2f ms", strings.TrimSuffix(key, "_ms"), value))
		}
	}
	if len(timingParts) > 0 {
		lines = append(lines, "耗时："+strings.Join(timingParts, " / "))
	}
	return strings.Join(lines, "\n")
}

// ToolDetailLabels 不同工具对应的详情区标题与预览类别
type ToolDetailLabels struct {
	PromptTitle    string
	ReasoningTitle string
	OutputTitle    string
	PromptCategory string
	RequestKind    string
}

// GetToolDetailLabels 返回不同工具对应的详情区标题与预览类别
func GetToolDetailLabels(toolName string) ToolDetailLabels {
	normalized := strings.ToLower(strings.TrimSpace(toolName))
	switch normalized {
	case "reply":
		return ToolDetailLabels{"Reply Prompt", "Reply 思考", "Reply 输出", "replyer", "replyer"}
	case "send_emoji":
		return ToolDetailLabels{"Emotion Prompt", "Emotion 思考", "Emotion 输出", "emotion", "emotion"}
	}
	displayName := normalized
	if displayName == "" {
		displayName = "tool"
	}
	return ToolDetailLabels{
		PromptTitle:    displayName + " Prompt",
		ReasoningTitle: displayName + " 思考",
		OutputTitle:    displayName + " 输出",
		PromptCategory: displayName,
		RequestKind:    "sub_agent",
	}
}

// BuildPromptPreviewMetadataFromToolMetrics 从工具监控 metrics 中提取可写入 Prompt 预览的模型、耗时与 Token
func BuildPromptPreviewMetadataFromToolMetrics(metrics map[string]any) map[string]any {
	metadata := map[string]any{}
	if len(metrics) == 0 {
		return metadata
	}
	if modelName, ok := metrics["model_name"].(string); ok && strings.TrimSpace(modelName) != "" {
		metadata["model_name"] = strings.TrimSpace(modelName)
	}
	for _, key := range []string{"llm_ms", "overall_ms"} {
		if value, ok := toFloat(metrics[key]); ok {
			metadata["duration_ms"] = value
			break
		}
	}
	for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
		if value, ok := toInt(metrics[key]); ok {
			metadata[key] = value
		}
	}
	return metadata
}

// NormalizeToolCardBodyLines 将工具卡片正文规范化为行列表
func NormalizeToolCardBodyLines(body string) []string {
	lines := make([]string, 0)
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
