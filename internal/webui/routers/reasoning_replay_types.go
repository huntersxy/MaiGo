package webuiapi

import (
	"context"
	"encoding/json"
)

// ReasoningReplayRequest WebUI 推理重放请求，字段对齐 Python ReasoningReplayRequest
type ReasoningReplayRequest struct {
	SourcePath        string            `json:"source_path,omitempty"`
	Stage             string            `json:"stage,omitempty"`
	ModelName         string            `json:"model_name"`
	ItemSchemaVersion int               `json:"item_schema_version"`
	RequestItems      []json.RawMessage `json:"request_items"`
	ToolDefinitions   []json.RawMessage `json:"tool_definitions,omitempty"`
	Temperature       *float64          `json:"temperature,omitempty"`
	MaxTokens         *int              `json:"max_tokens,omitempty"`
}

// ReasoningReplayResult 重放响应，字段对齐 Python ReasoningReplayResponse
type ReasoningReplayResult struct {
	Success            bool
	OutputItems        []map[string]any
	SchemaVersion      int
	GenerationAttempts []map[string]any
	ModelName          string
	PromptTokens       int
	CompletionTokens   int
	TotalTokens        int
	PromptCacheHit     int
	PromptCacheMiss    int
	DurationMS         float64
	Error              string
}

// ReasoningReplayHTTPError 重放前置校验错误，携带与 Python 对齐的 HTTP 状态码
type ReasoningReplayHTTPError struct {
	Status  int
	Message string
}

func (e *ReasoningReplayHTTPError) Error() string { return e.Message }

// ReasoningReplayService 可注入的推理重放后端；默认实现走 agent 的 LLM 路由器
type ReasoningReplayService interface {
	ReplayReasoning(ctx context.Context, request ReasoningReplayRequest) (ReasoningReplayResult, error)
}
