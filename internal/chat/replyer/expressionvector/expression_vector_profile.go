package expressionvector

// embedding profile 的持久化反序列化与向量空间漂移检测；
// 对齐 expression_vector_index.py 的 _deserialize_embedding_profile /
// _resolve_embedding_profile_candidate / _embedding_profile_probe_similarities。

import (
	"fmt"
	"math"
	"strings"
)

const (
	// expressionVectorProfileMinCosine 漂移判定阈值，对齐 EMBEDDING_PROFILE_MIN_COSINE_SIMILARITY
	expressionVectorProfileMinCosine = 0.999
	// expressionVectorProfileDriftConfirmations 连续确认次数，对齐 EMBEDDING_PROFILE_DRIFT_CONFIRMATIONS
	expressionVectorProfileDriftConfirmations = 3
)

// deserializeExpressionEmbeddingProfile 从索引元数据恢复并校验持久化 profile
// 版本、探针文本、探针数量、维度与 marker 全部比对，任一不符返回错误。
func deserializeExpressionEmbeddingProfile(raw map[string]any) (ExpressionEmbeddingProfile, error) {
	if raw == nil {
		return ExpressionEmbeddingProfile{}, fmt.Errorf("embedding profile 元数据为空")
	}
	version := intValueOf(raw["version"])
	if version != 2 && version != expressionVectorProfileVersion {
		return ExpressionEmbeddingProfile{}, fmt.Errorf("embedding profile 元数据版本不匹配: %d", version)
	}
	probeTexts := stringListOf(raw["probe_texts"])
	if len(probeTexts) != len(ExpressionEmbeddingProfileProbeTexts) {
		return ExpressionEmbeddingProfile{}, fmt.Errorf("embedding profile 探针文本与当前版本不一致")
	}
	for index, expected := range ExpressionEmbeddingProfileProbeTexts {
		if probeTexts[index] != expected {
			return ExpressionEmbeddingProfile{}, fmt.Errorf("embedding profile 探针文本与当前版本不一致")
		}
	}
	rawProbes := matrixOf(raw["probe_embeddings"])
	if len(rawProbes) != len(ExpressionEmbeddingProfileProbeTexts) {
		return ExpressionEmbeddingProfile{}, fmt.Errorf("embedding profile 持久化探针数量异常")
	}

	modelName := normalizeExpressionVectorText(anyStringOf(raw["model_name"]))
	modelIdentifier := normalizeExpressionVectorText(anyStringOf(raw["model_identifier"]))
	apiProvider := normalizeExpressionVectorText(anyStringOf(raw["api_provider"]))
	if modelName == "" || modelIdentifier == "" || apiProvider == "" {
		return ExpressionEmbeddingProfile{}, fmt.Errorf("embedding profile 持久化后端身份为空")
	}
	revision := intValueOf(raw["revision"])
	if revision <= 0 {
		revision = 1
	}
	var fingerprint map[string]any
	if version >= 3 {
		if mapped, ok := raw["embedding_fingerprint"].(map[string]any); ok {
			fingerprint = mapped
		}
	}
	profile, err := buildExpressionEmbeddingProfileFromParts(modelName, modelIdentifier, apiProvider, rawProbes, revision, fingerprint)
	if err != nil {
		return ExpressionEmbeddingProfile{}, err
	}
	storedDimension := intValueOf(raw["dimension"])
	if storedDimension != profile.Dimension {
		return ExpressionEmbeddingProfile{}, fmt.Errorf("embedding profile 持久化维度不一致: stored=%d, actual=%d", storedDimension, profile.Dimension)
	}
	storedMarker := normalizeExpressionVectorText(anyStringOf(raw["marker"]))
	if storedMarker != profile.Marker {
		return ExpressionEmbeddingProfile{}, fmt.Errorf("embedding profile 持久化 marker 不一致: stored=%s, actual=%s",
			previewMarker(storedMarker), previewMarker(profile.Marker))
	}
	return profile, nil
}

