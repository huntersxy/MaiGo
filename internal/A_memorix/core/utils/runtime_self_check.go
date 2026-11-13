package utils

// A_memorix 运行时自检，对齐 Python core/utils/runtime_self_check.py
// embedding 与向量存储通过接口注入，避免 utils 依赖 embedding/storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

// DefaultSelfCheckSampleText 对齐 _DEFAULT_SAMPLE_TEXT
const DefaultSelfCheckSampleText = "A_Memorix runtime self check"

// SelfCheckEmbeddingManager 自检所需的 embedding 能力
type SelfCheckEmbeddingManager interface {
	// DetectDimension 对应 Python embedding_manager._detect_dimension
	DetectDimension(ctx context.Context) (int, error)
	// Encode 对应 Python embedding_manager.encode(str)，返回单条向量
	Encode(ctx context.Context, text string) ([]float32, error)
}

// RequestedDimensionProvider 提供请求维度，对应 get_requested_dimension
type RequestedDimensionProvider interface {
	GetRequestedDimension() int
}

// EmbeddingDimensionProvider 提供嵌入维度，对应 get_embedding_dimension
type EmbeddingDimensionProvider interface {
	GetEmbeddingDimension() int
}

// VectorStoreSnapshotSource 自检所需的向量存储只读能力
type VectorStoreSnapshotSource interface {
	Dimension() int
	NumVectors() int
	HasData() bool
}

// VectorStoreSnapshot 向量存储快照，对齐 _store_snapshot
type VectorStoreSnapshot struct {
	Available  bool
	Dimension  int
	NumVectors int
	HasData    bool
}

// ToDict 对齐快照字典键名
func (s VectorStoreSnapshot) ToDict() map[string]any {
	return map[string]any{
		"available":   s.Available,
		"dimension":   s.Dimension,
		"num_vectors": s.NumVectors,
		"has_data":    s.HasData,
	}
}

// RuntimeSelfCheckReport embedding 运行时自检报告，对齐 _build_report
type RuntimeSelfCheckReport struct {
	OK                   bool
	Code                 string
	Message              string
	ConfiguredDimension  int
	RequestedDimension   int
	VectorStoreDimension int
	DetectedDimension    int
	EncodedDimension     int
	ElapsedMS            float64
	SampleText           string
	VectorPools          map[string]any
	CheckedAt            float64
}

// ToDict 对齐 _build_report 返回的字典
func (r RuntimeSelfCheckReport) ToDict() map[string]any {
	pools := r.VectorPools
	if pools == nil {
		pools = map[string]any{}
	}
	return map[string]any{
		"ok":                     r.OK,
		"code":                   pyStrip(r.Code),
		"message":                pyStrip(r.Message),
		"configured_dimension":   r.ConfiguredDimension,
		"requested_dimension":    r.RequestedDimension,
		"vector_store_dimension": r.VectorStoreDimension,
		"detected_dimension":     r.DetectedDimension,
		"encoded_dimension":      r.EncodedDimension,
		"elapsed_ms":             r.ElapsedMS,
		"sample_text":            r.SampleText,
		"vector_pools":           pools,
		"checked_at":             r.CheckedAt,
	}
}

// CompletenessMessage 供调用方拼装降级提示
func (r RuntimeSelfCheckReport) CompletenessMessage() string {
	message := r.Message
	if pyStrip(message) == "" {
		message = "unknown"
	}
	return fmt.Sprintf("%s (configured=%d, store=%d, encoded=%d)",
		message, r.ConfiguredDimension, r.VectorStoreDimension, r.EncodedDimension)
}

// SelfCheckParams 自检输入
type SelfCheckParams struct {
	// Config 配置读取，对齐 plugin.get_config
	Config ConfigGetter
	// VectorStore 单池向量存储
	VectorStore VectorStoreSnapshotSource
	// EmbeddingManager 嵌入管理器
	EmbeddingManager SelfCheckEmbeddingManager
	// ParagraphVectorStore 段落池向量存储
	ParagraphVectorStore VectorStoreSnapshotSource
	// GraphVectorStore 图谱池向量存储
	GraphVectorStore VectorStoreSnapshotSource
	// SampleText 探测文本，空串时使用默认值
	SampleText string
	// Logger 日志注入
	Logger Logger
}

// SelfCheckCache 自检报告缓存，对齐 _runtime_self_check_report
type SelfCheckCache struct {
	mu     sync.Mutex
	report *RuntimeSelfCheckReport
}

