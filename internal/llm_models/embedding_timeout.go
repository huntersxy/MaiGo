package llm

// 嵌入任务级 hard_timeout
// MaiBot 的 LLMOrchestrator.get_embedding 与生成任务共用 _attempt_request_on_model_with_timeout，
// 单次模型尝试受 model_task_config.<task>.hard_timeout 约束（默认 embedding/image_embedding 60s）。
// Provider 的 HTTP timeout 覆盖不了内部重试与限速等待，因此在外层补任务级超时，
// 并把超时映射为 TaskTimeoutError（对齐 LLMTaskTimeoutError）。

import (
	"context"
	"errors"
	"time"
)

// embeddingTimeoutClient 给嵌入客户端附加任务级 hard_timeout
type embeddingTimeoutClient struct {
	task    TaskName
	model   string
	cfg     ModelConfig
	kind    string
	timeout time.Duration
	inner   EmbeddingClient
}

// withEmbeddingTaskTimeout 包装任务级超时；timeout<=0 时保持原客户端
// 每个 EmbedText 调用独立计时，批量串行时不会把整批共用同一个 60s 预算
func withEmbeddingTaskTimeout(task TaskName, resolved ResolvedRoute, kind string, timeout time.Duration, inner EmbeddingClient) EmbeddingClient {
	if inner == nil || timeout <= 0 {
		return inner
	}
	return &embeddingTimeoutClient{
		task:    task,
		model:   usageModelName(resolved),
		cfg:     resolved.Config,
		kind:    kind,
		timeout: timeout,
		inner:   inner,
	}
}

func (c *embeddingTimeoutClient) EmbedText(ctx context.Context, text string) (EmbeddingResult, error) {
	if c == nil || c.inner == nil {
		return EmbeddingResult{}, errors.New("嵌入客户端未初始化")
	}
	runCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	result, err := c.inner.EmbedText(runCtx, text)
	return result, c.snapshotTimeout(text, nil, c.translate(ctx, runCtx, err))
}

// EmbedTexts 逐条调用 EmbedText，保证每条输入各自拥有完整 hard_timeout
// 现有 OpenAI/Gemini 客户端本身也是串行逐条请求，因此不改变请求顺序与失败即停语义
func (c *embeddingTimeoutClient) EmbedTexts(ctx context.Context, texts []string) ([]EmbeddingResult, error) {
	if len(texts) == 0 {
		// 交给底层保持与 Python embed_texts 空输入返回空列表 / 客户端空列表报错的既有语义
		return c.inner.EmbedTexts(ctx, texts)
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

func (c *embeddingTimeoutClient) EmbedTextsConcurrent(ctx context.Context, texts []string, maxConcurrent int) ([]EmbeddingResult, error) {
	if maxConcurrent <= 1 {
		return c.EmbedTexts(ctx, texts)
	}
	if len(texts) == 0 {
		return c.inner.EmbedTexts(ctx, texts)
	}
	return embedTextsConcurrently(ctx, texts, maxConcurrent, c.EmbedText)
}

func (c *embeddingTimeoutClient) EmbedImage(ctx context.Context, input ImageEmbeddingInput) (EmbeddingResult, error) {
	if c == nil || c.inner == nil {
		return EmbeddingResult{}, errors.New("嵌入客户端未初始化")
	}
	runCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	result, err := c.inner.EmbedImage(runCtx, input)
	return result, c.snapshotTimeout("", &input, c.translate(ctx, runCtx, err))
}

// translate 只把本层计时器触发的超时转换成 TaskTimeoutError
// 调用方 context 自身取消/超时仍保留原错误，避免把外部中断误报成模型硬超时
func (c *embeddingTimeoutClient) translate(parent context.Context, runCtx context.Context, err error) error {
	if parent.Err() != nil {
		if err != nil {
			return err
		}
		return parent.Err()
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return &TaskTimeoutError{Task: string(c.task), Model: c.model, Timeout: c.timeout}
	}
	return err
}

// snapshotTimeout 为任务级 hard_timeout 保存可重放的请求快照
func (c *embeddingTimeoutClient) snapshotTimeout(text string, input *ImageEmbeddingInput, err error) error {
	var timeoutErr *TaskTimeoutError
	if !errors.As(err, &timeoutErr) {
		return err
	}
	var snapshot *attemptSnapshotSource
	if input != nil {
		snapshot = imageEmbeddingAttemptSnapshot(c.cfg, *input)
	} else {
		snapshot = embeddingAttemptSnapshot(c.cfg, text)
	}
	runner := attemptRunner{Task: c.task, Model: c.model, Timeout: c.timeout, Snapshot: snapshot}
	spec := hardTimeoutSnapshotSpec(runner, newContextUUID(), err)
	if wrapped := captureRequestSnapshot(spec); wrapped != nil {
		return wrapped
	}
	return err
}

// 接口实现检查
var _ EmbeddingClient = (*embeddingTimeoutClient)(nil)
var _ ConcurrentEmbeddingClient = (*embeddingTimeoutClient)(nil)
