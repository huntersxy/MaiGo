package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// modelAttempt 单个候选模型的重试与硬超时设置
type modelAttempt struct {
	Policy RetryPolicy
	// HardTimeout 任务级 hard_timeout，0 表示不限
	HardTimeout time.Duration
}

// failoverModel 在同一个任务的多候选模型间做请求级切换
// 对齐 MaiBot LLMOrchestrator._execute_request：
//   - 单次逻辑请求最多尝试 model_list 中的全部候选
//   - 同一模型内先按 api_providers.max_retry 重试，再切换下一个模型
//   - 单模型尝试受任务级 hard_timeout 约束，超时直接切换下一个
type failoverModel struct {
	router   *Router
	task     TaskName
	strategy SelectionStrategy
	models   []string
	clients  map[string]model.AgenticModel
	attempts map[string]modelAttempt
	configs  map[string]ModelConfig
}

// newFailoverModel 创建请求级模型包装器
func newFailoverModel(
	router *Router,
	task TaskName,
	models []string,
	strategy SelectionStrategy,
	clients map[string]model.AgenticModel,
	attempts map[string]modelAttempt,
	configs ...map[string]ModelConfig,
) *failoverModel {
	resolvedConfigs := map[string]ModelConfig{}
	if len(configs) > 0 && configs[0] != nil {
		resolvedConfigs = configs[0]
	}
	return &failoverModel{
		router:   router,
		task:     task,
		strategy: strategy,
		models:   models,
		clients:  clients,
		attempts: attempts,
		configs:  resolvedConfigs,
	}
}

// Generate 依次尝试候选模型，直到得到完整响应
func (m *failoverModel) Generate(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	ctx, _ = ensureSnapshotAttemptRecorder(ctx)
	excluded := make(map[string]struct{})
	var lastErr error
	attempts := MaxModelAttempts(m.strategy, len(m.models))
	for attempt := 0; attempt < attempts; attempt++ {
		name, client, ok := m.nextModel(excluded)
		if !ok {
			break
		}
		m.reserve(name)
		var message *schema.AgenticMessage
		err := m.runner(name, input, opts...).run(ctx, func(runCtx context.Context) error {
			var callErr error
			message, callErr = client.Generate(runCtx, input, opts...)
			return callErr
		})
		if compressed, ok := m.compressedInputForImageError(err, input, client); ok {
			err = m.runner(name, compressed, opts...).run(ctx, func(runCtx context.Context) error {
				var callErr error
				message, callErr = client.Generate(runCtx, compressed, opts...)
				return callErr
			})
		}
		if err == nil {
			m.complete(name, agenticMessageTokens(message))
			return message, nil
		}
		if failoverStopped(ctx, err) {
			m.release(name)
			return nil, err
		}
		m.fail(name)
		lastErr = err
		excluded[name] = struct{}{}
	}
	if lastErr != nil {
		m.emitFinalError(lastErr)
		return nil, lastErr
	}
	err := fmt.Errorf("任务 %q 无可用模型", m.task)
	m.emitFinalError(err)
	return nil, err
}

// Stream 先缓冲整条流再交付；建流与读流中途的可重试错误都整请求重放
// 对齐 MaiBot _attempt_request_on_model：Python 流式处理器缓冲整条流后失败会丢弃部分内容并按
// 同一重试预算重放整个请求；Go 在 failover 层执行同样语义，hard_timeout 覆盖建流与读流全程
// 流式响应的 usage 在完成事件中，整条流读完才算成功，回放时累计 token
func (m *failoverModel) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	ctx, _ = ensureSnapshotAttemptRecorder(ctx)
	excluded := make(map[string]struct{})
	var lastErr error
	attempts := MaxModelAttempts(m.strategy, len(m.models))
	for attempt := 0; attempt < attempts; attempt++ {
		name, client, ok := m.nextModel(excluded)
		if !ok {
			break
		}
		m.reserve(name)
		buffered, err := m.bufferedStreamAttempt(ctx, name, client, input, opts...)
		if compressed, ok := m.compressedInputForImageError(err, input, client); ok {
			buffered, err = m.bufferedStreamAttempt(ctx, name, client, compressed, opts...)
		}
		if err == nil {
			m.release(name)
			return trackStreamUsage(m.router, name, replayAgenticStream(buffered)), nil
		}
		if failoverStopped(ctx, err) {
			m.release(name)
			return nil, err
		}
		m.fail(name)
		lastErr = err
		excluded[name] = struct{}{}
	}
	if lastErr != nil {
		m.emitFinalError(lastErr)
		return nil, lastErr
	}
	err := fmt.Errorf("任务 %q 无可用模型", m.task)
	m.emitFinalError(err)
	return nil, err
}