// Get 读取缓存报告
func (c *SelfCheckCache) Get() (RuntimeSelfCheckReport, bool) {
	if c == nil {
		return RuntimeSelfCheckReport{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.report == nil {
		return RuntimeSelfCheckReport{}, false
	}
	return *c.report, true
}

// Set 写入缓存报告
func (c *SelfCheckCache) Set(report RuntimeSelfCheckReport) {
	if c == nil {
		return
	}
	c.mu.Lock()
	copyReport := report
	c.report = &copyReport
	c.mu.Unlock()
}

// Invalidate 清空缓存，便于强制重跑
func (c *SelfCheckCache) Invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.report = nil
	c.mu.Unlock()
}

// SafeInt 对齐 _safe_int
func SafeInt(value any, defaultValue int) int {
	parsed, err := pyFloat64(value)
	if err != nil {
		return int(defaultValue)
	}
	return int(parsed)
}

// selfCheckConfigValue 对齐 _get_config_value，config 为 nil 时返回默认值
func selfCheckConfigValue(config ConfigGetter, key string, defaultValue any) any {
	if config == nil {
		return defaultValue
	}
	return config(key, defaultValue)
}

// BuildStoreSnapshot 对齐 _store_snapshot
func BuildStoreSnapshot(store VectorStoreSnapshotSource) VectorStoreSnapshot {
	if store == nil {
		return VectorStoreSnapshot{Available: false, Dimension: 0, NumVectors: 0, HasData: false}
	}
	numVectors := store.NumVectors()
	return VectorStoreSnapshot{
		Available:  true,
		Dimension:  store.Dimension(),
		NumVectors: numVectors,
		HasData:    store.HasData(),
	}
}

// BuildVectorPoolsSnapshot 对齐 _build_vector_pools_snapshot
func BuildVectorPoolsSnapshot(
	config ConfigGetter,
	vectorStore VectorStoreSnapshotSource,
	paragraphVectorStore VectorStoreSnapshotSource,
	graphVectorStore VectorStoreSnapshotSource,
) map[string]any {
	configured := strings.ToLower(pyStrip(importValueText(selfCheckConfigValue(config, "retrieval.vector_pools.mode", "dual"))))
	if configured == "" {
		configured = "dual"
	}
	runtimeReady := isTruthy(selfCheckConfigValue(config, "runtime.vector_pools_ready", false))
	effective := "single"
	if configured == "dual" && runtimeReady {
		effective = "dual"
	}
	configuredMode := "single"
	if configured == "single" || configured == "dual" {
		configuredMode = configured
	}
	return map[string]any{
		"configured_mode": configuredMode,
		"effective_mode":  effective,
		"ready":           runtimeReady,
		"single_pool":     BuildStoreSnapshot(vectorStore).ToDict(),
		"paragraph_pool":  BuildStoreSnapshot(paragraphVectorStore).ToDict(),
		"graph_pool":      BuildStoreSnapshot(graphVectorStore).ToDict(),
	}
}

// NormalizeEncodedVector 校验单条嵌入向量，对齐 _normalize_encoded_vector
func NormalizeEncodedVector(encoded []float32) ([]float32, error) {
	if encoded == nil {
		return nil, errors.New("embedding encode returned None")
	}
	if len(encoded) == 0 {
		return nil, errors.New("embedding encode returned empty vector")
	}
	for _, value := range encoded {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, errors.New("embedding encode returned non-finite values")
		}
	}
	normalized := make([]float32, len(encoded))
	copy(normalized, encoded)
	return normalized, nil
}

// NormalizeEncodedMatrix 校验批量嵌入输出并取出单条向量
func NormalizeEncodedMatrix(encoded [][]float32) ([]float32, error) {
	if encoded == nil {
		return nil, errors.New("embedding encode returned None")
	}
	if len(encoded) == 0 {
		return nil, errors.New("embedding encode returned empty vector")
	}
	if len(encoded) != 1 {
		return nil, fmt.Errorf("embedding encode returned batched output: shape=(%d, %d)", len(encoded), len(encoded[0]))
	}
	return NormalizeEncodedVector(encoded[0])
}

// RequestedDimension 对齐 _get_requested_dimension
func RequestedDimension(manager any, fallback int) int {
	if manager == nil {
		return fallback
	}
	if provider, ok := manager.(RequestedDimensionProvider); ok {
		return provider.GetRequestedDimension()
	}
	if provider, ok := manager.(EmbeddingDimensionProvider); ok {
		return provider.GetEmbeddingDimension()
	}
	return fallback
}

