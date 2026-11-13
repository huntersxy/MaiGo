package llm

// 请求级重试与任务级 hard_timeout
// 对齐 MaiBot utils_model.LLMOrchestrator._attempt_request_on_model / _attempt_request_on_model_with_timeout：
//   - 同一模型内按 api_providers.max_retry 次尝试，失败间隔 retry_interval
//   - 任务级 hard_timeout 覆盖单模型的全部重试；超时视为切换下一个模型，不再重试

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// LLMRetryEvent 对齐 Python _schedule_llm_retry_event 的广播载荷
type LLMRetryEvent struct {
	SessionID     string
	TaskName      string
	RequestType   string
	ModelName     string
	Attempt       int
	MaxAttempts   int
	Reason        string
	RetryInterval float64
}

// LLMErrorEvent 对齐 Python _schedule_llm_error_event 的广播载荷
type LLMErrorEvent struct {
	SessionID   string
	TaskName    string
	RequestType string
	ModelName   string
	Message     string
}

var (
	llmMonitorMu    sync.RWMutex
	llmRetryEmitter func(LLMRetryEvent)
	llmErrorEmitter func(LLMErrorEvent)
)

// SetLLMMonitorEmitters 注入监控广播实现；传 nil 关闭。bootstrap 接到 maisaka/monitor。
func SetLLMMonitorEmitters(retry func(LLMRetryEvent), failure func(LLMErrorEvent)) {
	llmMonitorMu.Lock()
	defer llmMonitorMu.Unlock()
	llmRetryEmitter = retry
	llmErrorEmitter = failure
}

func emitLLMRetryEvent(ev LLMRetryEvent) {
	if strings.TrimSpace(ev.SessionID) == "" {
		return
	}
	llmMonitorMu.RLock()
	emitter := llmRetryEmitter
	llmMonitorMu.RUnlock()
	if emitter == nil {
		return
	}
	emitter(ev)
}

func emitLLMErrorEvent(ev LLMErrorEvent) {
	if strings.TrimSpace(ev.SessionID) == "" {
		return
	}
	llmMonitorMu.RLock()
	emitter := llmErrorEmitter
	llmMonitorMu.RUnlock()
	if emitter == nil {
		return
	}
	emitter(ev)
}

const (
	// defaultProviderTimeout 对齐 MaiBot APIProvider.timeout 默认值（秒）
	defaultProviderTimeout = 120 * time.Second
	// defaultRetryInterval 对齐 MaiBot APIProvider.retry_interval 默认值（秒）
	defaultRetryInterval = 4 * time.Second
	// defaultProviderMaxRetry 对齐 MaiBot APIProvider.max_retry 默认值
	defaultProviderMaxRetry = 3
)

// RetryPolicy 单模型请求的重试策略
type RetryPolicy struct {
	// MaxAttempts 含首次请求在内的总尝试次数（MaiBot max_retry，0 或 1 表示不重试）
	MaxAttempts int
	// Interval 失败后的重试间隔
	Interval time.Duration
}

// providerMaxRetry 返回 APIProvider.max_retry 的有效值；零值且未显式配置时默认 3
func providerMaxRetry(cfg ModelConfig) int {
	if cfg.MaxRetrySet {
		return cfg.MaxRetry
	}
	if cfg.MaxRetry > 0 {
		return cfg.MaxRetry
	}
	return defaultProviderMaxRetry
}

// providerRetryAttempts 把 max_retry 转换成含首次请求在内的尝试次数；0/1 都只请求一次
func providerRetryAttempts(cfg ModelConfig) int {
	attempts := providerMaxRetry(cfg)
	if attempts < 1 {
		return 1
	}
	return attempts
}

// retryPolicyFromConfig 从模型配置解析重试策略
func retryPolicyFromConfig(cfg ModelConfig) RetryPolicy {
	attempts := providerRetryAttempts(cfg)
	interval := cfg.RetryInterval
	if interval <= 0 {
		interval = defaultRetryInterval
	}
	return RetryPolicy{MaxAttempts: attempts, Interval: interval}
}

// attemptRunner 给单模型请求套上重试与 hard_timeout
type attemptRunner struct {
	Task   TaskName
	Model  string
	Policy RetryPolicy
	// Timeout 任务级 hard_timeout，0 表示不限制
	Timeout time.Duration
	// Snapshot 本次逻辑请求的可诊断来源，供 hard_timeout 失败快照复用
	Snapshot *attemptSnapshotSource
}

