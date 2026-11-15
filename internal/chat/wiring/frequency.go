// Package chatwiring 把 messagereceive 的端口接到宿主真实能力，并提供可注入的装配入口
package chatwiring

import (
	"context"
	"math"
	"strings"

	"MaiGo/internal/chat/message_receive"
)

// RuntimeFrequencyLookup 读取心流运行时当前生效的回复频率
type RuntimeFrequencyLookup interface {
	EffectiveReplyFrequency(sessionID string) (float64, bool)
}

// FrequencyFiller 对齐 Python fill_reply_frequency_if_available
// 优先使用心流运行时值，运行时缺失时回退到聊天流配置
type FrequencyFiller struct {
	Runtime  RuntimeFrequencyLookup
	Fallback func(message *messagereceive.SessionMessage) (float64, bool)
}

// FillReplyFrequency 消息已有频率时保持不变，没有可用来源时保留空值
func (f *FrequencyFiller) FillReplyFrequency(_ context.Context, message *messagereceive.SessionMessage) error {
	if f == nil || message == nil || message.ReplyFrequency != nil {
		return nil
	}
	frequency, ok := f.lookup(message)
	if !ok {
		return nil
	}
	message.ReplyFrequency = &frequency
	return nil
}

// SessionFrequency 供没有 SessionMessage 的心流落库路径读取频率
func (f *FrequencyFiller) SessionFrequency(sessionID string) (float64, bool) {
	if f == nil || strings.TrimSpace(sessionID) == "" {
		return 0, false
	}
	if f.Runtime == nil {
		return 0, false
	}
	frequency, ok := f.Runtime.EffectiveReplyFrequency(strings.TrimSpace(sessionID))
	if !ok {
		return 0, false
	}
	return normalizeFrequency(frequency)
}

func (f *FrequencyFiller) lookup(message *messagereceive.SessionMessage) (float64, bool) {
	if f.Runtime != nil && strings.TrimSpace(message.SessionID) != "" {
		if frequency, ok := f.Runtime.EffectiveReplyFrequency(strings.TrimSpace(message.SessionID)); ok {
			return normalizeFrequency(frequency)
		}
	}
	if f.Fallback != nil {
		if frequency, ok := f.Fallback(message); ok {
			return normalizeFrequency(frequency)
		}
	}
	return 0, false
}

func normalizeFrequency(frequency float64) (float64, bool) {
	if math.IsNaN(frequency) || math.IsInf(frequency, 0) {
		return 0, false
	}
	if frequency < 0 {
		frequency = 0
	}
	return frequency, true
}
