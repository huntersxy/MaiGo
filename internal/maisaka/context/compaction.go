package context

import (
	"time"

	maisaka "MaiGo/internal/maisaka"
)

// 压缩相关常量，转发 root 实现
const (
	ContextSummarySource = maisaka.ContextSummarySource
	ContextSummaryPrefix = maisaka.ContextSummaryPrefix
)

// ConversationState 可直接接续对话的状态
type ConversationState = maisaka.ConversationState

// ParseConversationState 解析压缩结果中的状态块
func ParseConversationState(raw string) (*ConversationState, error) {
	return maisaka.ParseConversationState(raw)
}

// MessageTextForCompaction 将消息、工具参数和执行结果编码为资料
func MessageTextForCompaction(message maisaka.ContextMessage) string {
	return maisaka.MessageTextForCompaction(message)
}

// BuildContextSummaryMessage 构造常驻 Planner 的上下文摘要消息
func BuildContextSummaryMessage(content string, timestamp time.Time, prefix string) maisaka.ContextMessage {
	return maisaka.BuildSummaryMessage(content, timestamp, prefix)
}
