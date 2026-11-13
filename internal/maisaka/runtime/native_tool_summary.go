package runtime

// Provider 原生工具摘要提取，对齐 openai_responses_client 的 _extract_web_search_summary

import (
	"fmt"
	"strings"

	"maibot-go/internal/maisaka"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
)

const (
	// nativeToolDetailLimit 单条摘要明细的最大字符数
	nativeToolDetailLimit = 500
	// nativeToolDetailCountLimit 每条原生工具最多保留的明细条数
	nativeToolDetailCountLimit = 5
	// nativeToolItemIDKey agenticopenai 写入的 Responses item 标识
	nativeToolItemIDKey = "openai-item-id"
	// nativeToolItemStatusKey agenticopenai 写入的 Responses item 状态
	nativeToolItemStatusKey = "openai-item-status"
)

// extractNativeToolSummaries 从本次响应内容块提取原生工具摘要，按调用 ID 去重
func extractNativeToolSummaries(blocks []*schema.ContentBlock) []maisaka.NativeToolCallSummary {
	if len(blocks) == 0 {
		return nil
	}
	// 结果块与调用块共享 item 标识，先建索引以便回填来源数量
	sourceCounts := map[string]int{}
	for _, block := range blocks {
		if block == nil || block.Type != schema.ContentBlockTypeServerToolResult || block.ServerToolResult == nil {
			continue
		}
		if block.ServerToolResult.Name != "web_search" {
			continue
		}
		if count := webSearchSourceCount(block.ServerToolResult.Content); count > 0 {
			sourceCounts[nativeItemID(block)] = count
		}
	}
	summaries := make([]maisaka.NativeToolCallSummary, 0, len(blocks))
	seen := map[string]struct{}{}
	for index, block := range blocks {
		if block == nil || block.Type != schema.ContentBlockTypeServerToolCall || block.ServerToolCall == nil {
			continue
		}
		if block.ServerToolCall.Name != "web_search" {
			continue
		}
		callKey := nativeItemID(block)
		if callKey == "" {
			callKey = fmt.Sprintf("web_search_call:%d", index)
		}
		if _, exists := seen[callKey]; exists {
			continue
		}
		seen[callKey] = struct{}{}
		summary := webSearchSummary(block.ServerToolCall.Arguments)
		summary.CallID = nativeItemID(block)
		summary.Status = nativeItemStatus(block)
		summary.SourceCount = sourceCounts[summary.CallID]
		summaries = append(summaries, summary)
	}
	return summaries
}

// webSearchSummary 从 web_search 调用参数提取安全可观测摘要
func webSearchSummary(arguments any) maisaka.NativeToolCallSummary {
	summary := maisaka.NativeToolCallSummary{ToolType: "web_search"}
	payload := serverToolArguments(arguments)
	if payload == nil {
		return summary
	}
	search, _ := payload["web_search"].(map[string]any)
	if search == nil {
		return summary
	}
	actionType, _ := search["action_type"].(string)
	summary.ActionType = strings.TrimSpace(actionType)
	details := make([]string, 0, nativeToolDetailCountLimit)
	switch summary.ActionType {
	case "search":
		queries := nativeStringList(search["search"])
		for _, query := range queries {
			if normalized := normalizeNativeToolDetail(query); normalized != "" {
				details = append(details, "查询："+normalized)
			}
		}
	case "open_page":
		openPage, _ := search["open_page"].(map[string]any)
		if url := normalizeNativeToolDetail(stringValue(openPage, "url")); url != "" {
			details = append(details, "页面："+url)
		}
	case "find":
		find, _ := search["find"].(map[string]any)
		if pattern := normalizeNativeToolDetail(stringValue(find, "pattern")); pattern != "" {
			details = append(details, "页内查找："+pattern)
		}
		if url := normalizeNativeToolDetail(stringValue(find, "url")); url != "" {
			details = append(details, "页面："+url)
		}
	}
	if len(details) > nativeToolDetailCountLimit {
		details = details[:nativeToolDetailCountLimit]
	}
	summary.Details = details
	return summary
}

// toolCallFromReasoning 读取工具调用块的来源标记，对齐 Python TOOL_CALL_SOURCE_EXTRA_KEY
func toolCallFromReasoning(block *schema.ContentBlock) bool {
	source, _ := toolCallSourceOf(block)
	return source == "reasoning"
}

// cloneBlockExtra 复制内容块附加元数据，供 ToolCall.ExtraContent 使用
func cloneBlockExtra(block *schema.ContentBlock) map[string]any {
	if block == nil || len(block.Extra) == 0 {
		return nil
	}
	out := make(map[string]any, len(block.Extra))
	for key, value := range block.Extra {
		out[key] = value
	}
	return out
}

func toolCallSourceOf(block *schema.ContentBlock) (string, bool) {
	if block == nil || len(block.Extra) == 0 {
		return "", false
	}
	source, _ := block.Extra["tool_call_source"].(string)
	return strings.ToLower(strings.TrimSpace(source)), true
}

// nativeStringList 读取 search 区块下的 queries，缺失时回退单条 query
func nativeStringList(search any) []string {
	block, _ := search.(map[string]any)
	if block == nil {
		return nil
	}
	if raw, ok := block["queries"].([]any); ok && len(raw) > 0 {
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	}
	if text, ok := block["query"].(string); ok {
		return []string{text}
	}
	return nil
}

// webSearchSourceCount 统计 web_search 结果中的来源数量
func webSearchSourceCount(content any) int {
	payload, ok := content.(map[string]any)
	if !ok {
		if encoded, err := sonic.MarshalString(content); err == nil {
			if err := sonic.UnmarshalString(encoded, &payload); err != nil {
				return 0
			}
		}
	}
	if payload == nil {
		return 0
	}
	webSearch, _ := payload["web_search"].(map[string]any)
	if webSearch == nil {
		return 0
	}
	search, _ := webSearch["search"].(map[string]any)
	if search == nil {
		return 0
	}
	sources, _ := search["sources"].([]any)
	return len(sources)
}

// serverToolArguments 把 eino ServerToolCall.Arguments 展开成通用 map
func serverToolArguments(arguments any) map[string]any {
	if arguments == nil {
		return nil
	}
	if mapped, ok := arguments.(map[string]any); ok {
		return mapped
	}
	encoded, err := sonic.MarshalString(arguments)
	if err != nil {
		return nil
	}
	var payload map[string]any
	if err := sonic.UnmarshalString(encoded, &payload); err != nil {
		return nil
	}
	return payload
}

// nativeItemID 读取 Responses item 标识
func nativeItemID(block *schema.ContentBlock) string {
	if block == nil || block.Extra == nil {
		return ""
	}
	value, _ := block.Extra[nativeToolItemIDKey].(string)
	return strings.TrimSpace(value)
}

// nativeItemStatus 读取 Responses item 状态
func nativeItemStatus(block *schema.ContentBlock) string {
	if block == nil || block.Extra == nil {
		return ""
	}
	switch typed := block.Extra[nativeToolItemStatusKey].(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

// normalizeNativeToolDetail 压缩为单行并截断到上限
func normalizeNativeToolDetail(value string) string {
	detail := strings.Join(strings.Fields(value), " ")
	if len([]rune(detail)) <= nativeToolDetailLimit {
		return detail
	}
	runes := []rune(detail)
	return string(runes[:nativeToolDetailLimit-3]) + "..."
}

func stringValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	text, _ := values[key].(string)
	return text
}
