package context

import (
	maisaka "maibot-go/internal/maisaka"
)

// 消息 ID 别名常量，转发 root 实现
const (
	MaxPlainMessageIDLength = maisaka.MaxPlainMessageIDLength
	MessageAliasPrefix      = maisaka.MessageAliasPrefix
	MessageAliasHashLength  = maisaka.MessageAliasHashLength
)

// ToDisplayMessageID 返回展示给模型的消息 ID
func ToDisplayMessageID(messageID string) string {
	return maisaka.ToDisplayMessageID(messageID)
}

// BuildAliasMap 建立别名到原始 ID 的映射
func BuildAliasMap(messageIDs []string) map[string]string {
	return maisaka.BuildAliasMap(messageIDs)
}

// ExpandMessageIDAliases 递归地把参数里等于别名的字符串换回原始消息 ID
func ExpandMessageIDAliases(value any, aliasMap map[string]string) any {
	return maisaka.ExpandMessageIDAliases(value, aliasMap)
}
