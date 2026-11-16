package replyer

import (
	"context"
	"time"

	"MaiGo/internal/llm_models"
	"MaiGo/internal/plugin_runtime/hooks"
)

// MaxHookRetries 对齐 maisaka_generator_base.REPLYER_MAX_HOOK_RETRIES
const MaxHookRetries = 3

// GenerationTrace 对齐 Python generation_trace 的最小字段集；仅用于判断推理续写
type GenerationTrace struct {
	Status string
}

// GenerationAttempt 对齐 Python GenerationAttempt，供 replyer 监控详情序列化
type GenerationAttempt struct {
	AttemptID       string
	WorkflowPurpose string
	WorkflowAttempt int
	ProviderAttempt int
	ModelAttempt    int
	Status          string
	StartedAt       time.Time
	DurationMS      int64
	Provider        string
	Endpoint        string
	Model           string
	ClientType      string
	Operation       string
	WireProtocol    string
	Error           *GenerationAttemptError
}

// GenerationAttemptError 对齐 Python attempt.error 字段
type GenerationAttemptError struct {
	Message    string
	StatusCode int
	Type       string
}

// ModelInfo 本次实际选中模型的最小描述，供视觉开关与 Hook 使用
type ModelInfo struct {
	Name            string
	ModelIdentifier string
	Visual          bool
	ClientType      string
	APIProvider     string
	MaxTokens       int
	Temperature     *float64
}

// GenerationOptions 对齐 LLMGenerationOptions 的 replyer 子集
type GenerationOptions struct {
	ModelName      string
	Temperature    *float64
	MaxTokens      int
	Stream         bool
	ForceStream    bool
	Tools          []any
	ResponseFormat any
	Memory         map[string]any
}

// GenerationResult 对齐 LLMResponseResult 的 replyer 子集
type GenerationResult struct {
	OutputItems           []llm.ContextItem
	GenerationTrace       *GenerationTrace
	GenerationAttempts    []GenerationAttempt
	ModelName             string
	ClientType            string
	Reasoning             string
	Response              string
	ToolCalls             []llm.ContextToolCall
	PromptTokens          int
	CompletionTokens      int
	TotalTokens           int
	PromptCacheHitTokens  int
	PromptCacheMissTokens int
	PromptCacheReported   bool
	ProviderResponse      map[string]any
	WireProtocol          string
}

// ResponseText 返回 Provider 正文；优先使用已解析的 output_items
func (r GenerationResult) ResponseText() string {
	if len(r.OutputItems) > 0 {
		return llm.GetContextResponseText(r.OutputItems)
	}
	return r.Response
}

// ReasoningText 返回 Provider reasoning；优先使用已解析的 output_items
func (r GenerationResult) ReasoningText() string {
	if len(r.OutputItems) > 0 {
		if text := llm.GetContextResponseReasoning(r.OutputItems); text != "" {
			return text
		}
	}
	return r.Reasoning
}

// ContextFactory 对齐 Python context_factory：客户端选定实际模型后按 ModelInfo 构建请求
type ContextFactory func(ctx context.Context, modelInfo *ModelInfo) ([]llm.ContextItem, error)

// LLMClient 是 replyer 对 LLM 服务的最小端口
// 流式与非流式都由实现内部处理；replyer 只消费最终 response/output_items
type LLMClient interface {
	TaskName() string
	GenerateResponseWithContext(ctx context.Context, factory ContextFactory, options GenerationOptions) (GenerationResult, error)
}

// LLMClientFactory 按 task 构造客户端，对齐 Python self._llm_client_cls(...)
type LLMClientFactory func(taskName, requestType, sessionID string) (LLMClient, error)

// HookResult 对齐插件 Hook 调用结果
type HookResult struct {
	Aborted     bool
	Kwargs      map[string]any
	HookName    string
	HandlerName string
	Reason      string
}

// AbortError 把 Hook 中止信息转成跨调用层的错误；未中止时返回 nil
func (r HookResult) AbortError() error {
	if !r.Aborted {
		return nil
	}
	return &hooks.HookAbortedError{HookName: r.HookName, HandlerName: r.HandlerName, Reason: r.Reason}
}

// HookInvoker 是 replyer 对插件运行时的最小端口
type HookInvoker interface {
	InvokeHook(ctx context.Context, name string, kwargs map[string]any) (HookResult, error)
}

// HookInvokerFunc 让调用方用函数适配 HookInvoker
type HookInvokerFunc func(ctx context.Context, name string, kwargs map[string]any) (HookResult, error)

// InvokeHook 实现 HookInvoker
func (f HookInvokerFunc) InvokeHook(ctx context.Context, name string, kwargs map[string]any) (HookResult, error) {
	return f(ctx, name, kwargs)
}

// ExpressionSelectRequest 一次表达方式选择请求
type ExpressionSelectRequest struct {
	SessionID      string
	ChatHistory    []ChatMessage
	Target         *ReplyTarget
	ReplyReason    string
	ReplyToolArgs  map[string]any
	SubAgentRunner func(context.Context, string) (string, error)
}

// ExpressionSelection 对齐 MaisakaExpressionSelectionResult
type ExpressionSelection struct {
	ExpressionHabits      string
	SelectedExpressionIDs []int
	SelectedExpressions   []map[string]any
}

// ExpressionSelector 是 replyer 对表达方式选择器的端口
type ExpressionSelector interface {
	SelectForReply(ctx context.Context, request ExpressionSelectRequest) (ExpressionSelection, error)
}

// ReplyContext 对齐 MaisakaReplyContext
type ReplyContext struct {
	ExpressionHabits      string
	SelectedExpressionIDs []int
	SelectedExpressions   []map[string]any
}

// LLMCompletionResult 对齐 LLMCompletionResult
type LLMCompletionResult struct {
	RequestPrompt    string
	ResponseText     string
	ReasoningText    string
	ModelName        string
	ToolCalls        []llm.ContextToolCall
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// GenerationMetrics 对齐 GenerationMetrics
type GenerationMetrics struct {
	PromptMS  float64
	LLMMS     float64
	OverallMS float64
	StageLogs []string
	Extra     map[string]any
}

// ReplyGenerationResult 对齐 ReplyGenerationResult 的 replyer 侧字段
type ReplyGenerationResult struct {
	Success bool

	Completion LLMCompletionResult
	Metrics    GenerationMetrics

	SelectedExpressionIDs     []int
	SelectedExpressionDetails []map[string]any

	TextFragments []string

	ErrorMessage string
	// ErrorCode 可供调用方区分的失败类型；成功时为空
	ErrorCode string
	// ErrorDetails 结构化失败详情，例如中止回复的 Hook、处理器和原因
	ErrorDetails map[string]any

	RequestMessageCount int
	RequestMessages     []llm.ContextItem
	OutputItems         []llm.ContextItem
	GenerationAttempts  []GenerationAttempt
}
