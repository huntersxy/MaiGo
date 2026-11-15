package focus

import (
	"sync"

	"MaiGo/internal/maisaka/quote"
)

// SessionRuntime 会话级 Focus 运行态（内存）；完整 FocusGroupRuntime 仍待迁移
type SessionRuntime struct {
	mu sync.Mutex

	quoteCounter   *quote.Counter
	quoteSessionID string
	rosterSnapshot string
}

// NewSessionRuntime 创建空的会话 Focus 运行态
func NewSessionRuntime() *SessionRuntime {
	return &SessionRuntime{}
}

// EnsureQuoteCounter 按 MaiBot session_id 懒初始化引用计数器
func (r *SessionRuntime) EnsureQuoteCounter(sessionID string) *quote.Counter {
	if r == nil || sessionID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.quoteCounter != nil && r.quoteSessionID == sessionID {
		return r.quoteCounter
	}
	r.quoteSessionID = sessionID
	r.quoteCounter = quote.NewCounter(sessionID)
	return r.quoteCounter
}

// RosterSnapshot 返回上次注入 Planner 时的 roster 文本快照
func (r *SessionRuntime) RosterSnapshot() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rosterSnapshot
}

// SetRosterSnapshot 更新 roster 快照并返回是否与上次不同
func (r *SessionRuntime) SetRosterSnapshot(snapshot string) bool {
	if r == nil {
		return snapshot != ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := r.rosterSnapshot != snapshot
	r.rosterSnapshot = snapshot
	return changed
}
