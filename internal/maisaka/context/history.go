package context

import (
	"sort"
	"strings"

	"maibot-go/internal/llm_models/payload_content"
	maisaka "maibot-go/internal/maisaka"
)

// ToolPairReport 工具配对清理统计
type ToolPairReport struct {
	OrphanToolResults   int
	UnansweredToolCalls int
	MovedToolResults    int
	InvalidToolTurns    int
}

// CollectAvailableToolCallIDs 收集仍保留原始 assistant tool_calls 的工具调用 ID
func CollectAvailableToolCallIDs(history []maisaka.ContextMessage) map[string]struct{} {
	available := map[string]struct{}{}
	for _, message := range history {
		if message.Role != maisaka.RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.CallID != "" {
				available[call.CallID] = struct{}{}
			}
		}
	}
	return available
}

// DropLeadingOrphanToolResults 移除历史前缀中缺少对应 tool_call 的工具结果消息
func DropLeadingOrphanToolResults(history []maisaka.ContextMessage) ([]maisaka.ContextMessage, int) {
	if len(history) == 0 {
		return history, 0
	}
	available := CollectAvailableToolCallIDs(history)
	firstValid := 0
	for firstValid < len(history) {
		message := history[firstValid]
		if !IsToolResultMessage(message) {
			break
		}
		if _, ok := available[message.ToolCallID]; ok {
			break
		}
		firstValid++
	}
	if firstValid == 0 {
		return history, 0
	}
	return history[firstValid:], firstValid
}

// DropOrphanToolResults 移除窗口任意位置中缺少对应 tool_call 的工具结果消息
func DropOrphanToolResults(history []maisaka.ContextMessage) ([]maisaka.ContextMessage, int) {
	if len(history) == 0 {
		return history, 0
	}
	available := CollectAvailableToolCallIDs(history)
	invalidTurns := map[string]struct{}{}
	for _, message := range history {
		if !IsToolResultMessage(message) {
			continue
		}
		if _, ok := available[message.ToolCallID]; !ok && message.LogicalTurnID != "" {
			invalidTurns[message.LogicalTurnID] = struct{}{}
		}
	}
	filtered := make([]maisaka.ContextMessage, 0, len(history))
	removed := 0
	for _, message := range history {
		if _, invalid := invalidTurns[message.LogicalTurnID]; invalid {
			removed++
			continue
		}
		if IsToolResultMessage(message) {
			if _, ok := available[message.ToolCallID]; !ok {
				removed++
				continue
			}
		}
		if isOrphanToolResultMedia(message, available) {
			removed++
			continue
		}
		filtered = append(filtered, message)
	}
	return filtered, removed
}

// IsToolResultMediaMessage 判断是否是工具结果拆分出的媒体消息
//
// 对齐 Python ToolResultMediaMessage：引擎写入 metadata.kind=tool_result_media，
// Focus 路径额外写 MediaIndex；两种形态都要识别，否则媒体轮次保护与孤儿清理都会失效。
func IsToolResultMediaMessage(message maisaka.ContextMessage) bool {
	if message.MetadataString("kind") == ToolResultMediaKind {
		return true
	}
	return message.MediaIndex != "" && message.Source == maisaka.SourceToolResult
}

func isOrphanToolResultMedia(message maisaka.ContextMessage, owners map[string]struct{}) bool {
	if !IsToolResultMediaMessage(message) {
		return false
	}
	_, ok := owners[message.ToolCallID]
	return !ok
}

// NormalizeToolCallResultPairs 统一清理并排序工具调用与工具结果
func NormalizeToolCallResultPairs(history []maisaka.ContextMessage, pendingCallIDs map[string]struct{}) ([]maisaka.ContextMessage, ToolPairReport) {
	processed, orphanCount := DropOrphanToolResults(history)
	processed, unansweredCount := DropUnansweredToolCalls(processed, pendingCallIDs)
	processed, movedCount := NormalizeToolResultOrder(processed)
	processed, invalidCount := DropInvalidToolTurns(processed, pendingCallIDs)
	return processed, ToolPairReport{
		OrphanToolResults:   orphanCount,
		UnansweredToolCalls: unansweredCount,
		MovedToolResults:    movedCount,
		InvalidToolTurns:    invalidCount,
	}
}