// run 执行一次非流式请求：重试 + hard_timeout
func (r attemptRunner) run(ctx context.Context, call func(context.Context) error) error {
	runCtx := ctx
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	runCtx, recorder := ensureSnapshotAttemptRecorder(runCtx)
	// 每个逻辑轮次只生成一次 logical_turn_id，同一轮重试复用同一值
	logicalTurnID := newContextUUID()
	recorder.SetLogicalTurnID(logicalTurnID)
	return r.runWithRetry(runCtx, ctx, recorder, logicalTurnID, call)
}

func (r attemptRunner) runWithRetry(runCtx, parentCtx context.Context, recorder *snapshotAttemptRecorder, logicalTurnID string, call func(context.Context) error) error {
	var lastErr error
	for attempt := 0; attempt < r.Policy.MaxAttempts; attempt++ {
		if err := abortErrorFromContext(parentCtx); err != nil && parentCtx.Err() != nil {
			return err
		}
		// 重试间隔也可能耗尽 hard_timeout，此时不再发起下一次模型调用
		if timeoutErr := r.hardTimeoutError(parentCtx, runCtx); timeoutErr != nil {
			return r.withHardTimeoutSnapshot(recorder, logicalTurnID, timeoutErr)
		}
		lastErr = call(runCtx)
		if lastErr == nil {
			return nil
		}
		if timeoutErr := r.hardTimeoutError(parentCtx, runCtx); timeoutErr != nil {
			return r.withHardTimeoutSnapshot(recorder, logicalTurnID, timeoutErr)
		}
		if parentCtx != nil && parentCtx.Err() != nil {
			return &AbortError{Message: "请求被外部信号中断"}
		}
		action := classifyAttemptError(lastErr)
		if action == RetryActionAbort {
			return lastErr
		}
		if action != RetryActionRetry || attempt == r.Policy.MaxAttempts-1 {
			return lastErr
		}
		zap.L().Warn("模型请求失败，准备重试",
			zap.String("task", string(r.Task)),
			zap.String("model", r.Model),
			zap.Int("attempt", attempt+1),
			zap.Int("max_attempts", r.Policy.MaxAttempts),
			zap.Duration("retry_interval", r.Policy.Interval),
			zap.Error(lastErr))
		r.scheduleRetryEvent(attempt+1, lastErr)
		// 重试间隔同样受 hard_timeout 约束，对齐 Python wait_for 包住整个单模型重试循环
		if waitErr := sleepWithContext(runCtx, r.Policy.Interval); waitErr != nil {
			if timeoutErr := r.hardTimeoutError(parentCtx, runCtx); timeoutErr != nil {
				return r.withHardTimeoutSnapshot(recorder, logicalTurnID, timeoutErr)
			}
			if parentCtx != nil && parentCtx.Err() != nil {
				return &AbortError{Message: "请求被外部信号中断"}
			}
			return waitErr
		}
	}
	return lastErr
}

// withHardTimeoutSnapshot 为 hard_timeout 失败保存快照
// 对齐 MaiBot utils_model.py:1322-1333：保存 operation="task.hard_timeout" 的失败快照并挂到错误链
// 同时保留原始请求体（internal_request/context_items）与 provider_request.hard_timeout，
// 使 hard_timeout 快照可以直接重放；同一次逻辑请求已有快照时只追加事件
func (r attemptRunner) withHardTimeoutSnapshot(recorder *snapshotAttemptRecorder, logicalTurnID string, timeoutErr error) error {
	if timeoutErr == nil {
		return nil
	}
	// 跨模型 failover 时 recorder 只接受本轮首个 logical_turn_id，
	// 后续模型的局部值必须让位，否则快照事件与 request_items 的 turn id 不一致
	if recorded := recorder.LogicalTurnIDValue(); recorded != "" {
		logicalTurnID = recorded
	}
	spec := hardTimeoutSnapshotSpec(r, logicalTurnID, timeoutErr)
	// 记录本次 hard_timeout 尝试，事件里才能看到真实 operation
	recordSnapshotAttempt(recorder, &spec, spec.StartedAt)
	if wrapped := captureRequestSnapshot(spec); wrapped != nil {
		return wrapped
	}
	return timeoutErr
}

