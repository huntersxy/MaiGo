package llm

// 语音转写的任务级路由与失败切换
// 对齐 MaiBot LLMOrchestrator._execute_request 的 audio 分支：
//   - 按任务 model_list 顺序尝试候选，单模型内由 Provider.max_retry 重试
//   - 任务级 hard_timeout 覆盖单个候选的全部重试，超时后切换下一个候选
//   - 成功累计 token 并回滚 usage_penalty，失败累计 penalty
//   - 外部取消/中断不累计失败，立即返回
//
// Python 全部失败时抛最后一个异常；Go 多候选时聚合各候选错误便于诊断。

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// audioRequestMetadata 门面注入的调用链元数据
type audioRequestMetadata struct {
	taskName    TaskName
	requestType string
	sessionID   string
}

func applyAudioRequestMetadata(cfg *ModelConfig, meta audioRequestMetadata) {
	if cfg == nil {
		return
	}
	if meta.taskName != "" {
		cfg.TaskName = string(meta.taskName)
	}
	if meta.requestType != "" {
		cfg.RequestType = meta.requestType
	}
	if meta.sessionID != "" {
		cfg.SessionID = meta.sessionID
	}
}

// audioCandidate 一个可用的语音转写模型客户端
type audioCandidate struct {
	name   string
	route  ResolvedRoute
	client AudioTranscriber
}

// NewAudioTranscriberForTask 按任务 model_list 创建语音转写客户端
// 多候选时返回请求级 failover 包装器；单候选也由 failover 统一累计 token/penalty
func (r *Router) NewAudioTranscriberForTask(ctx context.Context, task TaskName) (ResolvedRoute, AudioTranscriber, error) {
	return r.newAudioTranscriberForTask(ctx, task, audioRequestMetadata{
		taskName:    task,
		requestType: string(task),
	})
}

// TranscribeAudioForTask 按任务路由并执行一次语音转写
// 返回值 route 是初始候选；实际成功的候选信息见 AudioTranscriptionResult.ModelName 等字段
func (r *Router) TranscribeAudioForTask(ctx context.Context, task TaskName, requestType, sessionID string, req AudioTranscriptionRequest) (ResolvedRoute, AudioTranscriptionResult, error) {
	route, transcriber, err := r.newAudioTranscriberForTask(ctx, task, audioRequestMetadata{
		taskName:    task,
		requestType: requestType,
		sessionID:   sessionID,
	})
	if err != nil {
		return ResolvedRoute{}, AudioTranscriptionResult{}, err
	}
	result, err := transcriber.Transcribe(ctx, req)
	if err != nil {
		return route, AudioTranscriptionResult{}, err
	}
	return route, result, nil
}

func (r *Router) newAudioTranscriberForTask(ctx context.Context, task TaskName, meta audioRequestMetadata) (ResolvedRoute, AudioTranscriber, error) {
	if r == nil || r.profiles == nil {
		return ResolvedRoute{}, nil, fmt.Errorf("LLM 路由器未初始化")
	}
	task, err := NormalizeTaskName(string(task))
	if err != nil {
		return ResolvedRoute{}, nil, err
	}
	if task == TaskWebSearch {
		return ResolvedRoute{}, nil, fmt.Errorf("任务 %q 不支持语音转写", task)
	}
	candidates, initial, err := r.resolveAudioCandidates(ctx, task, meta)
	if err != nil {
		return ResolvedRoute{}, nil, err
	}
	return initial, newAudioFailoverClient(task, r, candidates), nil
}