// CollectToolTurnAnchorIndices 定位每个工具轮次之前最近的 user 上下文索引
func CollectToolTurnAnchorIndices(history []maisaka.ContextMessage, logicalTurnIDs map[string]struct{}) map[string]int {
	firstTurnIndex := map[string]int{}
	for index, message := range history {
		turnID := LogicalTurnID(message)
		if turnID == "" {
			continue
		}
		if _, wanted := logicalTurnIDs[turnID]; !wanted {
			continue
		}
		if _, exists := firstTurnIndex[turnID]; !exists {
			firstTurnIndex[turnID] = index
		}
	}
	anchors := map[string]int{}
	for turnID, turnIndex := range firstTurnIndex {
		for index := turnIndex - 1; index >= 0; index-- {
			if history[index].Role == maisaka.RoleUser {
				anchors[turnID] = index
				break
			}
		}
	}
	return anchors
}

// DropUnansweredToolCalls 移除缺少对应工具结果的 assistant tool_call
func DropUnansweredToolCalls(history []maisaka.ContextMessage, pendingCallIDs map[string]struct{}) ([]maisaka.ContextMessage, int) {
	if len(history) == 0 {
		return history, 0
	}
	answered := map[string]struct{}{}
	for _, message := range history {
		if IsToolResultMessage(message) && message.ToolCallID != "" {
			answered[message.ToolCallID] = struct{}{}
		}
	}
	unanswered := map[string]struct{}{}
	for _, message := range history {
		if message.Role != maisaka.RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.CallID == "" {
				continue
			}
			if _, ok := answered[call.CallID]; ok {
				continue
			}
			if _, pending := pendingCallIDs[call.CallID]; pending {
				continue
			}
			unanswered[call.CallID] = struct{}{}
		}
	}
	if len(unanswered) == 0 {
		return history, 0
	}
	invalidTurns := map[string]struct{}{}
	removed := 0
	for _, message := range history {
		if message.Role != maisaka.RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if _, drop := unanswered[call.CallID]; drop {
				removed++
				if message.LogicalTurnID != "" {
					invalidTurns[message.LogicalTurnID] = struct{}{}
				}
				break
			}
		}
	}
	filtered := make([]maisaka.ContextMessage, 0, len(history))
	for _, message := range history {
		if _, invalid := invalidTurns[message.LogicalTurnID]; invalid && message.LogicalTurnID != "" {
			continue
		}
		if message.Role == maisaka.RoleAssistant {
			keptCalls := message.ToolCalls[:0]
			dropped := false
			for _, call := range message.ToolCalls {
				if _, drop := unanswered[call.CallID]; drop {
					dropped = true
					continue
				}
				keptCalls = append(keptCalls, call)
			}
			message.ToolCalls = keptCalls
			if dropped && len(message.ToolCalls) == 0 && message.Content == "" {
				continue
			}
		}
		filtered = append(filtered, message)
	}
	return filtered, removed
}

// DropInvalidToolTurns 删除工具协议不完整的完整逻辑 turn
//
// 对齐 Python drop_invalid_tool_turns：把模型输出与工具结果投影成共享关系内核输入，
// 由 PruneContextItemsForHistory 判定非法 turn（重复 item/call/output、孤儿 output、
// 错位 output、turn 不一致、缺 logical_turn_id），再按 turn 过滤整段历史。
func DropInvalidToolTurns(history []maisaka.ContextMessage, pendingCallIDs map[string]struct{}) ([]maisaka.ContextMessage, int) {
	items := buildToolRelationItems(history)
	if len(items) == 0 {
		return history, 0
	}
	pending := make([]string, 0, len(pendingCallIDs))
	for callID := range pendingCallIDs {
		pending = append(pending, callID)
	}
	_, report, err := payloadcontent.PruneContextItemsForHistory(items, pending)
	if err != nil {
		// 关系内核校验失败说明历史本身已不可修复，保留原样交由上层发送校验暴露
		return history, 0
	}
	if len(report.InvalidTurnIDs) == 0 {
		return history, 0
	}
	invalidTurns := make(map[string]struct{}, len(report.InvalidTurnIDs))
	for _, turnID := range report.InvalidTurnIDs {
		invalidTurns[turnID] = struct{}{}
	}
	filtered := make([]maisaka.ContextMessage, 0, len(history))
	for _, message := range history {
		if turnID := LogicalTurnID(message); turnID != "" {
			if _, invalid := invalidTurns[turnID]; invalid {
				continue
			}
		}
		filtered = append(filtered, message)
	}
	return filtered, len(history) - len(filtered)
}

