package display

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	maisaka "maibot-go/internal/maisaka"
)

// 工具调用来源常量
const (
	ToolCallSourceExtraKey      = "tool_call_source"
	ToolCallSourceReasoning     = "reasoning"
	ToolCallSourceResponse      = "response"
	ToolCallSourceProvider      = "provider"
	ToolCallSourceProviderLabel = "Provider 原生调用"
)

var requestPanelStyleMap = map[string][2]string{
	"planner":                {"MaiSaka 大模型请求 - 对话单步", "green"},
	"replyer":                {"MaiSaka 回复器 Prompt", "bright_yellow"},
	"emotion":                {"MaiSaka Emotion Tool Prompt", "bright_cyan"},
	"expression_selector":    {"MaiSaka 表达选择子代理 Prompt", "bright_yellow"},
	"expression_learner":     {"MaiSaka 表达学习上下文", "bright_green"},
	"jargon_learner":         {"MaiSaka 黑话学习上下文", "bright_cyan"},
	"jargon_learning_update": {"MaiSaka 黑话含义推断 Prompt", "bright_cyan"},
	"context_compaction":     {"MaiSaka 上下文压缩 Prompt", "bright_magenta"},
	"sub_agent":              {"MaiSaka 大模型请求 - 子代理", "bright_blue"},
}

var defaultRequestPanelStyle = [2]string{"MaiSaka 大模型请求 - 对话单步", "cyan"}

// FormatTokenCount 格式化 token 数量展示文本
func FormatTokenCount(tokenCount int) string {
	if tokenCount >= 10000 {
		return fmt.Sprintf("%.1fk", float64(tokenCount)/1000)
	}
	return strconv.Itoa(tokenCount)
}

// GetRequestPanelStyle 返回不同请求类型对应的标题与边框颜色
func GetRequestPanelStyle(requestKind string) (string, string) {
	normalized := strings.ToLower(strings.TrimSpace(requestKind))
	if normalized == "" {
		normalized = "planner"
	}
	if style, ok := requestPanelStyleMap[normalized]; ok {
		return style[0], style[1]
	}
	return defaultRequestPanelStyle[0], defaultRequestPanelStyle[1]
}

// ToolCallDisplay 统一展示结构
type ToolCallDisplay struct {
	ID           string
	Name         string
	Arguments    any
	Source       string
	SourceLabel  string
	ExtraContent map[string]any
}

// FormatToolCallForDisplay 将不同来源的工具调用对象规范化为统一展示结构
//
// 对齐 Python format_tool_call_for_display：来源优先取 extra_content 里的
// tool_call_source，其次才是 reasoning 推断；extra_content 非空时一并带回展示。
func FormatToolCallForDisplay(toolCall maisaka.ToolCall) ToolCallDisplay {
	source := NormalizeToolCallSource(extraContentSource(toolCall.ExtraContent))
	if source == "" && toolCall.FromReasoning {
		source = ToolCallSourceReasoning
	}
	display := ToolCallDisplay{
		ID:           toolCall.CallID,
		Name:         toolCall.Name,
		Arguments:    toolCall.Arguments,
		Source:       source,
		ExtraContent: cloneToolCallExtraContent(toolCall.ExtraContent),
	}
	if source != "" {
		display.SourceLabel = FormatToolCallSourceLabel(source)
	}
	return display
}

// extraContentSource 读取 extra_content 中的工具调用来源标记
func extraContentSource(extraContent map[string]any) string {
	if len(extraContent) == 0 {
		return ""
	}
	source, _ := extraContent[ToolCallSourceExtraKey].(string)
	return source
}

func cloneToolCallExtraContent(extraContent map[string]any) map[string]any {
	if len(extraContent) == 0 {
		return nil
	}
	out := make(map[string]any, len(extraContent))
	for key, value := range extraContent {
		out[key] = value
	}
	return out
}

// NormalizeToolCallSource 归一化工具调用来源
func NormalizeToolCallSource(source string) string {
	normalized := strings.ToLower(strings.TrimSpace(source))
	switch normalized {
	case ToolCallSourceReasoning, "thinking", "reasoning_content":
		return ToolCallSourceReasoning
	case ToolCallSourceResponse, "content", "output", "text":
		return ToolCallSourceResponse
	case ToolCallSourceProvider, "native", "provider_native":
		return ToolCallSourceProvider
	}
	return ""
}

// FormatToolCallSourceLabel 返回工具调用来源的中文展示标签
func FormatToolCallSourceLabel(source string) string {
	switch NormalizeToolCallSource(source) {
	case ToolCallSourceReasoning:
		return "推理中调用"
	case ToolCallSourceResponse:
		return "正文调用"
	case ToolCallSourceProvider:
		return ToolCallSourceProviderLabel
	}
	return "未知来源"
}

// FormatNativeToolCallForDisplay 将 Provider 原生工具摘要投影为工具调用展示结构
func FormatNativeToolCallForDisplay(summary maisaka.NativeToolCallSummary) ToolCallDisplay {
	arguments := map[string]any{}
	if summary.ActionType != "" {
		arguments["action_type"] = summary.ActionType
	}
	if summary.Status != "" {
		arguments["status"] = summary.Status
	}
	if len(summary.Details) > 0 {
		arguments["details"] = append([]string(nil), summary.Details...)
	}
	if summary.SourceCount > 0 {
		arguments["source_count"] = summary.SourceCount
	}
	return ToolCallDisplay{
		ID:          summary.CallID,
		Name:        summary.ToolType,
		Arguments:   arguments,
		Source:      ToolCallSourceProvider,
		SourceLabel: ToolCallSourceProviderLabel,
	}
}

// BuildToolCallSummaryLines 构建工具调用摘要文本，附来源标签与参数
func BuildToolCallSummaryLines(toolCalls []maisaka.ToolCall) []string {
	lines := make([]string, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		display := FormatToolCallForDisplay(toolCall)
		name := strings.TrimSpace(display.Name)
		if name == "" {
			name = "unknown"
		}
		line := "- " + name
		if label := strings.TrimSpace(display.SourceLabel); label != "" {
			line += " [" + label + "]"
		}
		if len(toolCall.Arguments) > 0 {
			line += ": " + formatToolCallArguments(toolCall.Arguments)
		}
		lines = append(lines, line)
	}
	return lines
}

// formatToolCallArguments 以 Python dict 展示格式输出参数
func formatToolCallArguments(arguments map[string]any) string {
	if len(arguments) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(arguments))
	for key := range arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, "'"+key+"': "+formatToolCallArgumentValue(arguments[key]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func formatToolCallArgumentValue(value any) string {
	switch typed := value.(type) {
	case string:
		return "'" + typed + "'"
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case nil:
		return "None"
	default:
		return fmt.Sprint(typed)
	}
}
