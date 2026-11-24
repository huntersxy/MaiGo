package maisaka

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"MaiGo/internal/common/llmjson"
)

// ContextSummarySource 上下文压缩摘要消息的 source 标记
const ContextSummarySource = "context_compaction"

// ContextSummaryPrefix 内部状态摘要的展示前缀
const ContextSummaryPrefix = "【上下文状态摘要-内部参考】"

// ConversationStateLabels 摘要字段的中文标签，顺序固定
var ConversationStateLabels = []struct {
	Key   string
	Label string
}{
	{"topics", "当前话题"},
	{"background", "关键背景"},
	{"progress", "进展与结论"},
	{"pending", "未完事项"},
	{"agreements", "对话约定"},
}

// ConversationState 可直接接续对话的状态
type ConversationState struct {
	Topics     []string `json:"topics"`
	Background []string `json:"background"`
	Progress   []string `json:"progress"`
	Pending    []string `json:"pending"`
	Agreements []string `json:"agreements"`
}

// Clean 去空去重并保序
func (s *ConversationState) Clean() {
	s.Topics = cleanEntries(s.Topics)
	s.Background = cleanEntries(s.Background)
	s.Progress = cleanEntries(s.Progress)
	s.Pending = cleanEntries(s.Pending)
	s.Agreements = cleanEntries(s.Agreements)
}

func cleanEntries(entries []string) []string {
	seen := make(map[string]struct{}, len(entries))
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		text := strings.TrimSpace(entry)
		if text == "" {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	return out
}

// Render 渲染为可接续的状态文本
func (s *ConversationState) Render() string {
	s.Clean()
	values := map[string][]string{
		"topics":     s.Topics,
		"background": s.Background,
		"progress":   s.Progress,
		"pending":    s.Pending,
		"agreements": s.Agreements,
	}
	blocks := make([]string, 0, len(ConversationStateLabels))
	for _, label := range ConversationStateLabels {
		entries := values[label.Key]
		if len(entries) == 0 {
			continue
		}
		lines := make([]string, 0, len(entries)+1)
		lines = append(lines, label.Label+":")
		for _, entry := range entries {
			lines = append(lines, "- "+entry)
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	return strings.Join(blocks, "\n\n")
}

// ParseConversationState 校验摘要 JSON 中已知分类的类型
func ParseConversationState(raw string) (*ConversationState, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(llmjson.Normalize(raw)), &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, fmt.Errorf("状态摘要必须是 JSON 对象")
	}
	state := ConversationState{}
	for _, category := range []struct {
		key     string
		entries *[]string
	}{
		{"topics", &state.Topics}, {"background", &state.Background}, {"progress", &state.Progress},
		{"pending", &state.Pending}, {"agreements", &state.Agreements},
	} {
		rawEntries, ok := fields[category.key]
		if !ok {
			continue
		}
		var entries []*string
		if err := json.Unmarshal(rawEntries, &entries); err != nil {
			return nil, fmt.Errorf("状态摘要字段 %s 必须是字符串数组: %w", category.key, err)
		}
		if entries == nil {
			return nil, fmt.Errorf("状态摘要字段 %s 不能为 null", category.key)
		}
		for _, entry := range entries {
			if entry == nil {
				return nil, fmt.Errorf("状态摘要字段 %s 不接受 null 元素", category.key)
			}
			*category.entries = append(*category.entries, *entry)
		}
	}
	state.Clean()
	return &state, nil
}

// compactionMessageKey 用稳定字段标识一条历史消息，用于和 Planner 快照覆盖范围去重
func compactionMessageKey(message ContextMessage) string {
	parts := []string{
		string(message.Role), message.Source, message.MessageID, message.SessionID,
		message.ToolCallID, message.ToolName, message.Content,
		message.Timestamp.UTC().Format(time.RFC3339Nano),
	}
	for _, call := range message.ToolCalls {
		parts = append(parts, call.CallID, call.Name)
	}
	return strings.Join(parts, "\x00")
}

// BuildCompactionData 收集未被 Planner 快照覆盖的新增资料，已有摘要按普通消息处理
func BuildCompactionData(removed []ContextMessage, recent []ContextMessage, previous *ContextMessage, covered []ContextMessage) []string {
	seen := make(map[string]struct{}, len(covered)+len(removed)+len(recent)+1)
	for _, message := range covered {
		seen[compactionMessageKey(message)] = struct{}{}
	}
	var data []string
	appendMessage := func(message ContextMessage) {
		key := compactionMessageKey(message)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		if text := MessageTextForCompaction(message); text != "" {
			data = append(data, text)
		}
	}
	if previous != nil {
		appendMessage(*previous)
	}
	for _, message := range removed {
		appendMessage(message)
	}
	for _, message := range recent {
		appendMessage(message)
	}
	return data
}

// BuildCompactionTail 拼接额外对话资料、来源说明和压缩指令
func BuildCompactionTail(data []string, sourceContext, instruction string) string {
	parts := make([]string, 0, len(data)+6)
	parts = append(parts, "<additional_conversation_data>")
	parts = append(parts, data...)
	parts = append(parts, "</additional_conversation_data>")
	if sourceContext != "" {
		parts = append(parts, "<conversation_sources>", sourceContext, "</conversation_sources>")
	}
	parts = append(parts, instruction)
	return strings.Join(parts, "\n")
}

// MessageTextForCompaction 将消息编码为压缩资料，不重放工具调用或模型内部推理
func MessageTextForCompaction(message ContextMessage) string {
	if message.Role == RoleAssistant || len(message.ToolCalls) > 0 {
		if message.Reasoning != "" && message.Content == "" && len(message.ToolCalls) == 0 {
			return ""
		}
		if len(message.ToolCalls) > 0 {
			lines := make([]string, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				payload := map[string]any{
					"kind":    "tool_call",
					"call_id": call.CallID,
					"tool":    call.Name,
					"args":    call.Arguments,
				}
				encoded, err := json.Marshal(payload)
				if err != nil {
					continue
				}
				lines = append(lines, string(encoded))
			}
			return strings.Join(lines, "\n")
		}
	}
	if message.Role == RoleTool {
		success, _ := message.Metadata["success"].(bool)
		payload := map[string]any{
			"kind":    "tool_result",
			"call_id": message.ToolCallID,
			"tool":    message.ToolName,
			"success": success,
			"content": message.Content,
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
	timeText := ""
	if !message.Timestamp.IsZero() {
		timeText = message.Timestamp.Format("2006-01-02T15:04:05.999999")
	}
	payload := map[string]any{
		"kind":    message.Source,
		"role":    string(message.Role),
		"time":    timeText,
		"content": message.Content,
	}
	if message.MessageID != "" {
		payload["message_id"] = message.MessageID
	}
	if message.SessionID != "" {
		payload["session_id"] = message.SessionID
	}
	if speaker, ok := message.Metadata["speaker"].(string); ok && speaker != "" {
		payload["speaker"] = speaker
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// BuildSummaryMessage 构造常驻上下文摘要消息
func BuildSummaryMessage(content string, timestamp time.Time, prefix string) ContextMessage {
	if prefix == "" {
		prefix = ContextSummaryPrefix
	}
	return ContextMessage{
		Role:           RoleReference,
		Content:        content,
		Source:         ContextSummarySource,
		Timestamp:      timestamp,
		CountInContext: false,
		Metadata: map[string]any{
			"display_prefix": prefix,
			"focus_kind":     "summary",
			"remaining_uses": nil,
		},
	}
}
