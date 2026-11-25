package context

import (
	"regexp"
	"strings"
	"time"
)

// SpeakerPrefixPattern 说话人前缀正则，对齐 Python SPEAKER_PREFIX_PATTERN
var SpeakerPrefixPattern = regexp.MustCompile(`^(?:(\d{2}:\d{2}:\d{2}))?(?:\[msg_id:([^\]]+)\])?\[([^\]]+)\]([\s\S]*)$`)

// FormatSpeakerContent 将可见文本格式化为带说话人前缀的样式
func FormatSpeakerContent(speakerName, content string, timestamp *time.Time, messageID string) string {
	timePrefix := ""
	if timestamp != nil {
		timePrefix = timestamp.Format("15:04:05")
	}
	messageIDPrefix := ""
	if messageID != "" {
		messageIDPrefix = "[msg_id:" + ToDisplayMessageID(messageID) + "]"
	}
	return timePrefix + messageIDPrefix + "[" + speakerName + "]" + content
}

// ParseSpeakerContent 解析形如 [speaker]message 的可见文本
func ParseSpeakerContent(content string) (string, string) {
	matches := SpeakerPrefixPattern.FindStringSubmatch(content)
	if matches == nil {
		return "", content
	}
	return matches[3], matches[4]
}

// RenderAtText 将 @ 目标渲染为带尾随空格的文本
func RenderAtText(cardName, nickname, userID string) string {
	target := strings.TrimSpace(cardName)
	if target == "" {
		target = strings.TrimSpace(nickname)
	}
	if target == "" {
		target = strings.TrimSpace(userID)
	}
	return "@" + target + " "
}

// RenderVisibleReply 渲染引用组件文本
func RenderVisibleReply(targetMessageID string) string {
	trimmed := strings.TrimSpace(targetMessageID)
	if trimmed == "" {
		return ""
	}
	return "[引用消息]" + ToDisplayMessageID(trimmed)
}

// BuildVisibleText 从可见文本片段拼接最终展示文本
func BuildVisibleText(parts []string) string {
	return strings.Join(parts, "")
}
