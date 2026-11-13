package runtime

// 跨会话传话的图片来源索引，对齐 Python agent/messaging.py source_images

import (
	"strings"

	"maibot-go/internal/common/media"
	"maibot-go/internal/maisaka"
)

// SourceImages 返回当前会话可传给其他聊天的图片来源表
// 键包含消息编号、模型可见的短编号、图片完整哈希与短哈希，后写覆盖先写
func (s *Session) SourceImages() map[string][]media.Ref {
	available := map[string][]media.Ref{}
	if s == nil {
		return available
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	add := func(user *sessionUser) {
		if user == nil || len(user.MediaRefs) == 0 {
			return
		}
		// 助手回复与工具结果不是 Python 的 ContentContextMessage，只有内部任务消息例外
		if user.IsAgentMessage && !user.IsInternalTask {
			return
		}
		messageID := strings.TrimSpace(user.Ref)
		if messageID == "" {
			messageID = strings.TrimSpace(user.NoticeID)
		}
		if messageID == "" {
			return
		}
		refs := append([]media.Ref(nil), user.MediaRefs...)
		available[messageID] = refs
		available[maisaka.ToDisplayMessageID(messageID)] = refs
		for _, ref := range refs {
			if hash := strings.TrimSpace(ref.Hash); hash != "" {
				available[hash] = []media.Ref{ref}
			}
			if short := strings.TrimSpace(ref.Short); short != "" {
				available[short] = []media.Ref{ref}
			}
		}
	}
	for _, entry := range s.seen {
		add(entry.user)
	}
	for _, user := range s.pending {
		add(user)
	}
	return available
}
