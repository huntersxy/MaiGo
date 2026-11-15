package imagememory

import (
	"MaiGo/internal/llm_models"
)

// ImageEmbeddingFingerprintInput 对齐 Python build_image_embedding_fingerprint 的关键字参数
type ImageEmbeddingFingerprintInput struct {
	Model               string
	Provider            string
	Dimension           int
	PreprocessVersion   string
	Normalization       string
	RequestProtocolHash string
}

// BuildImageEmbeddingFingerprint 生成图片嵌入空间指纹
func BuildImageEmbeddingFingerprint(input ImageEmbeddingFingerprintInput) (map[string]any, error) {
	comparable := map[string]any{
		"dimension":          input.Dimension,
		"modality":           "image",
		"model":              pythonStrip(input.Model),
		"normalization":      normalizationOr(input.Normalization),
		"preprocess_version": preprocessVersionOr(input.PreprocessVersion),
		"provider":           pythonStrip(input.Provider),
		"version":            1,
	}
	if input.RequestProtocolHash != "" {
		comparable["request_protocol_hash"] = input.RequestProtocolHash
	}
	hashValue, err := llm.CanonicalFingerprintHash(comparable)
	if err != nil {
		return nil, err
	}
	fingerprint := make(map[string]any, len(comparable)+1)
	for key, value := range comparable {
		fingerprint[key] = value
	}
	fingerprint["hash"] = hashValue
	return fingerprint, nil
}

// normalizationOr 对齐 str(normalization or "l2") 的取值
func normalizationOr(value string) string {
	if value == "" {
		return "l2"
	}
	return pythonStrip(value)
}

// preprocessVersionOr 对齐 str(preprocess_version or "identity_v1") 的取值
func preprocessVersionOr(value string) string {
	if value == "" {
		return "identity_v1"
	}
	return pythonStrip(value)
}

// FingerprintHash 读取指纹 hash 字段并去空白
func FingerprintHash(fingerprint map[string]any) string {
	if fingerprint == nil {
		return ""
	}
	value, ok := fingerprint["hash"]
	if !ok || value == nil {
		return ""
	}
	return pythonStrip(stringOr(value, ""))
}
