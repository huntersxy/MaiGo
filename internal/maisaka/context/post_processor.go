package context

import (
	maisaka "maibot-go/internal/maisaka"
)

// 历史后处理常量
const (
	// TrimTargetRatio 裁切后保留目标占配置条数的比例
	TrimTargetRatio = 0.2
	// MinRetainedChatMessages 至少保留的真实聊天消息条数
	MinRetainedChatMessages = 5
)

// HistoryPostProcessResult 历史后处理结果，与主包共享同一契约
type HistoryPostProcessResult = maisaka.HistoryPostProcessResult

// PostProcessOptions 历史后处理选项，与主包共享同一契约
type PostProcessOptions = maisaka.HistoryPostProcessOptions

// IsContextSummaryMessage 判断是否是常驻上下文摘要消息
func IsContextSummaryMessage(message maisaka.ContextMessage) bool {
	return maisaka.IsContextSummaryMessage(message)
}

// ProcessChatHistoryAfterCycle 在每轮结束后统一执行历史裁切与清理
func ProcessChatHistoryAfterCycle(history []maisaka.ContextMessage, options PostProcessOptions) HistoryPostProcessResult {
	processed := make([]maisaka.ContextMessage, 0, len(history))
	oneShotRemoved := 0
	for _, message := range history {
		if options.ConsumeReferences && consumeOnce(&message) == false {
			oneShotRemoved++
			continue
		}
		processed = append(processed, message)
	}
	processed, normalizedRemoved, movedCount := normalizeHistoryStructure(processed, options.PendingCallIDs)
	remaining := countInContext(processed)

	compactRemoved := 0
	removedMessages := []maisaka.ContextMessage{}
	if options.MaxContextSize > 0 && remaining >= options.MaxContextSize {
		target := int(float64(options.MaxContextSize) * TrimTargetRatio)
		if target < MinRetainedChatMessages {
			target = MinRetainedChatMessages
		}
		processed, removedMessages = trimHistoryToContextTarget(processed, target, options.PendingCallIDs)
		var removedAfterTrim, movedAfterTrim int
		processed, removedAfterTrim, movedAfterTrim = normalizeHistoryStructure(processed, options.PendingCallIDs)
		compactRemoved = len(removedMessages) + removedAfterTrim
		movedCount += movedAfterTrim
	}
	remaining = countInContext(processed)
	removedCount := oneShotRemoved + normalizedRemoved + compactRemoved
	return HistoryPostProcessResult{
		History:               processed,
		RemovedMessages:       removedMessages,
		RemovedCount:          removedCount,
		ChangedCount:          removedCount + movedCount,
		RemainingContextCount: remaining,
	}
}

// consumeOnce 消费一次引用生命周期，返回是否继续保留
// 对齐 Python ReferenceMessage.consume_once：未声明 remaining_uses 视为默认 1 次；
// 显式设置为 nil 表示永久引用；整数每次消费减 1，减后仍大于 0 才保留
func consumeOnce(message *maisaka.ContextMessage) bool {
	if message.Role != maisaka.RoleReference {
		return true
	}
	if message.Metadata == nil {
		return false
	}
	remaining, ok := message.Metadata["remaining_uses"]
	if !ok {
		return false
	}
	if remaining == nil {
		return true
	}
	count, ok := remainingUseCount(remaining)
	if !ok || count <= 0 {
		return false
	}
	message.Metadata["remaining_uses"] = count - 1
	return count-1 > 0
}

