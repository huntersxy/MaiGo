package maisaka

import (
	"crypto/sha1"
	"encoding/base32"
	"strings"
)

// 消息 ID 短别名常量，对齐 Python context/message_id_alias.py
const (
	// MaxPlainMessageIDLength 不超过该长度的 ID 原样展示
	MaxPlainMessageIDLength = 12
	// MessageAliasPrefix 别名前缀
	MessageAliasPrefix = "m"
	// MessageAliasHashLength 别名哈希长度
	MessageAliasHashLength = 6
)

// ToDisplayMessageID 返回展示给模型的消息 ID：长 ID 缩短为稳定哈希别名，短 ID 原样返回
func ToDisplayMessageID(messageID string) string {
	normalized := strings.TrimSpace(messageID)
	if len(normalized) <= MaxPlainMessageIDLength {
		return normalized
	}
	digest := sha1.Sum([]byte(normalized))
	encoded := strings.ToLower(base32.StdEncoding.EncodeToString(digest[:]))
	if len(encoded) < MessageAliasHashLength {
		return MessageAliasPrefix + encoded
	}
	return MessageAliasPrefix + encoded[:MessageAliasHashLength]
}

// BuildAliasMap 建立别名到原始 ID 的映射，冲突时先出现的优先
func BuildAliasMap(messageIDs []string) map[string]string {
	aliasMap := map[string]string{}
	for _, rawMessageID := range messageIDs {
		messageID := strings.TrimSpace(rawMessageID)
		alias := ToDisplayMessageID(messageID)
		if alias == messageID {
			continue
		}
		if _, exists := aliasMap[alias]; !exists {
			aliasMap[alias] = messageID
		}
	}
	return aliasMap
}

// ExpandMessageIDAliases 递归地把参数里等于别名的字符串换回原始消息 ID
func ExpandMessageIDAliases(value any, aliasMap map[string]string) any {
	if len(aliasMap) == 0 {
		return value
	}
	switch typed := value.(type) {
	case string:
		if expanded, ok := aliasMap[strings.TrimSpace(typed)]; ok {
			return expanded
		}
		return value
	case map[string]any:
		expanded := make(map[string]any, len(typed))
		for key, item := range typed {
			expanded[key] = ExpandMessageIDAliases(item, aliasMap)
		}
		return expanded
	case []any:
		expanded := make([]any, len(typed))
		for index, item := range typed {
			expanded[index] = ExpandMessageIDAliases(item, aliasMap)
		}
		return expanded
	default:
		return value
	}
}