func previewMarker(marker string) string {
	if len(marker) <= 12 {
		return marker
	}
	return marker[:12]
}

// buildExpressionEmbeddingProfileFromParts 按稳定身份与探针基准构建 profile
func buildExpressionEmbeddingProfileFromParts(
	modelName, modelIdentifier, apiProvider string,
	probes [][]float32,
	revision int,
	fingerprint map[string]any,
) (ExpressionEmbeddingProfile, error) {
	if len(probes) == 0 {
		return ExpressionEmbeddingProfile{}, fmt.Errorf("embedding profile 探针返回空向量")
	}
	dimension := len(probes[0])
	if dimension <= 0 {
		return ExpressionEmbeddingProfile{}, fmt.Errorf("embedding profile 探针返回空向量")
	}
	for _, probe := range probes {
		if len(probe) != dimension {
			return ExpressionEmbeddingProfile{}, fmt.Errorf("embedding profile 探针维度不一致")
		}
	}
	if revision <= 0 {
		return ExpressionEmbeddingProfile{}, fmt.Errorf("embedding profile 修订号无效: %d", revision)
	}
	markerPayload := map[string]any{
		"version":               expressionVectorProfileVersion,
		"model_name":            modelName,
		"model_identifier":      modelIdentifier,
		"api_provider":          apiProvider,
		"dimension":             dimension,
		"revision":              revision,
		"embedding_fingerprint": fingerprintOrEmpty(fingerprint),
	}
	marker, err := pythonJSONSHA256(markerPayload)
	if err != nil {
		return ExpressionEmbeddingProfile{}, err
	}
	embeddings := make([][]float32, 0, len(probes))
	for _, probe := range probes {
		embeddings = append(embeddings, append([]float32(nil), probe...))
	}
	return ExpressionEmbeddingProfile{
		Marker:          marker,
		ModelName:       modelName,
		ModelIdentifier: modelIdentifier,
		APIProvider:     apiProvider,
		Dimension:       dimension,
		Revision:        revision,
		ProbeTexts:      append([]string(nil), ExpressionEmbeddingProfileProbeTexts...),
		ProbeEmbeddings: embeddings,
		Fingerprint:     fingerprintOrEmpty(fingerprint),
	}, nil
}

func fingerprintOrEmpty(fingerprint map[string]any) map[string]any {
	if fingerprint == nil {
		return map[string]any{}
	}
	return fingerprint
}

// expressionProfileIdentity 稳定后端身份三元组
func expressionProfileIdentity(profile ExpressionEmbeddingProfile) string {
	return strings.Join([]string{
		normalizeExpressionVectorText(profile.ModelName),
		normalizeExpressionVectorText(profile.ModelIdentifier),
		normalizeExpressionVectorText(profile.APIProvider),
	}, "\x00")
}

// expressionProfileProbeSimilarities 逐条探针计算余弦相似度，长度不等时返回 0
func expressionProfileProbeSimilarities(persisted, candidate ExpressionEmbeddingProfile) []float64 {
	count := len(persisted.ProbeEmbeddings)
	if len(candidate.ProbeEmbeddings) < count {
		count = len(candidate.ProbeEmbeddings)
	}
	similarities := make([]float64, 0, count)
	for index := 0; index < count; index++ {
		similarities = append(similarities, cosineSimilarityOf(persisted.ProbeEmbeddings[index], candidate.ProbeEmbeddings[index]))
	}
	return similarities
}

