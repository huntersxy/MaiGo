package monitor

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 麦麦观察事件名
const (
	EventSessionStart     = "session.start"
	EventStageStatus      = "stage.status"
	EventStageSnapshot    = "stage.snapshot"
	EventStageRemoved     = "stage.removed"
	EventFlowStep         = "flow.step"
	EventMessageIngested  = "message.ingested"
	EventMessageSent      = "message.sent"
	EventMessageUpdated   = "message.updated"
	EventPlannerProgress  = "planner.progress"
	EventPlannerFinalized = "planner.finalized"
	EventLLMRetry         = "llm.retry"
	EventLLMError         = "llm.error"
	EventSubagentStarted  = "subagent.started"
	EventSubagentRound    = "subagent.round"
	EventSubagentTool     = "subagent.tool_result"
	EventSubagentFinished = "subagent.finished"
)

// IsNonPersistedEvent 判断事件是否只广播不落账
func IsNonPersistedEvent(event string) bool {
	_, ok := nonPersistedEvents[event]
	return ok
}

// 子代理事件单段文字与工具摘要上限，避免观察事件体积过大
const (
	SubagentEventTextMaxChars        = 2000
	SubagentEventToolSummaryMaxChars = 600
)

// NowSeconds 当前 Unix 秒，浮点精度
func NowSeconds() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

// NormalizePayloadValue 将事件载荷中的任意值规范化为可序列化结构
func NormalizePayloadValue(value any) any {
	switch typed := value.(type) {
	case nil, bool, string, float64, int, int64:
		return typed
	case float32:
		return float64(typed)
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized[key] = NormalizePayloadValue(item)
		}
		return normalized
	case []any:
		normalized := make([]any, 0, len(typed))
		for _, item := range typed {
			normalized = append(normalized, NormalizePayloadValue(item))
		}
		return normalized
	default:
		if raw, err := json.Marshal(typed); err == nil {
			var decoded any
			if err := json.Unmarshal(raw, &decoded); err == nil {
				return decoded
			}
		}
		return fmt.Sprint(typed)
	}
}

// ExtractTextContent 从消息内容中提取纯文本表示
func ExtractTextContent(content any) string {
	switch typed := content.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if record, ok := item.(map[string]any); ok {
				if text := strings.TrimSpace(fmt.Sprint(record["text"])); text != "" && text != "<nil>" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "")
	default:
		return fmt.Sprint(typed)
	}
}

// NormalizeToolCallArguments 标准化工具调用参数，兼容 JSON 字符串和对象
func NormalizeToolCallArguments(arguments any) (any, string) {
	if arguments == nil {
		return map[string]any{}, ""
	}
	if text, ok := arguments.(string); ok {
		var parsed any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return map[string]any{}, text
		}
		return parsed, ""
	}
	return NormalizePayloadValue(arguments), ""
}

// SerializeToolCall 将工具调用标准化为前端可直接展示的结构
func SerializeToolCall(toolCall map[string]any) map[string]any {
	return SerializeToolCallObjects([]map[string]any{toolCall})[0]
}

// SerializeToolCallObjects 批量序列化统一工具调用对象
//
// 兼容 MaiBot 的两种输入：OpenAI function 包装与扁平对象；保留 source/source_label
// 与 arguments_raw，供前端区分推理/正文调用。
func SerializeToolCallObjects(toolCalls []map[string]any) []map[string]any {
	serialized := make([]map[string]any, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		name := toolCall["name"]
		arguments := toolCall["arguments"]
		if arguments == nil {
			arguments = toolCall["args"]
		}
		if function, ok := toolCall["function"].(map[string]any); ok {
			if value, exists := function["name"]; exists {
				name = value
			}
			if value, exists := function["arguments"]; exists {
				arguments = value
			}
		}
		normalizedArguments, rawArguments := NormalizeToolCallArguments(arguments)
		identifier := toolCall["id"]
		if identifier == nil {
			identifier = toolCall["call_id"]
		}
		entry := map[string]any{
			"id":        NormalizePayloadValue(identifier),
			"name":      NormalizePayloadValue(name),
			"arguments": normalizedArguments,
		}
		if rawArguments != "" {
			entry["arguments_raw"] = rawArguments
		}
		for _, key := range []string{"source", "source_label", "extra_content"} {
			if value, ok := toolCall[key]; ok && value != nil && value != "" {
				entry[key] = NormalizePayloadValue(value)
			}
		}
		serialized = append(serialized, entry)
	}
	return serialized
}

