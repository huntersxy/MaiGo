package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
)

// convertMaisakaToolResult 把既有工具输出转换为 Python ToolExecutionResult 对应的模型可见结果
// 现有工具仍返回 Go 原生 JSON；这里按 Python maisaka_builtin 的 content/structured_content/metadata 映射
func convertMaisakaToolResult(ctx context.Context, toolName, raw string) (StructuredToolResult, error) {
	output := map[string]any{}
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		if err := sonic.UnmarshalString(trimmed, &output); err != nil {
			// 非 JSON 输出按纯文本 success 处理，保持既有工具可用
			return StructuredToolResult{Success: true, Content: raw}, nil
		}
	}
	switch toolName {
	case "wait":
		return convertWaitResult(output), nil
	case "stop":
		return convertStopResult(output), nil
	case "tool_search":
		return convertToolSearchResult(output), nil
	case "read_tool_result":
		return convertReadToolResult(output), nil
	case "view_forward_message":
		return convertViewForwardResult(output), nil
	case "query_image_memory":
		return convertQueryImageMemoryResult(output), nil
	case "query_person_profile":
		return convertQueryPersonProfileResult(output), nil
	case "send_emoji":
		return convertSendEmojiResult(output), nil
	case "show_emoji_list":
		return convertShowEmojiListResult(output), nil
	case "subagent", "subagent_status", "subagent_stop":
		return convertSubagentResult(toolName, output), nil
	case "reply":
		return convertReplyResult(ctx, output), nil
	default:
		return StructuredToolResult{Success: true, Content: raw, StructuredContent: output}, nil
	}
}

func convertWaitResult(output map[string]any) StructuredToolResult {
	// waitFunc 在计数等待入口缺失或报错时返回 success=false，此时不得伪装成暂停成功
	if success, ok := output["success"].(bool); ok && !success {
		return StructuredToolResult{Success: false, ErrorMessage: maisakaFirstNonEmpty(maisakaStringValue(output["message"]), "wait 执行失败。")}
	}
	count := maisakaIntValue(output["consecutive_wait_count"])
	content := maisakaStringValue(output["content"])
	if content == "" {
		content = maisakaStringValue(output["message"])
	}
	return StructuredToolResult{
		Success: true,
		Content: content,
		Metadata: map[string]any{
			"pause_execution":        true,
			"consecutive_wait_count": count,
		},
		PauseTool: "wait",
	}
}

func convertStopResult(output map[string]any) StructuredToolResult {
	if success, ok := output["success"].(bool); ok && !success {
		return StructuredToolResult{Success: false, ErrorMessage: maisakaFirstNonEmpty(maisakaStringValue(output["message"]), "stop 执行失败。")}
	}
	return StructuredToolResult{
		Success: true,
		Content: "已结束本轮思考，等待新消息。",
		Metadata: map[string]any{
			"pause_execution": true,
		},
		PauseTool: "stop",
	}
}

func convertToolSearchResult(output map[string]any) StructuredToolResult {
	if success, ok := output["success"].(bool); ok && !success {
		message := maisakaStringValue(output["message"])
		if message == "tool_search 需要提供非空的 query 字符串参数" {
			message = "tool_search 需要提供非空的 `query` 字符串参数。"
		}
		return StructuredToolResult{Success: false, ErrorMessage: message}
	}
	content := maisakaStringValue(output["message"])
	query := maisakaStringValue(output["query"])
	matched := maisakaStringSlice(output["matched_tool_names"])
	newly := maisakaStringSlice(output["newly_discovered_tool_names"])
	structured := map[string]any{
		"query":                       query,
		"matched_tool_names":          matched,
		"newly_discovered_tool_names": newly,
	}
	return StructuredToolResult{
		Success:           true,
		Content:           content,
		StructuredContent: structured,
		Metadata: map[string]any{
			"monitor_detail": map[string]any{
				"query":                       query,
				"matched_tool_names":          matched,
				"newly_discovered_tool_names": newly,
				"content":                     content,
			},
			"matched_tool_names":          matched,
			"newly_discovered_tool_names": newly,
		},
	}
}