// bufferedStreamAttempt 一次可重试的流尝试：读完并缓冲整条流，中途错误交给 attemptRunner 分类重试
// 读流失败时丢弃本次尝试已缓冲的内容，避免调用方看到重放前的半截输出
func (m *failoverModel) bufferedStreamAttempt(
	ctx context.Context,
	name string,
	client model.AgenticModel,
	input []*schema.AgenticMessage,
	opts ...model.Option,
) ([]*schema.AgenticMessage, error) {
	var buffered []*schema.AgenticMessage
	err := m.runner(name, input, opts...).run(ctx, func(runCtx context.Context) error {
		reader, callErr := client.Stream(runCtx, input, opts...)
		if callErr != nil {
			return callErr
		}
		messages, readErr := drainAgenticStream(reader)
		if readErr != nil {
			return readErr
		}
		if len(messages) == 0 {
			// 空响应按 Python EmptyResponseException 语义可重试，不能当作成功
			return &EmptyResponseError{Message: "流式响应内容为空"}
		}
		buffered = messages
		return nil
	})
	if err != nil {
		return nil, err
	}
	return buffered, nil
}

// compressedInputForImageError 按 Python orchestrator 语义，在单次模型尝试因 413
// 或 data URI 单项超限失败后压缩图片并不消耗 retry 地重发一次；
// Chat Completions 客户端已内置该重试，避免重复压缩。
func (m *failoverModel) compressedInputForImageError(err error, input []*schema.AgenticMessage, client model.AgenticModel) ([]*schema.AgenticMessage, bool) {
	return imageCompressionRetryInput(err, input, client)
}

// runner 单个模型的重试与超时配置，并携带本次逻辑请求的快照来源
func (m *failoverModel) runner(name string, input []*schema.AgenticMessage, opts ...model.Option) attemptRunner {
	limits := m.attempts[name]
	runner := attemptRunner{Task: m.task, Model: name, Policy: limits.Policy, Timeout: limits.HardTimeout}
	if cfg, ok := m.configs[name]; ok {
		runner.Snapshot = responseAttemptSnapshot(cfg, input, opts)
	}
	return runner
}

func (m *failoverModel) emitFinalError(err error) {
	if err == nil {
		return
	}
	sessionID, requestType := "", string(m.task)
	for _, cfg := range m.configs {
		if id := strings.TrimSpace(cfg.SessionID); id != "" {
			sessionID = id
		}
		if rt := strings.TrimSpace(cfg.RequestType); rt != "" {
			requestType = rt
		}
		if sessionID != "" {
			break
		}
	}
	modelName := ""
	if len(m.models) > 0 {
		modelName = m.models[len(m.models)-1]
	}
	emitLLMErrorEvent(LLMErrorEvent{
		SessionID:   sessionID,
		TaskName:    string(m.task),
		RequestType: requestType,
		ModelName:   modelName,
		Message:     err.Error(),
	})
}

func (m *failoverModel) nextModel(excluded map[string]struct{}) (string, model.AgenticModel, bool) {
	usage := m.usage()
	name, ok := PickNextModel(ModelSelectInput{
		Task:     m.task,
		Models:   m.models,
		Strategy: m.strategy,
		Excluded: excluded,
		Score: func(candidate string) int {
			return usage[candidate].BalanceScore()
		},
	})
	if !ok {
		return "", nil, false
	}
	client := m.clients[name]
	if client == nil {
		excluded[name] = struct{}{}
		return m.nextModel(excluded)
	}
	return name, client, true
}

func (m *failoverModel) usage() map[string]ModelUsage {
	if m == nil || m.router == nil {
		return nil
	}
	return m.router.modelUsageSnapshot()
}

func (m *failoverModel) reserve(name string) {
	m.adjust(name, ModelUsage{UsagePenalty: 1})
}

func (m *failoverModel) release(name string) {
	m.adjust(name, ModelUsage{UsagePenalty: -1})
}

func (m *failoverModel) complete(name string, tokens int) {
	m.adjust(name, ModelUsage{TotalTokens: tokens, UsagePenalty: -1})
}

func (m *failoverModel) fail(name string) {
	m.adjust(name, ModelUsage{Penalty: 1, UsagePenalty: -1})
}

func (m *failoverModel) adjust(name string, delta ModelUsage) {
	if m == nil || m.router == nil || name == "" {
		return
	}
	m.router.adjustModelUsage(name, delta)
}

func agenticMessageTokens(message *schema.AgenticMessage) int {
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.TokenUsage == nil {
		return 0
	}
	return message.ResponseMeta.TokenUsage.TotalTokens
}

func failoverStopped(ctx context.Context, err error) bool {
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	var abortErr *AbortError
	return errors.As(err, &abortErr)
}