// SerializeToolCalls 批量序列化工具调用
func SerializeToolCalls(toolCalls []map[string]any) []map[string]any {
	return SerializeToolCallObjects(toolCalls)
}

// SerializeNativeToolCalls 序列化 Provider 原生工具摘要，不读取原始 replay 片段
func SerializeNativeToolCalls(toolCalls []map[string]any) []map[string]any {
	serialized := make([]map[string]any, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		details := make([]any, 0)
		if raw, ok := toolCall["details"].([]any); ok {
			for _, item := range raw {
				details = append(details, fmt.Sprint(NormalizePayloadValue(item)))
			}
		} else if raw, ok := toolCall["details"].([]string); ok {
			for _, item := range raw {
				details = append(details, item)
			}
		}
		serialized = append(serialized, map[string]any{
			"tool_type":    monitorString(toolCall["tool_type"]),
			"call_id":      monitorString(toolCall["call_id"]),
			"status":       monitorString(toolCall["status"]),
			"action_type":  monitorString(toolCall["action_type"]),
			"details":      details,
			"source_count": payloadIntValue(toolCall["source_count"]),
		})
	}
	return serialized
}

// SerializeToolResults 标准化 planner 卡中的工具结果列表
func SerializeToolResults(tools []map[string]any) []map[string]any {
	serialized := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		entry := map[string]any{
			"tool_call_id":           monitorString(tool["tool_call_id"]),
			"tool_name":              monitorString(tool["tool_name"]),
			"tool_args":              NormalizePayloadValue(tool["tool_args"]),
			"tool_call_source":       monitorString(tool["tool_call_source"]),
			"tool_call_source_label": monitorString(tool["tool_call_source_label"]),
			"success":                payloadBoolValue(tool["success"]),
			"error_message":          monitorString(tool["error_message"]),
			"stop_after_execution":   payloadBoolValue(tool["stop_after_execution"]),
			"duration_ms":            payloadFloatValue(tool["duration_ms"]),
			"summary":                monitorString(tool["summary"]),
			"reply_schedule":         NormalizePayloadValue(tool["reply_schedule"]),
			"reply_supplement":       NormalizePayloadValue(tool["reply_supplement"]),
		}
		if sessionID := monitorString(tool["session_id"]); sessionID != "" {
			entry["session_id"] = sessionID
			entry["session_name"] = monitorString(tool["session_name"])
		}
		for _, key := range []string{"started_at", "ended_at"} {
			if _, exists := tool[key]; exists {
				entry[key] = payloadFloatValue(tool[key])
			}
		}
		if images, ok := tool["images"]; ok && images != nil {
			entry["images"] = NormalizePayloadValue(images)
		}
		if detail, exists := tool["detail"]; exists && detail != nil {
			entry["detail"] = NormalizePayloadValue(detail)
		}
		if uri := monitorString(tool["prompt_html_uri"]); uri != "" {
			entry["prompt_html_uri"] = uri
		}
		if monitorString(tool["tool_name"]) == "tool_search" && payloadBoolValue(tool["success"]) {
			entry["matched_tool_names"] = NormalizePayloadValue(tool["matched_tool_names"])
			entry["newly_discovered_tool_names"] = NormalizePayloadValue(tool["newly_discovered_tool_names"])
		}
		serialized = append(serialized, entry)
	}
	return serialized
}

