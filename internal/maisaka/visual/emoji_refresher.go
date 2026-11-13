package visual

import "strings"

// EmojiComponentText 返回表情组件当前对外展示的文本
func EmojiComponentText(hash, description, placeholder string) string {
	if strings.TrimSpace(placeholder) != "" {
		return placeholder
	}
	if strings.TrimSpace(description) != "" {
		return "[表情包: " + description + "]"
	}
	return ""
}

// ShouldRefreshEmojiComponent 判断表情组件是否仍处于待补全文本的占位状态
func ShouldRefreshEmojiComponent(description, placeholder string) bool {
	if strings.TrimSpace(placeholder) == "" && strings.TrimSpace(description) == "" {
		return true
	}
	return strings.TrimSpace(placeholder) == PendingEmojiPlaceholder
}