func convertReadToolResult(output map[string]any) StructuredToolResult {
	if success, ok := output["success"].(bool); ok && !success {
		return StructuredToolResult{Success: false, ErrorMessage: maisakaFirstNonEmpty(maisakaStringValue(output["message"]), "read_tool_result 执行失败。")}
	}
	return StructuredToolResult{Success: true, Content: maisakaStringValue(output["content"])}
}

func convertViewForwardResult(output map[string]any) StructuredToolResult {
	if success, ok := output["success"].(bool); ok && !success {
		return StructuredToolResult{Success: false, ErrorMessage: maisakaFirstNonEmpty(maisakaStringValue(output["message"]), "查看转发消息失败。")}
	}
	content := maisakaStringValue(output["content"])
	msgID := maisakaStringValue(output["id"])
	path := maisakaIntSlice(output["path"])
	structured := map[string]any{
		"msg_id":       msgID,
		"message_type": "forward",
		"path":         path,
		"full_content": content,
	}
	return StructuredToolResult{Success: true, Content: content, StructuredContent: structured}
}

func convertQueryImageMemoryResult(output map[string]any) StructuredToolResult {
	if success, ok := output["success"].(bool); ok && !success {
		return StructuredToolResult{
			Success:           false,
			ErrorMessage:      maisakaFirstNonEmpty(maisakaStringValue(output["message"]), "没有找到指定图片。"),
			StructuredContent: output,
		}
	}
	return StructuredToolResult{
		Success:           true,
		Content:           maisakaFirstNonEmpty(maisakaStringValue(output["message"]), "没有找到同图或达到阈值的相似图片记录。"),
		StructuredContent: output,
	}
}

func convertQueryPersonProfileResult(output map[string]any) StructuredToolResult {
	success, _ := output["success"].(bool)
	message := strings.TrimSpace(maisakaStringValue(output["message"]))
	if !success {
		if message == "" {
			message = "未找到人物画像。"
		}
		return StructuredToolResult{
			Success:           false,
			ErrorMessage:      message,
			StructuredContent: queryPersonProfileStructured(output),
		}
	}
	summary := strings.TrimSpace(maisakaStringValue(output["summary"]))
	if summary == "" {
		summary = "未找到可用的人物画像。"
	}
	return StructuredToolResult{
		Success:           true,
		Content:           summary,
		StructuredContent: queryPersonProfileStructured(output),
	}
}

func queryPersonProfileStructured(output map[string]any) map[string]any {
	return map[string]any{
		"success":             maisakaBoolValue(output["success"]),
		"summary":             maisakaStringValue(output["summary"]),
		"traits":              maisakaStringSlice(output["traits"]),
		"person_id":           maisakaStringValue(output["person_id"]),
		"person_name":         maisakaStringValue(output["person_name"]),
		"profile_source":      maisakaStringValue(output["profile_source"]),
		"has_manual_override": maisakaBoolValue(output["has_manual_override"]),
		"from_cache":          maisakaBoolValue(output["from_cache"]),
		"limit":               maisakaIntValue(output["limit"]),
	}
}

func convertSendEmojiResult(output map[string]any) StructuredToolResult {
	success := maisakaBoolValue(output["success"])
	message := strings.TrimSpace(maisakaStringValue(output["message"]))
	reason := maisakaStringValue(output["reason"])
	// Python structured_result 初始化即固定六键；成功时后三者保持空值但键必须存在
	structured := map[string]any{
		"success":         success,
		"message":         message,
		"description":     maisakaStringValue(output["description"]),
		"emotion":         maisakaStringSlice(output["emotion"]),
		"matched_emotion": maisakaStringValue(output["matched_emotion"]),
		"reason":          reason,
	}
	// metadata 由选择器与发送结果在工具内组装，对齐 send_emoji.py:_build_send_emoji_monitor_metadata
	metadata := maisakaMapValue(output["selection_metadata"])
	if !success {
		return StructuredToolResult{
			Success:           false,
			ErrorMessage:      maisakaFirstNonEmpty(message, "发送表情包失败。"),
			StructuredContent: structured,
			Metadata:          metadata,
		}
	}
	if message == "" {
		message = "表情包发送成功"
		structured["message"] = message
	}
	return StructuredToolResult{
		Success:           true,
		Content:           maisakaFirstNonEmpty(reason, "表情包发送成功"),
		StructuredContent: structured,
		Metadata:          metadata,
	}
}

