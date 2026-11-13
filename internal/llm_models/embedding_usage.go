package llm

// 嵌入客户端的用量与惩罚统计
// MaiBot 的 embedding 调用也经过 usage_model 记录 provider 用量。本包装用与
// 生成任务相同的 Router.adjustModelUsage 语义，把嵌入 token 与失败惩罚计入
// Router 的模型使用统计，不改变 EmbeddingClient 接口。

import "context"

// embeddingUsageClient 给嵌入客户端附加模型用量统计
type embeddingUsageClient struct {
	router *Router
	model  string
	inner  EmbeddingClient
}

func withEmbeddingUsageTracking(router *Router, modelName string, inner EmbeddingClient) EmbeddingClient {
	if router == nil || modelName == "" || inner == nil {
		return inner
	}
	return &embeddingUsageClient{router: router, model: modelName, inner: inner}
}

func (c *embeddingUsageClient) EmbedText(ctx context.Context, text string) (EmbeddingResult, error) {
	result, err := c.inner.EmbedText(ctx, text)
	c.track(ctx, []EmbeddingResult{result}, err)
	return result, err
}

func (c *embeddingUsageClient) EmbedTexts(ctx context.Context, texts []string) ([]EmbeddingResult, error) {
	results, err := c.inner.EmbedTexts(ctx, texts)
	c.track(ctx, results, err)
	return results, err
}

func (c *embeddingUsageClient) EmbedTextsConcurrent(ctx context.Context, texts []string, maxConcurrent int) ([]EmbeddingResult, error) {
	client, ok := c.inner.(ConcurrentEmbeddingClient)
	if !ok {
		// 底层不支持并发时退回串行接口，保持可选能力的语义
		results, err := c.inner.EmbedTexts(ctx, texts)
		c.track(ctx, results, err)
		return results, err
	}
	results, err := client.EmbedTextsConcurrent(ctx, texts, maxConcurrent)
	c.track(ctx, results, err)
	return results, err
}

func (c *embeddingUsageClient) EmbedImage(ctx context.Context, input ImageEmbeddingInput) (EmbeddingResult, error) {
	result, err := c.inner.EmbedImage(ctx, input)
	c.track(ctx, []EmbeddingResult{result}, err)
	return result, err
}

func (c *embeddingUsageClient) track(ctx context.Context, results []EmbeddingResult, err error) {
	if c == nil || c.router == nil || c.model == "" {
		return
	}
	if err != nil {
		if !failoverStopped(ctx, err) {
			c.router.adjustModelUsage(c.model, ModelUsage{Penalty: 1})
		}
		return
	}
	tokens := 0
	for _, result := range results {
		if result.Usage != nil {
			tokens += result.Usage.TotalTokens
		}
	}
	if tokens > 0 {
		c.router.adjustModelUsage(c.model, ModelUsage{TotalTokens: tokens})
	}
}