// remainingUseCount 读取 remaining_uses 的整数表示，兼容 JSON 解码出的多种数字类型
func remainingUseCount(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func countInContext(history []maisaka.ContextMessage) int {
	count := 0
	for _, message := range history {
		if message.CountInContext {
			count++
		}
	}
	return count
}

func normalizeHistoryStructure(history []maisaka.ContextMessage, pendingCallIDs map[string]struct{}) ([]maisaka.ContextMessage, int, int) {
	processed, report := NormalizeToolCallResultPairs(history, pendingCallIDs)
	processed, leadingRemoved := DropLeadingOrphanToolResults(processed)
	removed := report.OrphanToolResults + report.UnansweredToolCalls + report.InvalidToolTurns + leadingRemoved
	return processed, removed, report.MovedToolResults
}

func trimHistoryToContextTarget(history []maisaka.ContextMessage, targetContextCount int, pendingCallIDs map[string]struct{}) ([]maisaka.ContextMessage, []maisaka.ContextMessage) {
	remainingCount := countInContext(history)
	if remainingCount <= targetContextCount {
		return history, nil
	}
	toolTurnIDs := CollectToolTurnIDs(history)
	mediaTurnIDs := map[string]string{}
	for _, message := range history {
		turnID := LogicalTurnID(message)
		if _, ok := toolTurnIDs[turnID]; !ok {
			continue
		}
		for _, call := range message.ToolCalls {
			mediaTurnIDs[call.CallID] = turnID
		}
	}
	protectedStart := len(history) - 1
	if protectedStart < 0 {
		protectedStart = 0
	}
	chatMessageIndices := make([]int, 0, len(history))
	turnIndexes := map[string][]int{}
	for index, message := range history {
		if message.CountInContext {
			chatMessageIndices = append(chatMessageIndices, index)
			protectedStart = index
		}
		turnID := LogicalTurnID(message)
		if IsToolResultMediaMessage(message) {
			turnID = mediaTurnIDs[message.ToolCallID]
		}
		if _, ok := toolTurnIDs[turnID]; ok {
			turnIndexes[turnID] = append(turnIndexes[turnID], index)
		}
	}
	if len(chatMessageIndices) > 0 {
		oldest := chatMessageIndices[max(0, len(chatMessageIndices)-MinRetainedChatMessages)]
		if oldest < protectedStart {
			protectedStart = oldest
		}
	}
	anchors := CollectToolTurnAnchorIndices(history, toolTurnIDs)
	spans := make([][2]int, 0, len(turnIndexes))
	for turnID, indexes := range turnIndexes {
		start := indexes[0]
		if anchor, ok := anchors[turnID]; ok && anchor < start {
			start = anchor
		}
		spans = append(spans, [2]int{start, indexes[len(indexes)-1] + 1})
	}
	for index, message := range history {
		if message.Role != maisaka.RoleAssistant {
			continue
		}
		hasPending := false
		for _, call := range message.ToolCalls {
			if _, pending := pendingCallIDs[call.CallID]; pending {
				hasPending = true
				break
			}
		}
		if hasPending {
			anchor := index
			if value, ok := anchors[LogicalTurnID(message)]; ok {
				anchor = value
			}
			if anchor < protectedStart {
				protectedStart = anchor
			}
		}
	}
	boundary := 0
	for candidate := 1; candidate <= protectedStart; candidate++ {
		message := history[candidate-1]
		if !IsContextSummaryMessage(message) {
			remainingCount -= countOne(message)
		}
		if insideSpan(spans, candidate) {
			continue
		}
		boundary = candidate
		if remainingCount <= targetContextCount {
			break
		}
	}
	removed := make([]maisaka.ContextMessage, 0, boundary)
	kept := make([]maisaka.ContextMessage, 0, len(history)-boundary)
	for index := 0; index < len(history); index++ {
		message := history[index]
		if index < boundary && !IsContextSummaryMessage(message) {
			removed = append(removed, message)
			continue
		}
		kept = append(kept, message)
	}
	return kept, removed
}

func countOne(message maisaka.ContextMessage) int {
	if message.CountInContext {
		return 1
	}
	return 0
}

func insideSpan(spans [][2]int, candidate int) bool {
	for _, span := range spans {
		if span[0] < candidate && candidate < span[1] {
			return true
		}
	}
	return false
}

// CollectToolTurnIDs 收集至少包含一次 function call/output 的逻辑工具轮次
func CollectToolTurnIDs(history []maisaka.ContextMessage) map[string]struct{} {
	turnIDs := map[string]struct{}{}
	for _, message := range history {
		turnID := LogicalTurnID(message)
		if turnID == "" {
			continue
		}
		if IsToolResultMessage(message) || len(message.ToolCalls) > 0 {
			turnIDs[turnID] = struct{}{}
		}
	}
	return turnIDs
}