// RunEmbeddingRuntimeSelfCheck 探测真实 embedding 链路并与存储维度比较
func RunEmbeddingRuntimeSelfCheck(ctx context.Context, params SelfCheckParams) RuntimeSelfCheckReport {
	configuredDimension := SafeInt(selfCheckConfigValue(params.Config, "embedding.dimension", 0), 0)
	vectorStoreDimension := 0
	if params.VectorStore != nil {
		vectorStoreDimension = params.VectorStore.Dimension()
	}
	var manager any
	if params.EmbeddingManager != nil {
		manager = params.EmbeddingManager
	}
	requestedDimension := RequestedDimension(manager, configuredDimension)
	vectorPools := BuildVectorPoolsSnapshot(params.Config, params.VectorStore, params.ParagraphVectorStore, params.GraphVectorStore)
	sampleText := params.SampleText
	if sampleText == "" {
		sampleText = DefaultSelfCheckSampleText
	}

	if params.EmbeddingManager == nil {
		return buildSelfCheckReport(
			false, "runtime_components_missing", "embedding_manager 未初始化",
			configuredDimension, requestedDimension, vectorStoreDimension, 0, 0, 0, sampleText, vectorPools,
		)
	}

	start := time.Now()
	detectedDimension := 0
	encodedDimension := 0
	detected, detectErr := params.EmbeddingManager.DetectDimension(ctx)
	if detectErr == nil {
		detectedDimension = SafeInt(detected, 0)
	}
	if detectErr == nil {
		fallback := detectedDimension
		if fallback == 0 {
			fallback = configuredDimension
		}
		requestedDimension = RequestedDimension(manager, fallback)
		var encoded []float32
		encoded, detectErr = params.EmbeddingManager.Encode(ctx, sampleText)
		if detectErr == nil {
			var normalized []float32
			normalized, detectErr = NormalizeEncodedVector(encoded)
			if detectErr == nil {
				encodedDimension = len(normalized)
			}
		}
	}
	if detectErr != nil {
		elapsedMS := float64(time.Since(start)) / float64(time.Millisecond)
		OrLogger(params.Logger).Warning(fmt.Sprintf("embedding runtime self-check failed: %v", detectErr))
		return buildSelfCheckReport(
			false, "embedding_probe_failed", fmt.Sprintf("embedding probe failed: %v", detectErr),
			configuredDimension, requestedDimension, vectorStoreDimension, detectedDimension, encodedDimension,
			elapsedMS, sampleText, vectorPools,
		)
	}

	elapsedMS := float64(time.Since(start)) / float64(time.Millisecond)
	expectedDimension := vectorStoreDimension
	if expectedDimension == 0 {
		expectedDimension = configuredDimension
	}
	if expectedDimension == 0 {
		expectedDimension = detectedDimension
	}
	if expectedDimension <= 0 {
		return buildSelfCheckReport(
			false, "invalid_expected_dimension", "无法确定期望 embedding 维度",
			configuredDimension, requestedDimension, vectorStoreDimension, detectedDimension, encodedDimension,
			elapsedMS, sampleText, vectorPools,
		)
	}
	if encodedDimension != expectedDimension {
		message := fmt.Sprintf("embedding 真实输出维度与当前向量存储不一致: expected=%d, encoded=%d", expectedDimension, encodedDimension)
		OrLogger(params.Logger).Error(message)
		return buildSelfCheckReport(
			false, "embedding_dimension_mismatch", message,
			configuredDimension, requestedDimension, vectorStoreDimension, detectedDimension, encodedDimension,
			elapsedMS, sampleText, vectorPools,
		)
	}
	return buildSelfCheckReport(
		true, "ok", "embedding runtime self-check passed",
		configuredDimension, requestedDimension, vectorStoreDimension, detectedDimension, encodedDimension,
		elapsedMS, sampleText, vectorPools,
	)
}

// buildSelfCheckReport 对齐 _build_report
func buildSelfCheckReport(
	ok bool,
	code string,
	message string,
	configuredDimension int,
	requestedDimension int,
	vectorStoreDimension int,
	detectedDimension int,
	encodedDimension int,
	elapsedMS float64,
	sampleText string,
	vectorPools map[string]any,
) RuntimeSelfCheckReport {
	if vectorPools == nil {
		vectorPools = map[string]any{}
	}
	return RuntimeSelfCheckReport{
		OK:                   ok,
		Code:                 pyStrip(code),
		Message:              pyStrip(message),
		ConfiguredDimension:  configuredDimension,
		RequestedDimension:   requestedDimension,
		VectorStoreDimension: vectorStoreDimension,
		DetectedDimension:    detectedDimension,
		EncodedDimension:     encodedDimension,
		ElapsedMS:            elapsedMS,
		SampleText:           sampleText,
		VectorPools:          vectorPools,
		CheckedAt:            float64(time.Now().UnixNano()) / 1e9,
	}
}

// EnsureRuntimeSelfCheck 执行自检或复用已有报告缓存
func EnsureRuntimeSelfCheck(ctx context.Context, params *SelfCheckParams, cache *SelfCheckCache, force bool) RuntimeSelfCheckReport {
	if params == nil {
		sampleText := DefaultSelfCheckSampleText
		return buildSelfCheckReport(
			false, "missing_plugin_or_config", "plugin/config unavailable",
			0, 0, 0, 0, 0, 0, sampleText, map[string]any{},
		)
	}
	if !force {
		if cached, ok := cache.Get(); ok {
			return cached
		}
	}
	report := RunEmbeddingRuntimeSelfCheck(ctx, *params)
	cache.Set(report)
	return report
}
