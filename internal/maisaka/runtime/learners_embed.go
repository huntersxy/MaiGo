package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"MaiGo/internal/learners"
	"MaiGo/internal/llm_models"

	"go.uber.org/zap"
)

var errLearnersEmbedderUnavailable = errors.New("learners: 嵌入客户端不可用")

// float32Vector 把 API 层 float64 向量降为表达向量索引使用的 float32
func float32Vector(values []float64) []float32 {
	out := make([]float32, len(values))
	for index, value := range values {
		out[index] = float32(value)
	}
	return out
}

type routerTextEmbedder struct {
	client llm.EmbeddingClient
	cfg    llm.ModelConfig
}

func (e *routerTextEmbedder) EmbedText(ctx context.Context, text string) ([]float32, error) {
	if e == nil || e.client == nil {
		return nil, errLearnersEmbedderUnavailable
	}
	res, err := e.client.EmbedText(ctx, text)
	if err != nil {
		return nil, err
	}
	return float32Vector(res.Embedding), nil
}

// EmbeddingProfile 返回三条探针向量及其模型身份，供表达向量 profile marker 使用
func (e *routerTextEmbedder) EmbeddingProfile(ctx context.Context, probes []string) ([]learners.TextEmbeddingProbe, error) {
	if e == nil || e.client == nil {
		return nil, errLearnersEmbedderUnavailable
	}
	results, err := e.client.EmbedTexts(ctx, probes)
	if err != nil {
		return nil, err
	}
	if len(results) != len(probes) {
		return nil, fmt.Errorf("embedding profile 探针结果数量不一致: got=%d want=%d", len(results), len(probes))
	}
	output := make([]learners.TextEmbeddingProbe, 0, len(results))
	for _, result := range results {
		fingerprint, err := llm.EmbeddingFingerprintFromConfig(e.cfg, len(result.Embedding))
		if err != nil {
			return nil, err
		}
		output = append(output, learners.TextEmbeddingProbe{
			Embedding:       float32Vector(result.Embedding),
			Model:           result.Model,
			ModelIdentifier: result.ModelIdentifier,
			APIProvider:     result.APIProvider,
			Fingerprint:     fingerprint.Map(),
		})
	}
	return output, nil
}

// wireLearnersEmbedding 从 llm.Router 注入表达向量嵌入客户端；失败时清空并回退 DB 启发式
// 启动/刷新后台历史补建循环，启动与热重载都不阻塞主流程
func (a *Agent) wireLearnersEmbedding(ctx context.Context) {
	reg := learners.DefaultRegistry()
	startBackfill := func(routeSignature string) {
		options := learners.ExpressionBackfillOptions{Settings: reg.VectorSettingsProvider(), RouteSignature: routeSignature}
		if err := reg.StartExpressionVectorBackfill(a.ctx, options); err != nil {
			zap.L().Warn("表达向量历史补建启动失败，保留当前索引", zap.Error(err))
		}
	}
	if a == nil {
		reg.SetTextEmbedder(nil)
		return
	}
	router := a.llmRouterSnapshot()
	if router == nil {
		reg.SetTextEmbedder(nil)
		startBackfill("")
		return
	}
	route, client, err := router.NewEmbeddingClientForTask(ctx, llm.TaskEmbedding)
	if err != nil {
		reg.SetTextEmbedder(nil)
		zap.L().Debug("表达向量 embedding 未就绪，MatchExpressions 将使用 DB 回退", zap.Error(err))
		startBackfill("")
		return
	}
	reg.SetTextEmbedder(&routerTextEmbedder{client: client, cfg: route.Config})
	startBackfill(expressionBackfillRouteSignature(route.Config))
}

// expressionBackfillRouteSignature 标识 embedding 路由身份；不含 APIKey
// 配置未变化时避免重复唤醒补建循环，模型/Provider 变化时触发重新标定
func expressionBackfillRouteSignature(cfg llm.ModelConfig) string {
	payload := map[string]any{
		"provider":     cfg.ProviderName,
		"model":        cfg.Model,
		"display_name": cfg.ModelDisplayName,
		"base_url":     cfg.BaseURL,
		"client_type":  cfg.ClientType,
		"extra_params": cfg.ExtraParams,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("%s|%s|%s|%s|%s", cfg.ProviderName, cfg.Model, cfg.BaseURL, cfg.ClientType, cfg.ModelDisplayName)
	}
	return string(raw)
}