func (r *Router) resolveAudioCandidates(ctx context.Context, task TaskName, meta audioRequestMetadata) ([]audioCandidate, ResolvedRoute, error) {
	single := func() ([]audioCandidate, ResolvedRoute, error) {
		resolved, err := r.Resolve(ctx, task)
		if err != nil {
			return nil, ResolvedRoute{}, err
		}
		applyAudioRequestMetadata(&resolved.Config, meta)
		client, err := NewAudioTranscriber(resolved.Config)
		if err != nil {
			return nil, ResolvedRoute{}, err
		}
		candidate := audioCandidate{
			name:   usageModelName(resolved),
			route:  resolved,
			client: withAudioAttempt(task, usageModelName(resolved), resolved, client),
		}
		return []audioCandidate{candidate}, resolved, nil
	}

	if r.bindings[task] != "" || r.file == nil {
		return single()
	}
	taskRes, models, err := TaskModelList(*r.file, task)
	if err != nil || len(models) == 0 {
		// 任务未配置 model_list 时回退到 Resolve 的档案路由
		return single()
	}
	candidates := make([]audioCandidate, 0, len(models))
	var lastErr error
	for _, modelName := range models {
		resolved, err := r.resolveMaiBotModel(ctx, task, taskRes.Effective, taskRes.Config, modelName)
		if err != nil {
			lastErr = err
			continue
		}
		applyAudioRequestMetadata(&resolved.Config, meta)
		client, err := NewAudioTranscriber(resolved.Config)
		if err != nil {
			lastErr = err
			continue
		}
		candidates = append(candidates, audioCandidate{
			name:   usageModelName(resolved),
			route:  resolved,
			client: withAudioAttempt(task, usageModelName(resolved), resolved, client),
		})
	}
	if len(candidates) == 0 {
		if lastErr != nil {
			return nil, ResolvedRoute{}, fmt.Errorf("任务 %q 全部候选语音模型创建客户端失败: %w", task, lastErr)
		}
		return single()
	}
	strategy := NormalizeSelectionStrategy(task, taskRes.Config.SelectionStrategy)
	candidates = orderAudioCandidates(candidates, strategy, r.modelUsageSnapshot())
	return candidates, candidates[0].route, nil
}

// orderAudioCandidates 按任务选择策略决定尝试顺序
// Python 每次切换模型时用当前 usage 重新选择；失败只增加被尝试模型的惩罚，
// 其余候选的相对分数不变，因此这里按创建时分数稳定排序即可等价。
func orderAudioCandidates(candidates []audioCandidate, strategy SelectionStrategy, usage map[string]ModelUsage) []audioCandidate {
	if len(candidates) == 0 {
		return candidates
	}
	if strategy == StrategyFirst {
		return candidates[:1]
	}
	if len(candidates) < 2 {
		return candidates
	}
	switch strategy {
	case StrategyRandom:
		rand.Shuffle(len(candidates), func(i, j int) {
			candidates[i], candidates[j] = candidates[j], candidates[i]
		})
	case StrategyBalance:
		sort.SliceStable(candidates, func(i, j int) bool {
			return usage[candidates[i].name].BalanceScore() < usage[candidates[j].name].BalanceScore()
		})
	}
	return candidates
}

// audioAttemptTranscriber 单模型重试与任务级 hard_timeout
type audioAttemptTranscriber struct {
	task   TaskName
	model  string
	cfg    ModelConfig
	policy RetryPolicy
	// timeout 任务级 hard_timeout，0 表示不限制
	timeout time.Duration
	inner   AudioTranscriber
}

func withAudioAttempt(task TaskName, model string, route ResolvedRoute, inner AudioTranscriber) AudioTranscriber {
	if inner == nil {
		return nil
	}
	timeout := hardTimeoutForTask(task, route.TaskConfig.HardTimeout)
	return &audioAttemptTranscriber{
		task:    task,
		model:   model,
		cfg:     route.Config,
		policy:  retryPolicyFromConfig(route.Config),
		timeout: timeout,
		inner:   inner,
	}
}

