package modelstats

import "context"

// clientUsageRecordingKey 标记一次模型调用已由 llm 客户端包装器记录
// eino 内置模型（Responses API）会触发 modelstats.Handler，若不抑制会与包装器重复计数
type clientUsageRecordingKey struct{}

// WithClientUsageRecording 标记当前请求已由客户端包装器负责写统计
func WithClientUsageRecording(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, clientUsageRecordingKey{}, true)
}

// ClientUsageRecording 报告当前请求是否已由客户端包装器记录
func ClientUsageRecording(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(clientUsageRecordingKey{}).(bool)
	return marked
}