func monitorString(value any) string {
	if value == nil {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Sprint(NormalizePayloadValue(value))
	}
	return text
}

func payloadBoolValue(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func payloadIntValue(value any) int {
	number, _ := payloadInt64Value(value)
	return int(number)
}

func payloadFloatValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int32:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}

// SerializeMessage 将单条消息序列化为可通过 WebSocket 传输的字典
func SerializeMessage(message map[string]any) map[string]any {
	serialized := map[string]any{
		"role":    monitorString(message["role"]),
		"content": ExtractTextContent(message["content"]),
	}
	if toolCallID := monitorString(message["tool_call_id"]); toolCallID != "" {
		serialized["tool_call_id"] = toolCallID
	}
	if toolCalls, ok := message["tool_calls"].([]map[string]any); ok && len(toolCalls) > 0 {
		serialized["tool_calls"] = SerializeToolCalls(toolCalls)
	} else if toolCalls, ok := message["tool_calls"].([]any); ok && len(toolCalls) > 0 {
		entries := make([]map[string]any, 0, len(toolCalls))
		for _, item := range toolCalls {
			if entry, ok := item.(map[string]any); ok {
				entries = append(entries, entry)
			}
		}
		if len(entries) > 0 {
			serialized["tool_calls"] = SerializeToolCalls(entries)
		}
	}
	return serialized
}

// SerializeMessages 批量序列化消息列表
func SerializeMessages(messages []map[string]any) []map[string]any {
	serialized := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		serialized = append(serialized, SerializeMessage(message))
	}
	return serialized
}

// SerializeContextSections 标准化提示词分段用量列表
func SerializeContextSections(sections []map[string]any) []map[string]any {
	serialized := make([]map[string]any, 0, len(sections))
	for _, section := range sections {
		serialized = append(serialized, NormalizePayloadValue(section).(map[string]any))
	}
	return serialized
}

// EmitSessionStart 广播会话开始事件
func EmitSessionStart(sessionID, sessionName string, isGroupChat bool, groupID, userID, platform string) {
	Publish(EventSessionStart, map[string]any{
		"session_id":    sessionID,
		"session_name":  sessionName,
		"is_group_chat": isGroupChat,
		"group_id":      groupID,
		"user_id":       userID,
		"platform":      platform,
		"timestamp":     NowSeconds(),
	})
}

// EmitStageStatus 广播单个聊天流的当前阶段状态
func EmitStageStatus(sessionID, sessionName, stage, detail, roundText, agentState string, waitUntil *float64, stageStartedAt, updatedAt, timestamp float64) {
	Publish(EventStageStatus, map[string]any{
		"session_id":       sessionID,
		"session_name":     sessionName,
		"stage":            stage,
		"detail":           detail,
		"round_text":       roundText,
		"agent_state":      agentState,
		"wait_until":       waitUntil,
		"stage_started_at": stageStartedAt,
		"updated_at":       updatedAt,
		"timestamp":        timestamp,
	})
}

// EmitStageRemoved 广播聊天流阶段状态移除事件
func EmitStageRemoved(sessionID, sessionName string) {
	Publish(EventStageRemoved, map[string]any{
		"session_id":   sessionID,
		"session_name": sessionName,
		"timestamp":    NowSeconds(),
	})
}

// EmitFlowStep 广播可回放的思考流程节点
func EmitFlowStep(sessionID, runID, step, title, detail string, cycleID *int, trim map[string]any) {
	payload := map[string]any{
		"session_id": sessionID,
		"run_id":     runID,
		"step":       step,
		"title":      title,
		"detail":     detail,
		"timestamp":  NowSeconds(),
	}
	if cycleID != nil {
		payload["cycle_id"] = *cycleID
	}
	if trim != nil {
		payload["trim"] = trim
	}
	Publish(EventFlowStep, payload)
}

