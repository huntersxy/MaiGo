package chatsession

import (
	"strings"
	"sync"
)

// maxRememberedReplyTargets 会话内保留的回复目标记录上限
const maxRememberedReplyTargets = 128

// ReplyTargetMemory 记录会话内最近一次回复同一目标消息的正文
//
// 对齐 Python maisaka/builtin_tool/reply.py::_find_recent_reply_to_target：
// 优先从 Maisaka 历史反查 source_kind == "guided_reply" 且引用了该目标的消息；
// 历史查不到时回退到发送时写入的缓存（历史可能已被压缩或尚未建立）。
//
// 历史反查由调用方通过 SetHistoryLookup 注入，避免本包依赖 maisaka。
type ReplyTargetMemory struct {
	mu      sync.Mutex
	targets map[string]string
	order   []string

	historyLookup func(msgID string) string
}

// NewReplyTargetMemory 创建空的回复目标记忆
func NewReplyTargetMemory() *ReplyTargetMemory {
	return &ReplyTargetMemory{targets: map[string]string{}}
}

// SetHistoryLookup 注入历史反查函数；传 nil 时只用缓存
func (m *ReplyTargetMemory) SetHistoryLookup(lookup func(msgID string) string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.historyLookup = lookup
}

// Previous 返回最近一次回复该目标消息的正文
func (m *ReplyTargetMemory) Previous(msgID string) string {
	if m == nil {
		return ""
	}
	key := strings.TrimSpace(msgID)
	if key == "" {
		return ""
	}
	m.mu.Lock()
	lookup := m.historyLookup
	cached := m.targets[key]
	m.mu.Unlock()

	if lookup != nil {
		if text := strings.TrimSpace(lookup(key)); text != "" {
			return text
		}
	}
	return cached
}

// Record 记录成功发送的回复目标与正文，超出上限时按写入顺序淘汰
func (m *ReplyTargetMemory) Record(msgID string, content string) {
	if m == nil {
		return
	}
	key := strings.TrimSpace(msgID)
	text := strings.TrimSpace(content)
	if key == "" || text == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.targets == nil {
		m.targets = map[string]string{}
	}
	if _, exists := m.targets[key]; !exists {
		m.order = append(m.order, key)
	}
	m.targets[key] = text
	for len(m.order) > maxRememberedReplyTargets {
		oldest := m.order[0]
		m.order = m.order[1:]
		delete(m.targets, oldest)
	}
}

// Len 返回当前缓存的记录条数
func (m *ReplyTargetMemory) Len() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.targets)
}