// buildToolRelationItems 把模型输出与工具结果投影为共享关系内核输入
// 对齐 Python _build_tool_relation_items：只取带 tool_calls 的 assistant 与工具结果。
func buildToolRelationItems(history []maisaka.ContextMessage) []payloadcontent.ContextItem {
	items := make([]payloadcontent.ContextItem, 0, len(history))
	for _, message := range history {
		turnID := LogicalTurnID(message)
		if message.Role == maisaka.RoleAssistant && len(message.ToolCalls) > 0 {
			for _, call := range message.ToolCalls {
				if strings.TrimSpace(call.CallID) == "" {
					continue
				}
				items = append(items, payloadcontent.NewFunctionCallItem(turnID,
					payloadcontent.NewContextToolCall(call.CallID, call.Name, call.Arguments)))
			}
			continue
		}
		if IsToolResultMessage(message) {
			items = append(items, payloadcontent.NewFunctionCallOutputItem(turnID, message.ToolCallID, message.Content, message.ToolName))
		}
	}
	return items
}

// NormalizeToolResultOrder 把被其他消息隔开的 tool 结果移动到对应 assistant tool_calls 后面
func NormalizeToolResultOrder(history []maisaka.ContextMessage) ([]maisaka.ContextMessage, int) {
	if len(history) == 0 {
		return history, 0
	}
	consumed := map[int]struct{}{}
	normalized := make([]maisaka.ContextMessage, 0, len(history))
	moved := 0
	for index := 0; index < len(history); index++ {
		if _, skip := consumed[index]; skip {
			continue
		}
		message := history[index]
		if message.Role != maisaka.RoleAssistant {
			normalized = append(normalized, message)
			continue
		}
		outputIndexes := make([]int, 0, 2)
		for cursor := index; cursor < len(history) && history[cursor].Role == maisaka.RoleAssistant; cursor++ {
			if _, skip := consumed[cursor]; skip {
				continue
			}
			outputIndexes = append(outputIndexes, cursor)
		}
		if len(outputIndexes) == 0 {
			continue
		}
		calls := make([]maisaka.ToolCall, 0, 4)
		for _, outputIndex := range outputIndexes {
			consumed[outputIndex] = struct{}{}
			normalized = append(normalized, history[outputIndex])
			calls = append(calls, history[outputIndex].ToolCalls...)
		}
		appended := 0
		for _, call := range calls {
			if call.CallID == "" {
				continue
			}
			matching := findToolResultIndex(history, call.CallID, outputIndexes[0]+1, consumed)
			if matching < 0 {
				continue
			}
			consumed[matching] = struct{}{}
			normalized = append(normalized, history[matching])
			expected := outputIndexes[0] + len(outputIndexes) + appended
			if matching != expected {
				moved++
			}
			appended++
		}
	}
	if moved <= 0 {
		return history, 0
	}
	return normalized, moved
}

func findToolResultIndex(history []maisaka.ContextMessage, toolCallID string, startIndex int, consumed map[int]struct{}) int {
	for index := startIndex; index < len(history); index++ {
		if _, skip := consumed[index]; skip {
			continue
		}
		message := history[index]
		if !IsToolResultMessage(message) {
			continue
		}
		if message.ToolCallID == toolCallID {
			return index
		}
	}
	return -1
}

// SortHistoryByTimestamp 按时间稳定排序（骨架用，保持历史顺序一致性）
func SortHistoryByTimestamp(history []maisaka.ContextMessage) []maisaka.ContextMessage {
	sorted := append([]maisaka.ContextMessage{}, history...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Timestamp.Before(sorted[j].Timestamp)
	})
	return sorted
}
