package maisaka

// HistoryPostProcessOptions 每轮结束后的历史后处理选项，由 Agent 注入的实现解释
type HistoryPostProcessOptions struct {
	// MaxContextSize 当前会话的上下文条数上限
	MaxContextSize   int
	MaxContextTokens int
	PromptTokens     int
	// PendingCallIDs 仍处于等待中的工具调用编号
	PendingCallIDs map[string]struct{}
	// ConsumeReferences 是否消费一次性引用
	ConsumeReferences bool
}

// HistoryPostProcessResult 历史后处理结果
type HistoryPostProcessResult struct {
	History               []ContextMessage
	RemovedMessages       []ContextMessage
	RemovedCount          int
	ChangedCount          int
	RemainingContextCount int
}

// HistoryPostProcessor 历史后处理端口，避免 maisaka 主包反向依赖 context 子包
type HistoryPostProcessor func(history []ContextMessage, options HistoryPostProcessOptions) HistoryPostProcessResult

// HistoryTrimSink 接收每轮提交后的历史变化，供宿主同步持久工作集
type HistoryTrimSink func(original, final []ContextMessage)

// IsContextSummaryMessage 判断是否是常驻上下文摘要消息
func IsContextSummaryMessage(message ContextMessage) bool {
	if message.Source == ContextSummarySource {
		return true
	}
	if message.Metadata == nil {
		return false
	}
	source, _ := message.Metadata["source"].(string)
	return source == ContextSummarySource
}
