package llm

import "context"

// modelUsageMetadata 由调用方注入一次模型请求的任务/会话信息；覆盖 ModelConfig 默认值
type modelUsageMetadata struct {
	Task        string
	RequestType string
	SessionID   string
}

type modelUsageMetadataKey struct{}

// WithModelUsageMetadata 给一次生成/嵌入/转写请求附加统计元数据
// 对齐 MaiBot LLMService/LLMOrchestrator 每次实际客户端调用传入的 task_name/request_type/session_id
func WithModelUsageMetadata(ctx context.Context, task, requestType, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, modelUsageMetadataKey{}, modelUsageMetadata{
		Task:        trimTask(task),
		RequestType: trimTask(requestType),
		SessionID:   trimTask(sessionID),
	})
}

// resolveUsageMetadata 合并调用方元数据与 ModelConfig 默认值
func resolveUsageMetadata(ctx context.Context, cfg ModelConfig) (task, requestType, sessionID string) {
	task = trimTask(cfg.TaskName)
	requestType = trimTask(cfg.RequestType)
	sessionID = trimTask(cfg.SessionID)
	if ctx == nil {
		return task, requestType, sessionID
	}
	meta, ok := ctx.Value(modelUsageMetadataKey{}).(modelUsageMetadata)
	if !ok {
		return task, requestType, sessionID
	}
	if meta.Task != "" {
		task = meta.Task
	}
	if meta.RequestType != "" {
		requestType = meta.RequestType
	}
	sessionID = meta.SessionID
	return task, requestType, sessionID
}
