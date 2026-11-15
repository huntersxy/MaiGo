package runtime

import (
	"context"
	"errors"

	"MaiGo/internal/A_memorix/core/runtime/native"
	"MaiGo/internal/llm_models"
)

// wireNativeImageEmbedding 把模型路由交给原生图片记忆运行时；provider 每次按当前路由解析
func (a *Agent) wireNativeImageEmbedding() {
	native.SetImageEmbeddingClientProvider(func(ctx context.Context) (llm.EmbeddingClient, error) {
		router := a.llmRouterSnapshot()
		if router == nil {
			return nil, errors.New("模型路由未就绪")
		}
		_, client, err := router.NewEmbeddingClientForTask(ctx, llm.TaskImageEmbedding)
		if err != nil {
			return nil, err
		}
		return client, nil
	})
}
