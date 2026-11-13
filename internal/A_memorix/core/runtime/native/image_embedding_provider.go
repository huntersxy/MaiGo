package native

// 图片嵌入客户端延迟提供者，供宿主在模型路由就绪后注入
// 原生内核启动早于 Agent，图片运行时只登记提供者，不在构造阶段伪造客户端

import (
	"context"
	"errors"
	"sync"

	"maibot-go/internal/llm_models"
)

var (
	imageEmbeddingProviderMu sync.RWMutex
	imageEmbeddingProvider   func(ctx context.Context) (llm.EmbeddingClient, error)
)

// SetImageEmbeddingClientProvider 注册图片嵌入客户端提供者
func SetImageEmbeddingClientProvider(provider func(ctx context.Context) (llm.EmbeddingClient, error)) {
	imageEmbeddingProviderMu.Lock()
	imageEmbeddingProvider = provider
	imageEmbeddingProviderMu.Unlock()
}

// currentImageEmbeddingClient 解析当前图片嵌入客户端
func currentImageEmbeddingClient(ctx context.Context) (llm.EmbeddingClient, error) {
	imageEmbeddingProviderMu.RLock()
	provider := imageEmbeddingProvider
	imageEmbeddingProviderMu.RUnlock()
	if provider == nil {
		return nil, errors.New("图片嵌入客户端尚未接线，等待宿主模型路由就绪")
	}
	return provider(ctx)
}
