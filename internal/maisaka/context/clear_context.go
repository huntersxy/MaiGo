package context

import (
	"strings"

	maisaka "MaiGo/internal/maisaka"
)

// 上下文清理指令常量
const (
	// ClearContextCommand 清理指令
	ClearContextCommand = "/clear"
	// ClearContextMarkerKey 清理边界标记键
	ClearContextMarkerKey = "maisaka_context_cleared"
	// ClearContextInterceptLevel 拦截等级
	ClearContextInterceptLevel = 1
)

// IsClearContextCommand 判断消息文本是否是完整的 /clear 指令
func IsClearContextCommand(text string) bool {
	return strings.TrimSpace(text) == ClearContextCommand
}

// MarkClearContextCommand 标记消息为 Maisaka 上下文清理边界
func MarkClearContextCommand(message *maisaka.ContextMessage) {
	if message == nil {
		return
	}
	message.MetadataBoolSet(ClearContextMarkerKey, true)
}

// IsClearContextMarker 判断消息是否是已执行的清理边界
func IsClearContextMarker(message maisaka.ContextMessage) bool {
	return message.MetadataBool(ClearContextMarkerKey)
}

// SelectMessagesAfterLatestClearMarker 仅保留最后一次清理之后的消息
func SelectMessagesAfterLatestClearMarker[T any](messages []T, isMarker func(T) bool, isCommand func(T) bool) []T {
	start := 0
	for index, message := range messages {
		if isMarker(message) {
			start = index + 1
		}
	}
	result := make([]T, 0, len(messages)-start)
	for _, message := range messages[start:] {
		if isCommand != nil && isCommand(message) {
			continue
		}
		result = append(result, message)
	}
	return result
}