func cosineSimilarityOf(left, right []float32) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	var dot, leftNorm, rightNorm float64
	for index := range left {
		l := float64(left[index])
		r := float64(right[index])
		dot += l * r
		leftNorm += l * l
		rightNorm += r * r
	}
	if leftNorm <= 0 || rightNorm <= 0 {
		return 0
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

// expressionProfileDriftState 连续漂移确认状态
type expressionProfileDriftState struct {
	candidate     *ExpressionEmbeddingProfile
	confirmations int
}

func (s *expressionProfileDriftState) reset() {
	s.candidate = nil
	s.confirmations = 0
}

// resolveExpressionProfileCandidate 按持久化基准与连续确认结果解析本次实际使用的 profile
// 返回的 second 值表示是否发生了确认后的切换（revision 递增）。
func (s *expressionProfileDriftState) resolve(
	persisted *ExpressionEmbeddingProfile,
	candidate ExpressionEmbeddingProfile,
) (ExpressionEmbeddingProfile, bool) {
	if persisted == nil {
		s.reset()
		return candidate, false
	}
	if expressionProfileIdentity(*persisted) != expressionProfileIdentity(candidate) {
		s.reset()
		return candidate, false
	}
	similarities := expressionProfileProbeSimilarities(*persisted, candidate)
	if len(similarities) == 0 {
		s.reset()
		return *persisted, false
	}
	minSimilarity := similarities[0]
	for _, value := range similarities[1:] {
		if value < minSimilarity {
			minSimilarity = value
		}
	}
	if minSimilarity >= expressionVectorProfileMinCosine {
		s.reset()
		return *persisted, false
	}

	if s.candidate != nil && expressionProfilesCompatible(*s.candidate, candidate) {
		s.confirmations++
	} else {
		confirmed := candidate
		s.candidate = &confirmed
		s.confirmations = 1
	}
	if s.confirmations < expressionVectorProfileDriftConfirmations {
		return *persisted, false
	}
	next, err := buildExpressionEmbeddingProfileFromParts(
		candidate.ModelName, candidate.ModelIdentifier, candidate.APIProvider,
		candidate.ProbeEmbeddings, persisted.Revision+1, candidate.Fingerprint,
	)
	if err != nil {
		s.reset()
		return *persisted, false
	}
	s.reset()
	return next, true
}

// expressionProfilesCompatible 两次候选是否属于同一向量空间
func expressionProfilesCompatible(left, right ExpressionEmbeddingProfile) bool {
	if expressionProfileIdentity(left) != expressionProfileIdentity(right) {
		return false
	}
	if left.Dimension != right.Dimension {
		return false
	}
	similarities := expressionProfileProbeSimilarities(left, right)
	if len(similarities) == 0 {
		return false
	}
	for _, value := range similarities {
		if value < expressionVectorProfileMinCosine {
			return false
		}
	}
	return true
}

// intValueOf 宽松取整
func intValueOf(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	}
	return 0
}

// anyStringOf 宽松取字符串
func anyStringOf(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// stringListOf 取字符串数组
func stringListOf(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]string); ok {
			return typed
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, anyStringOf(item))
	}
	return out
}

// matrixOf 取二维浮点数组
func matrixOf(value any) [][]float32 {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([][]float32, 0, len(items))
	for _, item := range items {
		row, ok := item.([]any)
		if !ok {
			return nil
		}
		vector := make([]float32, 0, len(row))
		for _, cell := range row {
			switch typed := cell.(type) {
			case float64:
				vector = append(vector, float32(typed))
			case float32:
				vector = append(vector, typed)
			case int:
				vector = append(vector, float32(typed))
			case int64:
				vector = append(vector, float32(typed))
			default:
				return nil
			}
		}
		out = append(out, vector)
	}
	return out
}

// persistedEmbeddingProfile 返回索引里持久化的 profile 基准，无法反序列化时返回 nil
//
// 反序列化失败说明索引元数据与当前版本不兼容（版本、探针或 marker 不符），
// 此时按「无基准」处理，让本次探针结果直接成为新基准。
func (s *PersistentExpressionVectorStore) persistedEmbeddingProfile() *ExpressionEmbeddingProfile {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	raw := s.embeddingProfile
	s.mu.Unlock()
	if raw == nil {
		return nil
	}
	profile, err := deserializeExpressionEmbeddingProfile(raw)
	if err != nil {
		return nil
	}
	return &profile
}
