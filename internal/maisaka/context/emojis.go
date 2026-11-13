package context

import (
	"strconv"
	"time"

	maisaka "maibot-go/internal/maisaka"
)

// EmojiCandidateSource 表情候选消息来源
const EmojiCandidateSource = maisaka.EmojiCandidateSource

// BuildEmojiCandidateMessage 构造固定表情候选快照消息
func BuildEmojiCandidateMessage(visibleText string, emojiHashes map[int]string, timestamp time.Time, sessionID string) maisaka.ContextMessage {
	hashes := make(map[string]any, len(emojiHashes))
	for index, hash := range emojiHashes {
		hashes[strconv.Itoa(index)] = hash
	}
	return maisaka.ContextMessage{
		Role:      maisaka.RoleUser,
		Content:   visibleText,
		Source:    EmojiCandidateSource,
		Timestamp: timestamp,
		SessionID: sessionID,
		Metadata: map[string]any{
			"emoji_hashes": hashes,
		},
	}
}

// EmojiHashes 读取表情候选消息的编号映射
func EmojiHashes(message maisaka.ContextMessage) map[int]string {
	hashes := map[int]string{}
	if message.Metadata == nil {
		return hashes
	}
	switch value := message.Metadata["emoji_hashes"].(type) {
	case map[int]string:
		for index, hash := range value {
			hashes[index] = hash
		}
	case map[string]any:
		for rawIndex, item := range value {
			index, err := strconv.Atoi(rawIndex)
			if err != nil {
				continue
			}
			if hash, ok := item.(string); ok {
				hashes[index] = hash
			}
		}
	}
	return hashes
}

// IsEmojiCandidateMessage 判断是否是表情候选消息
func IsEmojiCandidateMessage(message maisaka.ContextMessage) bool {
	return message.Source == EmojiCandidateSource
}