// hardTimeoutSnapshotSpec 把逻辑请求快照与 hard_timeout 标记合并成失败快照
func hardTimeoutSnapshotSpec(r attemptRunner, logicalTurnID string, timeoutErr error) requestSnapshotSpec {
	started := time.Now()
	spec := requestSnapshotSpec{
		Model:           r.Model,
		TaskName:        string(r.Task),
		Operation:       "task.hard_timeout",
		RequestKind:     requestKindResponse,
		ProviderRequest: map[string]any{"hard_timeout": r.Timeout.Seconds()},
		LogicalTurnID:   logicalTurnID,
		Error:           timeoutErr,
		StartedAt:       started,
	}
	if snapshot := r.Snapshot; snapshot != nil {
		spec.Config = snapshot.Config
		spec.ClientType = snapshot.Config.ClientType
		spec.ProviderName = snapshot.Config.ProviderName
		spec.BaseURL = snapshot.Config.BaseURL
		if strings.TrimSpace(snapshot.Config.Model) != "" {
			spec.Model = snapshot.Config.Model
		}
		spec.RequestKind = snapshot.RequestKind
		if spec.RequestKind == "" {
			spec.RequestKind = requestKindResponse
		}
		spec.ContextItems = snapshot.ContextItems
		spec.Input = snapshot.Input
		spec.Options = snapshot.Options
		spec.InternalRequest = cloneAnyMap(snapshot.InternalRequest)
		for key, value := range snapshot.ProviderRequest {
			spec.ProviderRequest[key] = value
		}
	}
	return spec
}

func (r attemptRunner) scheduleRetryEvent(attempt int, err error) {
	sessionID, requestType := r.monitorIdentity()
	reason := "模型请求失败"
	if err != nil {
		reason = err.Error()
	}
	emitLLMRetryEvent(LLMRetryEvent{
		SessionID:     sessionID,
		TaskName:      string(r.Task),
		RequestType:   requestType,
		ModelName:     r.Model,
		Attempt:       attempt,
		MaxAttempts:   r.Policy.MaxAttempts,
		Reason:        reason,
		RetryInterval: r.Policy.Interval.Seconds(),
	})
}

func (r attemptRunner) monitorIdentity() (sessionID, requestType string) {
	if r.Snapshot != nil {
		sessionID = strings.TrimSpace(r.Snapshot.Config.SessionID)
		requestType = strings.TrimSpace(r.Snapshot.Config.RequestType)
	}
	if requestType == "" {
		requestType = string(r.Task)
	}
	return sessionID, requestType
}

// hardTimeoutError 判断本次失败是否由任务级 hard_timeout 触发
func (r attemptRunner) hardTimeoutError(parentCtx, runCtx context.Context) error {
	if r.Timeout <= 0 || runCtx.Err() == nil {
		return nil
	}
	if parentCtx != nil && parentCtx.Err() != nil {
		// 调用方自己的取消/超时，按中断处理，不算任务级硬超时
		return nil
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return &TaskTimeoutError{Task: string(r.Task), Model: r.Model, Timeout: r.Timeout}
	}
	return nil
}

// sleepWithContext 可取消的等待，用于重试间隔
func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	if ctx == nil {
		<-timer.C
		return nil
	}
	select {
	case <-ctx.Done():
		if err := abortErrorFromContext(ctx); err != nil {
			return err
		}
		return &AbortError{Message: "请求被外部信号中断"}
	case <-timer.C:
		return nil
	}
}

// hardTimeoutForTask 任务级 hard_timeout
// 对齐 MaiBot model_config.toml 的 model_task_config.<task>.hard_timeout；
// 配置缺省时使用 MaiBot default_model_config 中的默认值
func hardTimeoutForTask(task TaskName, cfgSeconds float64) time.Duration {
	if cfgSeconds > 0 {
		return time.Duration(cfgSeconds * float64(time.Second))
	}
	if seconds, ok := defaultTaskHardTimeout[task]; ok {
		return time.Duration(seconds * float64(time.Second))
	}
	return 0
}

// defaultTaskHardTimeout 对齐 MaiBot src/config/default_model_config.py
var defaultTaskHardTimeout = map[TaskName]float64{
	TaskUtils:          120,
	TaskMemory:         240,
	TaskReplyer:        240,
	TaskPlanner:        180,
	TaskLearner:        120,
	TaskFastModel:      120,
	TaskEmoji:          120,
	TaskVLM:            240,
	TaskVoice:          120,
	TaskEmbedding:      60,
	TaskImageEmbedding: 60,
}
