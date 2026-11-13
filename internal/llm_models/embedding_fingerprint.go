package llm

// 文本嵌入的向量空间身份，对齐 MaiBot src/common/embedding_identity.py
// 指纹只包含影响向量语义的配置；维度控制键不进入 extra_params 比较，但会改变 dimension_request_mode

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"maibot-go/internal/config/maibot"

	"github.com/bytedance/sonic"
)

const (
	// EmbeddingFingerprintVersion 与 Python build_embedding_fingerprint 的 version 保持一致
	EmbeddingFingerprintVersion = 2

	// EmbeddingDimensionRequestAlways/ Never 对齐 Python dimension_request_mode
	EmbeddingDimensionRequestAlways = "always"
	EmbeddingDimensionRequestNever  = "never"
)

// embeddingFingerprintJSON 对齐 json.dumps(ensure_ascii=False, sort_keys=True, separators=(",", ":"))
var embeddingFingerprintJSON = sonic.Config{SortMapKeys: true, EscapeHTML: false}.Froze()

// CanonicalJSONBytes 返回 Python json.dumps(ensure_ascii=False, sort_keys=True, separators=(",", ":")) 等价字节
// 供 embedding/图片指纹等需要稳定 JSON 摘要的调用方复用
func CanonicalJSONBytes(value any) ([]byte, error) {
	return embeddingFingerprintJSON.Marshal(value)
}

// CanonicalFingerprintHash 对稳定 JSON 计算 sha256，并返回 "sha256:<hex>" 形式
func CanonicalFingerprintHash(value any) (string, error) {
	encoded, err := CanonicalJSONBytes(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// EmbeddingFingerprintInput 生成文本嵌入向量空间指纹的输入
// extra_params 中的 dimensions/output_dimensionality 会被剔除，但仍会由调用方决定 dimension_request_mode
type EmbeddingFingerprintInput struct {
	Model                string
	Provider             string
	ModelIdentifier      string
	BaseURL              string
	Dimension            int
	DimensionRequestMode string
	ExtraParams          map[string]any
}

// EmbeddingFingerprint 文本嵌入向量空间身份，字段与 Python 返回值一一对应
type EmbeddingFingerprint struct {
	Version              int
	Hash                 string
	Model                string
	Provider             string
	ModelIdentifier      string
	BaseURL              string
	Dimension            int
	DimensionRequestMode string
	Source               string
}

// Map 返回与 Python 字典一致的字段集合
func (f EmbeddingFingerprint) Map() map[string]any {
	return map[string]any{
		"version":                f.Version,
		"hash":                   f.Hash,
		"model":                  f.Model,
		"provider":               f.Provider,
		"model_identifier":       f.ModelIdentifier,
		"base_url":               f.BaseURL,
		"dimension":              f.Dimension,
		"dimension_request_mode": f.DimensionRequestMode,
		"source":                 f.Source,
	}
}

// BuildEmbeddingFingerprint 按实际模型、服务地址、有效维度及语义参数生成稳定指纹
// 对应 Python build_embedding_fingerprint
func BuildEmbeddingFingerprint(input EmbeddingFingerprintInput) (EmbeddingFingerprint, error) {
	semanticParams := make(map[string]any, len(input.ExtraParams))
	for key, value := range input.ExtraParams {
		if key == "dimensions" || key == "output_dimensionality" {
			continue
		}
		semanticParams[key] = value
	}
	baseURL := strings.TrimRight(input.BaseURL, "/")
	payload := map[string]any{
		"model":                  input.Model,
		"provider":               input.Provider,
		"model_identifier":       input.ModelIdentifier,
		"base_url":               baseURL,
		"dimension":              input.Dimension,
		"dimension_request_mode": input.DimensionRequestMode,
		"extra_params":           semanticParams,
	}
	encoded, err := embeddingFingerprintJSON.Marshal(payload)
	if err != nil {
		return EmbeddingFingerprint{}, fmt.Errorf("序列化 embedding 指纹失败: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return EmbeddingFingerprint{
		Version:              EmbeddingFingerprintVersion,
		Hash:                 "sha256:" + hex.EncodeToString(digest[:]),
		Model:                input.Model,
		Provider:             input.Provider,
		ModelIdentifier:      input.ModelIdentifier,
		BaseURL:              baseURL,
		Dimension:            input.Dimension,
		DimensionRequestMode: input.DimensionRequestMode,
		Source:               "observed",
	}, nil
}

// ResolveEmbeddingModelFingerprint 从 model_config 解析实际模型与 Provider 后生成指纹
// 对应 Python src/services/embedding_service.py 的 resolve_embedding_model_fingerprint
func ResolveEmbeddingModelFingerprint(
	file maibot.ModelConfigFile,
	modelName string,
	modelIdentifier string,
	apiProvider string,
	dimension int,
) (EmbeddingFingerprint, error) {
	modelName = trimTask(modelName)
	if modelName == "" {
		return EmbeddingFingerprint{}, fmt.Errorf("embedding 模型名不能为空")
	}
	info, ok := findModelInfo(file.Models, modelName)
	if !ok {
		return EmbeddingFingerprint{}, fmt.Errorf("model_config 中未找到模型 %q", modelName)
	}
	providerName := trimTask(apiProvider)
	if providerName == "" {
		return EmbeddingFingerprint{}, fmt.Errorf("embedding 模型 %q 的 api_provider 不能为空", modelName)
	}
	provider, ok := findAPIProvider(file.ApiProviders, providerName)
	if !ok {
		return EmbeddingFingerprint{}, fmt.Errorf("未找到 api_provider %q", providerName)
	}
	if modelIdentifier == "" {
		modelIdentifier = trimTask(info.ModelIdentifier)
	}
	return BuildEmbeddingFingerprint(EmbeddingFingerprintInput{
		Model:                modelName,
		Provider:             providerName,
		ModelIdentifier:      modelIdentifier,
		BaseURL:              provider.BaseUrl,
		Dimension:            dimension,
		DimensionRequestMode: embeddingDimensionRequestMode(info.ExtraParams),
		ExtraParams:          info.ExtraParams,
	})
}

// EmbeddingFingerprintFromConfig 用已路由的 ModelConfig 生成指纹
// Go Router 的数据库档案没有 api_providers[].name 时 Provider 为空；调用方需把该降级如实记录
func EmbeddingFingerprintFromConfig(cfg ModelConfig, dimension int) (EmbeddingFingerprint, error) {
	model := trimTask(cfg.ModelDisplayName)
	if model == "" {
		model = trimTask(cfg.Model)
	}
	if model == "" {
		return EmbeddingFingerprint{}, fmt.Errorf("embedding 模型名为空")
	}
	return BuildEmbeddingFingerprint(EmbeddingFingerprintInput{
		Model:                model,
		Provider:             trimTask(cfg.ProviderName),
		ModelIdentifier:      trimTask(cfg.Model),
		BaseURL:              cfg.BaseURL,
		Dimension:            dimension,
		DimensionRequestMode: embeddingDimensionRequestMode(cfg.ExtraParams),
		ExtraParams:          cfg.ExtraParams,
	})
}

// embeddingDimensionRequestMode 对齐 Python 服务层：
// 模型显式配置 dimensions/output_dimensionality 时 always，否则 never
func embeddingDimensionRequestMode(extraParams map[string]any) string {
	if _, ok := extraParams["dimensions"]; ok {
		return EmbeddingDimensionRequestAlways
	}
	if _, ok := extraParams["output_dimensionality"]; ok {
		return EmbeddingDimensionRequestAlways
	}
	return EmbeddingDimensionRequestNever
}
