package model_client

// 插件 Provider 文本/图片嵌入
// 对齐 MaiBot src/llm_models/model_client/plugin_client.py:105-123：
//   - get_embedding 通过 "embedding" 操作，请求载荷为 serialize_embedding_request_snapshot
//   - get_image_embedding 通过 "image_embedding" 操作，另带 image_base64
//   - 空 embedding、success=false 都显式报错，不能返回零向量冒充成功
//
// 图片协议指纹使用编排器兜底算法 defaultImageEmbeddingProtocolHash，
// 与 Python utils_model.py:658-676 的 image_embedding_input/body/task/dimensions 载荷一致

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

type pluginEmbeddingClient struct {
	model *pluginModel
}

func newPluginEmbeddingClient(cfg ModelConfig) (EmbeddingClient, error) {
	pluginID, invoker, err := resolvePluginProvider(cfg)
	if err != nil {
		return nil, err
	}
	return &pluginEmbeddingClient{model: &pluginModel{cfg: cfg, pluginID: pluginID, invoker: invoker}}, nil
}

func (c *pluginEmbeddingClient) EmbedText(ctx context.Context, text string) (EmbeddingResult, error) {
	if c == nil || c.model == nil {
		return EmbeddingResult{}, fmt.Errorf("插件嵌入客户端未初始化")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return EmbeddingResult{}, fmt.Errorf("嵌入文本不能为空")
	}
	request := map[string]any{
		"embedding_input": text,
		"extra_params":    cloneAnyMap(c.model.cfg.ExtraParams),
		"model_info":      c.model.modelInfoSnapshot(),
		"request_kind":    requestKindEmbedding,
		"api_provider":    c.model.apiProviderSnapshot(true),
	}
	result, err := c.model.invoke(ctx, string(requestKindEmbedding), request)
	if err != nil {
		return EmbeddingResult{}, err
	}
	embedding, err := pluginEmbeddingVector(result["embedding"])
	if err != nil {
		return EmbeddingResult{}, err
	}
	return EmbeddingResult{
		Embedding:       embedding,
		Model:           pluginEmbeddingModelName(c.model.cfg),
		ModelIdentifier: c.model.cfg.Model,
		APIProvider:     c.model.cfg.ProviderName,
		Usage:           pluginUsage(result["usage"]),
	}, nil
}

func (c *pluginEmbeddingClient) EmbedTexts(ctx context.Context, texts []string) ([]EmbeddingResult, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("嵌入输入列表不能为空")
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

func (c *pluginEmbeddingClient) EmbedTextsConcurrent(ctx context.Context, texts []string, maxConcurrent int) ([]EmbeddingResult, error) {
	return EmbedTextsConcurrently(ctx, texts, maxConcurrent, c.EmbedText)
}

func (c *pluginEmbeddingClient) EmbedImage(ctx context.Context, input ImageEmbeddingInput) (EmbeddingResult, error) {
	if c == nil || c.model == nil {
		return EmbeddingResult{}, fmt.Errorf("插件嵌入客户端未初始化")
	}
	if len(input.Bytes) == 0 {
		return EmbeddingResult{}, fmt.Errorf("图片嵌入输入不能为空")
	}
	mimeType := strings.TrimSpace(input.MIMEType)
	if mimeType == "" || !strings.HasPrefix(mimeType, "image/") {
		return EmbeddingResult{}, fmt.Errorf("图片嵌入需要 image/* MIME 类型")
	}
	protocolHash, err := defaultImageEmbeddingProtocolHash(imageExtraParams(c.model.cfg.ExtraParams))
	if err != nil {
		return EmbeddingResult{}, err
	}
	request := map[string]any{
		"byte_size":          len(input.Bytes),
		"content_sha256":     pluginImageContentSHA256(input.Bytes),
		"extra_params":       cloneAnyMap(c.model.cfg.ExtraParams),
		"image_base64":       base64.StdEncoding.EncodeToString(input.Bytes),
		"mime_type":          mimeType,
		"model_info":         c.model.modelInfoSnapshot(),
		"preprocess_version": input.PreprocessVersion,
		"request_kind":       requestKindImageEmbedding,
		"api_provider":       c.model.apiProviderSnapshot(true),
	}
	result, err := c.model.invoke(ctx, string(requestKindImageEmbedding), request)
	if err != nil {
		return EmbeddingResult{}, err
	}
	embedding, err := pluginEmbeddingVector(result["embedding"])
	if err != nil {
		return EmbeddingResult{}, err
	}
	return EmbeddingResult{
		Embedding:         embedding,
		Model:             pluginEmbeddingModelName(c.model.cfg),
		ModelIdentifier:   c.model.cfg.Model,
		APIProvider:       c.model.cfg.ProviderName,
		RequestProtocol:   protocolHash,
		MIMEType:          mimeType,
		PreprocessVersion: input.PreprocessVersion,
		Usage:             pluginUsage(result["usage"]),
	}, nil
}

// pluginEmbeddingVector 严格解析 embedding：缺失、非数组、空数组、非数值元素都报 ParseError
func pluginEmbeddingVector(raw any) ([]float64, error) {
	values, ok := raw.([]any)
	if !ok || len(values) == 0 {
		return nil, &ParseError{Message: "插件 LLM Provider 嵌入响应缺少非空 embedding"}
	}
	out := make([]float64, 0, len(values))
	for _, value := range values {
		number, ok := floatFromAny(value)
		if !ok {
			return nil, &ParseError{Message: fmt.Sprintf("插件 LLM Provider 嵌入向量包含非数值元素: %v", value)}
		}
		out = append(out, number)
	}
	return out, nil
}

// pluginImageContentSHA256 图片原始字节的 sha256 十六进制
func pluginImageContentSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func pluginEmbeddingModelName(cfg ModelConfig) string {
	if name := strings.TrimSpace(cfg.ModelDisplayName); name != "" {
		return name
	}
	return cfg.Model
}

var _ EmbeddingClient = (*pluginEmbeddingClient)(nil)
var _ ConcurrentEmbeddingClient = (*pluginEmbeddingClient)(nil)

// 保证 schema 包在仅使用类型别名时仍参与编译检查
var _ = schema.TokenUsage{}