// EmitLLMRetry 广播模型请求失败后的重试进度
func EmitLLMRetry(sessionID, taskName, requestType, modelName string, attempt, maxAttempts int, reason string, retryInterval float64) {
	Publish(EventLLMRetry, map[string]any{
		"session_id":     sessionID,
		"task_name":      taskName,
		"request_type":   requestType,
		"model_name":     modelName,
		"attempt":        attempt,
		"max_attempts":   maxAttempts,
		"reason":         reason,
		"retry_interval": retryInterval,
		"timestamp":      NowSeconds(),
	})
}

// EmitLLMError 广播模型请求最终失败
func EmitLLMError(sessionID, taskName, requestType, modelName, message string) {
	Publish(EventLLMError, map[string]any{
		"session_id":   sessionID,
		"task_name":    taskName,
		"request_type": requestType,
		"model_name":   modelName,
		"message":      message,
		"timestamp":    NowSeconds(),
	})
}

// EmitMessage 广播消息注入、发送或更新事件
func EmitMessage(event, sessionID, speakerName, content, messageID string, timestamp float64, sourceKind string, extra map[string]any) {
	payload := map[string]any{
		"session_id":   sessionID,
		"speaker_name": speakerName,
		"content":      content,
		"message_id":   messageID,
		"timestamp":    timestamp,
		"source_kind":  sourceKind,
	}
	for key, value := range extra {
		payload[key] = NormalizePayloadValue(value)
	}
	Publish(event, payload)
}

// EmitSubagentStarted 广播子代理任务开始
func EmitSubagentStarted(sessionID, taskID, callID, description string, grantedTools []string, background bool, maxRounds int) {
	Publish(EventSubagentStarted, map[string]any{
		"session_id":    sessionID,
		"task_id":       taskID,
		"call_id":       callID,
		"description":   ClipSubagentEventText(description, SubagentEventTextMaxChars),
		"granted_tools": grantedTools,
		"background":    background,
		"max_rounds":    maxRounds,
		"timestamp":     NowSeconds(),
	})
}

// EmitSubagentRound 广播子代理一轮模型输出
func EmitSubagentRound(sessionID, taskID string, roundIndex int, content string, toolCalls []map[string]any) {
	Publish(EventSubagentRound, map[string]any{
		"session_id":  sessionID,
		"task_id":     taskID,
		"round_index": roundIndex,
		"content":     ClipSubagentEventText(content, SubagentEventTextMaxChars),
		"tool_calls":  SerializeToolCalls(toolCalls),
		"timestamp":   NowSeconds(),
	})
}

// EmitSubagentToolResult 广播子代理内部一次工具调用的结果摘要
func EmitSubagentToolResult(sessionID, taskID string, roundIndex int, callID, toolName string, success bool, summary string, durationMS float64) {
	Publish(EventSubagentTool, map[string]any{
		"session_id":  sessionID,
		"task_id":     taskID,
		"round_index": roundIndex,
		"call_id":     callID,
		"tool_name":   toolName,
		"success":     success,
		"summary":     ClipSubagentEventText(summary, SubagentEventToolSummaryMaxChars),
		"duration_ms": durationMS,
		"timestamp":   NowSeconds(),
	})
}

// EmitSubagentFinished 广播子代理任务结束
func EmitSubagentFinished(sessionID, taskID, status, conclusion, errorMessage string, toolUsage map[string]int, durationMS float64) {
	Publish(EventSubagentFinished, map[string]any{
		"session_id":    sessionID,
		"task_id":       taskID,
		"status":        status,
		"conclusion":    ClipSubagentEventText(conclusion, SubagentEventTextMaxChars),
		"error_message": errorMessage,
		"tool_usage":    toolUsage,
		"duration_ms":   durationMS,
		"timestamp":     NowSeconds(),
	})
}

// ClipSubagentEventText 截断子代理事件里的长文本，并标明被省略的字数
func ClipSubagentEventText(text string, maxChars int) string {
	if maxChars <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	return fmt.Sprintf("%s…（已省略 %d 字）", string(runes[:maxChars]), len(runes)-maxChars)
}
