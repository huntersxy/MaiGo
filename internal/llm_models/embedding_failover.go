package llm

// 嵌入任务的候选模型失败切换
// 对齐 MaiBot LLMOrchestrator._execute_request 的嵌入分支：
//   - 嵌入任务强制 sequential，按 model_list 顺序尝试
//   - 单个模型内部由 Provider.max_retry 重试，任务级 hard_timeout 覆盖该模型的全部重试
//   - 单模型失败后切换下一个候选；成功累计 token 并回滚 usage_penalty，失败累计 penalty
//   - 外部取消/中断不累计失败，立即返回
//
// Python 全部失败时抛最后一个异常；Go 额外聚合各候选错误便于诊断，单候选时保留原错误。

import (
	"context"
	"fmt"
	"strings"
)

// embeddingRequestMetadata 门面注入的调用链元数据
type embeddingRequestMetadata struct {
	taskName    TaskName
	requestType string
	sessionID   string
}

func applyEmbeddingRequestMetadata(cfg *ModelConfig, meta embeddingRequestMetadata) {
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

// embeddingCandidate 一个可用的嵌入模型客户端
type embeddingCandidate struct {
	name   string
	route  ResolvedRoute
	client EmbeddingClient
}

// embeddingFailoverClient 在嵌入任务的多候选模型间做请求级切换
type embeddingFailoverClient struct {
	task       TaskName
	router     *Router
	candidates []embeddingCandidate
}

func newEmbeddingFailoverClient(task TaskName, router *Router, candidates []embeddingCandidate) EmbeddingClient {
	if len(candidates) == 1 && router == nil {
		return candidates[0].client
	}
	return &embeddingFailoverClient{task: task, router: router, candidates: candidates}
}

func (c *embeddingFailoverClient) EmbedText(ctx context.Context, text string) (EmbeddingResult, error) {
	if c == nil {
		return EmbeddingResult{}, fmt.Errorf("嵌入客户端未初始化")
	}
	return c.run(ctx, func(client EmbeddingClient) (EmbeddingResult, error) {
		return client.EmbedText(ctx, text)
	})
}

func (c *embeddingFailoverClient) EmbedTexts(ctx context.Context, texts []string) ([]EmbeddingResult, error) {
	if len(texts) == 0 {
		return []EmbeddingResult{}, nil
	}
	out := make([]EmbeddingResult, 0, len(texts))
	for _, text := range texts {
		row, err := c.EmbedText(ctx, text)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

func (c *embeddingFailoverClient) EmbedTextsConcurrent(ctx context.Context, texts []string, maxConcurrent int) ([]EmbeddingResult, error) {
	if len(texts) == 0 {
		return []EmbeddingResult{}, nil
	}
	if maxConcurrent <= 1 {
		return c.EmbedTexts(ctx, texts)
	}
	return embedTextsConcurrently(ctx, texts, maxConcurrent, c.EmbedText)
}

func (c *embeddingFailoverClient) EmbedImage(ctx context.Context, input ImageEmbeddingInput) (EmbeddingResult, error) {
	if c == nil {
		return EmbeddingResult{}, fmt.Errorf("嵌入客户端未初始化")
	}
	return c.run(ctx, func(client EmbeddingClient) (EmbeddingResult, error) {
		return client.EmbedImage(ctx, input)
	})
}

// run 依次尝试候选；单候选失败保留底层错误，多候选失败聚合
func (c *embeddingFailoverClient) run(ctx context.Context, call func(EmbeddingClient) (EmbeddingResult, error)) (EmbeddingResult, error) {
	if c == nil {
		return EmbeddingResult{}, fmt.Errorf("嵌入客户端未初始化")
	}
	if len(c.candidates) == 0 {
		return EmbeddingResult{}, fmt.Errorf("任务 %q 无可用嵌入模型", c.task)
	}
	var failures []embeddingAttemptFailure
	for _, candidate := range c.candidates {
		c.reserve(candidate.name)
		result, err := call(candidate.client)
		if err == nil {
			c.complete(candidate.name, embeddingResultTokens(result))
			return result, nil
		}
		if failoverStopped(ctx, err) {
			c.release(candidate.name)
			return EmbeddingResult{}, err
		}
		c.fail(candidate.name)
		failures = append(failures, embeddingAttemptFailure{Model: candidate.name, Err: err})
	}
	if len(failures) == 1 {
		return EmbeddingResult{}, failures[0].Err
	}
	return EmbeddingResult{}, &embeddingFailoverError{Task: c.task, Failures: failures}
}

func embeddingResultTokens(result EmbeddingResult) int {
	if result.Usage == nil {
		return 0
	}
	return result.Usage.TotalTokens
}

func (c *embeddingFailoverClient) reserve(name string) {
	c.adjust(name, ModelUsage{UsagePenalty: 1})
}

func (c *embeddingFailoverClient) release(name string) {
	c.adjust(name, ModelUsage{UsagePenalty: -1})
}

func (c *embeddingFailoverClient) complete(name string, tokens int) {
	c.adjust(name, ModelUsage{TotalTokens: tokens, UsagePenalty: -1})
}

func (c *embeddingFailoverClient) fail(name string) {
	c.adjust(name, ModelUsage{Penalty: 1, UsagePenalty: -1})
}

func (c *embeddingFailoverClient) adjust(name string, delta ModelUsage) {
	if c == nil || c.router == nil || name == "" {
		return
	}
	c.router.adjustModelUsage(name, delta)
}

// embeddingAttemptFailure 单个候选的错误
type embeddingAttemptFailure struct {
	Model string
	Err   error
}

// embeddingFailoverError 全部候选失败时的聚合错误；Unwrap 支持 errors.Is/As
type embeddingFailoverError struct {
	Task     TaskName
	Failures []embeddingAttemptFailure
}

func (e *embeddingFailoverError) Error() string {
	if e == nil {
		return "嵌入请求失败"
	}
	parts := make([]string, 0, len(e.Failures))
	for _, failure := range e.Failures {
		parts = append(parts, fmt.Sprintf("%s: %v", failure.Model, failure.Err))
	}
	return fmt.Sprintf("任务 %q 全部候选嵌入模型失败: %s", e.Task, strings.Join(parts, "; "))
}

func (e *embeddingFailoverError) Unwrap() []error {
	if e == nil {
		return nil
	}
	out := make([]error, 0, len(e.Failures))
	for _, failure := range e.Failures {
		out = append(out, failure.Err)
	}
	return out
}

// newEmbeddingClientForTask 解析候选并构造失败切换客户端
func (r *Router) newEmbeddingClientForTask(ctx context.Context, task TaskName, meta embeddingRequestMetadata) (ResolvedRoute, EmbeddingClient, error) {
	if task != TaskEmbedding && task != TaskImageEmbedding {
		return ResolvedRoute{}, nil, fmt.Errorf("任务 %q 不是嵌入任务", task)
	}
	if r == nil || r.profiles == nil {
		return ResolvedRoute{}, nil, fmt.Errorf("LLM 路由器未初始化")
	}
	candidates, initial, err := r.resolveEmbeddingCandidates(ctx, task, meta)
	if err != nil {
		return ResolvedRoute{}, nil, err
	}
	return initial, newEmbeddingFailoverClient(task, r, candidates), nil
}

// embeddingSnapshotKind 任务 → 快照 request_kind
func embeddingSnapshotKind(task TaskName) string {
	if task == TaskImageEmbedding {
		return requestKindImageEmbedding
	}
	return requestKindEmbedding
}

func (r *Router) resolveEmbeddingCandidates(ctx context.Context, task TaskName, meta embeddingRequestMetadata) ([]embeddingCandidate, ResolvedRoute, error) {
	single := func() ([]embeddingCandidate, ResolvedRoute, error) {
		resolved, err := r.Resolve(ctx, task)
		if err != nil {
			return nil, ResolvedRoute{}, err
		}
		applyEmbeddingRequestMetadata(&resolved.Config, meta)
		client, err := NewEmbeddingClient(resolved.Config)
		if err != nil {
			return nil, ResolvedRoute{}, err
		}
		timeout := hardTimeoutForTask(task, resolved.TaskConfig.HardTimeout)
		client = withEmbeddingTaskTimeout(task, resolved, embeddingSnapshotKind(task), timeout, client)
		candidate := embeddingCandidate{name: usageModelName(resolved), route: resolved, client: client}
		return []embeddingCandidate{candidate}, resolved, nil
	}

	if r.bindings[task] != "" || r.file == nil {
		return single()
	}
	taskRes, models, err := TaskModelList(*r.file, task)
	if err != nil || len(models) == 0 {
		// 任务未配置 model_list 时回退到 Resolve 的档案路由
		return single()
	}
	candidates := make([]embeddingCandidate, 0, len(models))
	var lastErr error
	for _, modelName := range models {
		resolved, err := r.resolveMaiBotModel(ctx, task, taskRes.Effective, taskRes.Config, modelName)
		if err != nil {
			lastErr = err
			continue
		}
		applyEmbeddingRequestMetadata(&resolved.Config, meta)
		client, err := NewEmbeddingClient(resolved.Config)
		if err != nil {
			lastErr = err
			continue
		}
		timeout := hardTimeoutForTask(task, resolved.TaskConfig.HardTimeout)
		client = withEmbeddingTaskTimeout(task, resolved, embeddingSnapshotKind(task), timeout, client)
		candidates = append(candidates, embeddingCandidate{
			name:   usageModelName(resolved),
			route:  resolved,
			client: client,
		})
	}
	if len(candidates) == 0 {
		if lastErr != nil {
			return nil, ResolvedRoute{}, fmt.Errorf("任务 %q 全部候选嵌入模型创建客户端失败: %w", task, lastErr)
		}
		return single()
	}
	return candidates, candidates[0].route, nil
}

// 接口实现检查
var (
	_ EmbeddingClient           = (*embeddingFailoverClient)(nil)
	_ ConcurrentEmbeddingClient = (*embeddingFailoverClient)(nil)
)
