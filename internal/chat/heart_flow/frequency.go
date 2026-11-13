package heartflow

import (
	"math"
	"strings"
)

// EffectiveReplyFrequencyRuntime 可提供当前会话生效回复频率的运行时
// 对应 Python runtime._get_effective_reply_frequency；由 *maisaka.Runtime 实现
type EffectiveReplyFrequencyRuntime interface {
	EffectiveReplyFrequency() float64
}

// EffectiveReplyFrequency 返回指定会话当前生效的回复频率
//
// 只读取已存在的运行实例，不触发创建；运行实例不存在或不支持频率读取时
// 返回 ok=false，由调用方回退到聊天流配置计算，避免凭空造出频率
func (m *Manager) EffectiveReplyFrequency(sessionID string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, false
	}
	m.mu.Lock()
	entry, exists := m.chats[sessionID]
	m.mu.Unlock()
	if !exists || entry == nil || entry.runtime == nil {
		return 0, false
	}
	provider, ok := entry.runtime.(EffectiveReplyFrequencyRuntime)
	if !ok {
		return 0, false
	}
	frequency := provider.EffectiveReplyFrequency()
	if math.IsNaN(frequency) || math.IsInf(frequency, 0) {
		return 0, false
	}
	if frequency < 0 {
		frequency = 0
	}
	return frequency, true
}