func (t *audioAttemptTranscriber) Transcribe(ctx context.Context, req AudioTranscriptionRequest) (AudioTranscriptionResult, error) {
	if t == nil || t.inner == nil {
		return AudioTranscriptionResult{}, errors.New("语音转写客户端未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runner := attemptRunner{
		Task:     t.task,
		Model:    t.model,
		Policy:   t.policy,
		Timeout:  t.timeout,
		Snapshot: audioAttemptSnapshot(t.cfg, req),
	}
	var result AudioTranscriptionResult
	err := runner.run(ctx, func(runCtx context.Context) error {
		var callErr error
		result, callErr = t.inner.Transcribe(runCtx, req)
		return callErr
	})
	if err != nil {
		return AudioTranscriptionResult{}, err
	}
	return result, nil
}

// audioFailoverClient 在语音转写的多候选模型间做请求级切换
type audioFailoverClient struct {
	task       TaskName
	router     *Router
	candidates []audioCandidate
}

func newAudioFailoverClient(task TaskName, router *Router, candidates []audioCandidate) AudioTranscriber {
	if len(candidates) == 1 && router == nil {
		return candidates[0].client
	}
	return &audioFailoverClient{task: task, router: router, candidates: candidates}
}

func (c *audioFailoverClient) Transcribe(ctx context.Context, req AudioTranscriptionRequest) (AudioTranscriptionResult, error) {
	if c == nil {
		return AudioTranscriptionResult{}, fmt.Errorf("语音转写客户端未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(c.candidates) == 0 {
		return AudioTranscriptionResult{}, fmt.Errorf("任务 %q 无可用语音模型", c.task)
	}
	// 同一次语音请求共享尝试记录器，失败快照按候选尝试聚合
	ctx, _ = ensureSnapshotAttemptRecorder(ctx)
	var failures []audioAttemptFailure
	for _, candidate := range c.candidates {
		c.reserve(candidate.name)
		result, err := candidate.client.Transcribe(ctx, req)
		if err == nil {
			c.complete(candidate.name, audioResultTokens(result))
			fillAudioResultRoute(&result, candidate.route)
			return result, nil
		}
		if failoverStopped(ctx, err) {
			c.release(candidate.name)
			return AudioTranscriptionResult{}, err
		}
		c.fail(candidate.name)
		failures = append(failures, audioAttemptFailure{Model: candidate.name, Err: err})
	}
	if len(failures) == 1 {
		return AudioTranscriptionResult{}, failures[0].Err
	}
	return AudioTranscriptionResult{}, &audioFailoverError{Task: c.task, Failures: failures}
}

func audioResultTokens(result AudioTranscriptionResult) int {
	if result.Usage == nil {
		return 0
	}
	return result.Usage.TotalTokens
}

func fillAudioResultRoute(result *AudioTranscriptionResult, route ResolvedRoute) {
	if result == nil {
		return
	}
	result.ModelName = usageModelName(route)
	result.ModelIdentifier = strings.TrimSpace(route.Config.Model)
	result.APIProvider = strings.TrimSpace(route.Config.ProviderName)
}

func (c *audioFailoverClient) reserve(name string) {
	c.adjust(name, ModelUsage{UsagePenalty: 1})
}

func (c *audioFailoverClient) release(name string) {
	c.adjust(name, ModelUsage{UsagePenalty: -1})
}

func (c *audioFailoverClient) complete(name string, tokens int) {
	c.adjust(name, ModelUsage{TotalTokens: tokens, UsagePenalty: -1})
}

func (c *audioFailoverClient) fail(name string) {
	c.adjust(name, ModelUsage{Penalty: 1, UsagePenalty: -1})
}

func (c *audioFailoverClient) adjust(name string, delta ModelUsage) {
	if c == nil || c.router == nil || name == "" {
		return
	}
	c.router.adjustModelUsage(name, delta)
}

// audioAttemptFailure 单个候选的错误
type audioAttemptFailure struct {
	Model string
	Err   error
}

// audioFailoverError 全部候选失败时的聚合错误；Unwrap 支持 errors.Is/As
type audioFailoverError struct {
	Task     TaskName
	Failures []audioAttemptFailure
}

func (e *audioFailoverError) Error() string {
	if e == nil {
		return "语音转写失败"
	}
	parts := make([]string, 0, len(e.Failures))
	for _, failure := range e.Failures {
		parts = append(parts, fmt.Sprintf("%s: %v", failure.Model, failure.Err))
	}
	return fmt.Sprintf("任务 %q 全部候选语音模型失败: %s", e.Task, strings.Join(parts, "; "))
}

func (e *audioFailoverError) Unwrap() []error {
	if e == nil {
		return nil
	}
	out := make([]error, 0, len(e.Failures))
	for _, failure := range e.Failures {
		out = append(out, failure.Err)
	}
	return out
}

var _ AudioTranscriber = (*audioAttemptTranscriber)(nil)
var _ AudioTranscriber = (*audioFailoverClient)(nil)