func convertShowEmojiListResult(output map[string]any) StructuredToolResult {
	if success, ok := output["success"].(bool); ok && !success {
		return StructuredToolResult{Success: false, ErrorMessage: maisakaFirstNonEmpty(maisakaStringValue(output["message"]), "当前表情包库中没有可用表情。")}
	}
	return StructuredToolResult{Success: true, Content: maisakaStringValue(output["message"])}
}

func convertSubagentResult(toolName string, output map[string]any) StructuredToolResult {
	success, _ := output["success"].(bool)
	message := maisakaStringValue(output["message"])
	if !success {
		return StructuredToolResult{Success: false, ErrorMessage: maisakaFirstNonEmpty(message, "子代理操作失败。")}
	}
	taskID := maisakaStringValue(output["task_id"])
	switch toolName {
	case "subagent":
		structured := map[string]any{
			"task_id":        taskID,
			"granted_tools":  maisakaStringSlice(output["granted_tools"]),
			"rejected_tools": maisakaStringSlice(output["rejected_tools"]),
		}
		if strings.HasPrefix(strings.TrimSpace(message), "[子代理结论") || output["tool_usage"] != nil {
			structured["tool_usage"] = maisakaIntMap(output["tool_usage"])
		}
		return StructuredToolResult{Success: true, Content: message, StructuredContent: structured}
	case "subagent_status":
		if tasks := maisakaMapSlice(output["tasks"]); len(tasks) > 0 {
			return StructuredToolResult{
				Success:           true,
				Content:           message,
				StructuredContent: map[string]any{"tasks": tasks},
			}
		}
		if taskID != "" {
			return StructuredToolResult{
				Success:           true,
				Content:           message,
				StructuredContent: map[string]any{"task_id": taskID, "status": maisakaStringValue(output["status"])},
			}
		}
	}
	return StructuredToolResult{Success: true, Content: message}
}

func convertReplyResult(ctx context.Context, output map[string]any) StructuredToolResult {
	success := maisakaBoolValue(output["success"])
	message := strings.TrimSpace(maisakaStringValue(output["message"]))
	setQuote, effectiveQuote := maisakaReplyQuoteFlags(ctx, output)
	segments := maisakaStringSlice(output["reply_segments"])
	sendResults := maisakaReplySendResults(output["send_results"])
	msgID := maisakaStringValue(output["msg_id"])
	if !success {
		if maisakaBoolValue(output["send_failed"]) {
			// Python 发送失败分支只带 5 键，不含 reply_text 与 target_user_name
			structured := map[string]any{
				"msg_id":              msgID,
				"set_quote":           setQuote,
				"effective_set_quote": effectiveQuote,
				"reply_segments":      segments,
				"send_results":        sendResults,
			}
			return StructuredToolResult{
				Success:           false,
				ErrorMessage:      maisakaFirstNonEmpty(message, "发送可见回复失败。"),
				StructuredContent: structured,
				Metadata:          maisakaReplyMetadata(output),
			}
		}
		// 对齐 Python reply.py:552-560：生成失败分支同样带 metadata
		return StructuredToolResult{
			Success:      false,
			ErrorMessage: maisakaFirstNonEmpty(message, "发送可见回复失败。"),
			Metadata:     maisakaReplyMetadata(output),
		}
	}
	replyText := maisakaStringValue(output["content"])
	if len(segments) > 0 {
		replyText = strings.Join(segments, "")
	}
	botName := "MaiSaka"
	if tc := GetToolContext(ctx); tc != nil {
		if name := strings.TrimSpace(tc.BotName); name != "" {
			botName = name
		}
	}
	targetUserName := maisakaStringValue(output["target_user_name"])
	receipt := "\"" + botName + "\"已生成并向当前会话发送了回复\"" + replyText + "\""
	if targetUserName != "" {
		receipt = "\"" + botName + "\"已生成并向\"" + targetUserName + "\"发送了回复\"" + replyText + "\""
	}
	structured := map[string]any{
		"msg_id":              msgID,
		"set_quote":           setQuote,
		"effective_set_quote": effectiveQuote,
		"reply_text":          replyText,
		"reply_segments":      segments,
		"send_results":        sendResults,
		"target_user_name":    targetUserName,
	}
	return StructuredToolResult{
		Success:           true,
		Content:           receipt,
		StructuredContent: structured,
		Metadata:          maisakaReplyMetadata(output),
	}
}

