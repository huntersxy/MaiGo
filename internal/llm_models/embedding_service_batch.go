package llm

// 嵌入批量的 return_exceptions 语义
// 对齐 MaiBot EmbeddingService.embed_texts(return_exceptions=True)：
//   - 按输入顺序返回每一项的结果或异常
//   - 单项失败不取消其他项，也不提前结束
//   - max_concurrent<=1 时仍串行，但继续处理后续输入
// Go 用 EmbeddingTextResult.Err 表示对应位置的异常，避免把异常塞进 EmbeddingResult。

import (
	"context"
	"sync"
)

// EmbeddingTextResult 批量嵌入中单项的结果或错误
type EmbeddingTextResult struct {
	Result EmbeddingResult
	Err    error
}

// EmbedTextsWithExceptions 生成文本嵌入并保留逐项错误；结果顺序与输入一致
func (c *EmbeddingServiceClient) EmbedTextsWithExceptions(ctx context.Context, texts []string, maxConcurrent int) ([]EmbeddingTextResult, error) {
	if len(texts) == 0 {
		return []EmbeddingTextResult{}, nil
	}
	client, err := c.clientForTask(ctx, c.taskName)
	if err != nil {
		return nil, err
	}
	if maxConcurrent <= 1 {
		return collectEmbeddingTextResults(ctx, texts, 1, client.EmbedText), nil
	}
	return collectEmbeddingTextResults(ctx, texts, maxConcurrent, client.EmbedText), nil
}

// collectEmbeddingTextResults 并发执行逐条嵌入，任一项失败只记录到对应位置
func collectEmbeddingTextResults(
	ctx context.Context,
	texts []string,
	maxConcurrent int,
	embed func(context.Context, string) (EmbeddingResult, error),
) []EmbeddingTextResult {
	out := make([]EmbeddingTextResult, len(texts))
	if maxConcurrent > len(texts) {
		maxConcurrent = len(texts)
	}
	if maxConcurrent <= 1 {
		for index, text := range texts {
			result, err := embed(ctx, text)
			out[index] = EmbeddingTextResult{Result: result, Err: err}
		}
		return out
	}
	sem := make(chan struct{}, maxConcurrent)
	var wait sync.WaitGroup
	for index, text := range texts {
		wait.Add(1)
		go func(index int, text string) {
			defer wait.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				out[index] = EmbeddingTextResult{Err: ctx.Err()}
				return
			}
			defer func() { <-sem }()
			result, err := embed(ctx, text)
			out[index] = EmbeddingTextResult{Result: result, Err: err}
		}(index, text)
	}
	wait.Wait()
	return out
}
