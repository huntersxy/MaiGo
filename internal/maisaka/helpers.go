package maisaka

import (
	"strings"

	"github.com/google/uuid"
)

// ToolToolSearchName 对齐 maisaka_builtin 的 tool_search 名称
const ToolToolSearchName = "tool_search"

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func newRunID() string { return uuid.NewString() }

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// messageShape 对齐 get_message_shape：kind 1=图片 2=表情 3=语音/转发/文件/视频，length 为文本长度
func messageShape(message ContextMessage) (int, int) {
	if message.Metadata != nil {
		if kind, ok := metadataInt(message.Metadata["shape_kind"]); ok {
			if length, ok := metadataInt(message.Metadata["shape_length"]); ok {
				return kind, length
			}
		}
	}
	length := len([]rune(strings.TrimSpace(message.Content)))
	switch {
	case len(message.Images) > 0:
		return 1, length
	case len(message.Emojis) > 0:
		return 2, length
	default:
		return 0, length
	}
}

func metadataInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	default:
		return 0, false
	}
}