// maisakaReplyMetadata 构造 reply 工具的 metadata 通道
// 对齐 Python src/maisaka/builtin_tool/reply.py:240-255 的 _build_monitor_metadata
// 与 :655-670 的 sent_message_ids/send_results/quote_probability/reply_supplement/reply_extensions
func maisakaReplyMetadata(output map[string]any) map[string]any {
	metadata := map[string]any{}
	if replyID := maisakaStringValue(output["reply_id"]); replyID != "" {
		metadata["reply_id"] = replyID
	}
	if supplement, ok := output["reply_supplement"].(map[string]any); ok && len(supplement) > 0 {
		metadata["reply_supplement"] = supplement
	}
	if detail, ok := output["monitor_detail"].(map[string]any); ok && len(detail) > 0 {
		metadata["monitor_detail"] = detail
	}
	if extensions, ok := output["reply_extensions"].(map[string]any); ok && len(extensions) > 0 {
		metadata["reply_extensions"] = extensions
	}
	if probability, ok := output["quote_probability"]; ok && probability != nil {
		metadata["quote_probability"] = probability
	}
	// sent_message_ids 与 send_results 恒存在，对齐 Python 先赋值再逐段追加
	sentIDs := maisakaStringSlice(output["sent_message_ids"])
	if sentIDs == nil {
		sentIDs = []string{}
	}
	metadata["sent_message_ids"] = sentIDs
	metadata["send_results"] = maisakaReplySendResults(output["send_results"])
	return metadata
}

// maisakaReplyQuoteFlags 读取 set_quote 与 effective_set_quote；旧输出缺少分字段时按引用目标回退
func maisakaReplyQuoteFlags(ctx context.Context, output map[string]any) (bool, bool) {
	if _, ok := output["set_quote"]; ok {
		return maisakaBoolValue(output["set_quote"]), maisakaBoolValue(output["effective_set_quote"])
	}
	quote := maisakaIntValue(output["reply_to"]) > 0
	if tc := GetToolContext(ctx); tc != nil && tc.EffectiveReplyTo != nil {
		quote = *tc.EffectiveReplyTo != 0
	}
	return quote, quote
}

// maisakaReplySendResults 归一化逐段发送结果，保证空结果序列化为 [] 而不是 null
func maisakaReplySendResults(value any) []map[string]any {
	raw := maisakaMapSlice(value)
	results := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		results = append(results, map[string]any{
			"index":      maisakaIntValue(item["index"]),
			"segment":    maisakaStringValue(item["segment"]),
			"set_quote":  maisakaBoolValue(item["set_quote"]),
			"success":    maisakaBoolValue(item["success"]),
			"message_id": maisakaStringValue(item["message_id"]),
		})
	}
	return results
}

func maisakaStringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprintf("%v", value)
}

func maisakaFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func maisakaBoolValue(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func maisakaIntValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		return 0
	}
}

// maisakaMapValue 取 map[string]any；非对象返回 nil，避免把空对象写进 metadata
func maisakaMapValue(value any) map[string]any {
	raw, ok := value.(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	return raw
}

func maisakaIntMap(value any) map[string]int {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	result := make(map[string]int, len(raw))
	for key, item := range raw {
		result[key] = maisakaIntValue(item)
	}
	return result
}

func maisakaStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return []string{}
	}
}

func maisakaIntSlice(value any) []int {
	switch typed := value.(type) {
	case []int:
		return typed
	case []any:
		result := make([]int, 0, len(typed))
		for _, item := range typed {
			result = append(result, maisakaIntValue(item))
		}
		return result
	default:
		return []int{}
	}
}

func maisakaMapSlice(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if entry, ok := item.(map[string]any); ok {
				result = append(result, entry)
			}
		}
		return result
	default:
		return nil
	}
}
