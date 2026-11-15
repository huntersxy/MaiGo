package embedding

// 基于宿主 embedding API 的适配器，对齐 A_memorix/core/embedding/api_adapter.py

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"MaiGo/internal/config/maibot"

	"go.uber.org/zap"
)

const (
	defaultAdapterBatchSize     = 32
	defaultAdapterMaxConcurrent = 5
	defaultAdapterDimension     = 1024
	defaultEmbeddingModelName   = "auto"
	defaultDimensionRequestMode = "explicit"
)

var adapterLogger = zap.L().Named("A_Memorix.EmbeddingAPIAdapter")

// ErrModelConfigNotInitialized 对齐 Python RuntimeError("model_config 未初始化")
var ErrModelConfigNotInitialized = errors.New("model_config 未初始化")

// ModelConfigProvider 提供 model_list 配置；llm.Router 结构性满足该接口
type ModelConfigProvider interface {
	ModelConfigFile() *maibot.ModelConfigFile
}

// RetryConfig 外层重试配置，对齐 retry_config 字典
type RetryConfig struct {
	MaxAttempts       int
	MaxWaitSeconds    float64
	MinWaitSeconds    float64
	BackoffMultiplier float64
}

// DefaultRetryConfig 返回 Python 侧默认重试配置
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:       5,
		MaxWaitSeconds:    40,
		MinWaitSeconds:    3,
		BackoffMultiplier: 3,
	}
}

// RetryConfigFromMap 解析 retry_config 字典并应用 Python 侧边界约束
func RetryConfigFromMap(values map[string]any) RetryConfig {
	config := DefaultRetryConfig()
	if values == nil {
		return config
	}
	if value, ok := lookupNumber(values, "max_attempts"); ok {
		config.MaxAttempts = int(value)
	}
	if value, ok := lookupNumber(values, "max_wait_seconds"); ok {
		config.MaxWaitSeconds = value
	}
	if value, ok := lookupNumber(values, "min_wait_seconds"); ok {
		config.MinWaitSeconds = value
	}
	if value, ok := lookupNumber(values, "backoff_multiplier"); ok {
		config.BackoffMultiplier = value
	}
	if config.MaxAttempts < 1 {
		config.MaxAttempts = 1
	}
	if config.MaxWaitSeconds < 0.1 {
		config.MaxWaitSeconds = 0.1
	}
	if config.MinWaitSeconds < 0.1 {
		config.MinWaitSeconds = 0.1
	}
	if config.BackoffMultiplier < 1.0 {
		config.BackoffMultiplier = 1.0
	}
	return config
}

// lookupNumber 从字典中读取数值配置
func lookupNumber(values map[string]any, key string) (float64, bool) {
	value, ok := values[key]
	if !ok {
		return 0, false
	}
	return toFloatValue(value)
}

// toFloatValue 将常见数值/字符串转换为 float64
func toFloatValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// EmbeddingAPIAdapterOptions 构造参数，字段对应 Python __init__ 参数
// 零值表示使用 Python 默认值；负数按 max(1, n) 处理，无法区分 Python 显式传 0 与未传
type EmbeddingAPIAdapterOptions struct {
	BatchSize            int
	MaxConcurrent        int
	DefaultDimension     int
	EnableCache          bool
	ModelName            string
	DimensionRequestMode string
	RetryConfig          map[string]any
	ConfigSource         ModelConfigProvider
	Transport            EmbeddingTransport
	HTTPClient           *http.Client
	Sleep                func(ctx context.Context, duration time.Duration) error
}

// EmbeddingAPIAdapter 通过宿主 embedding API 生成向量，对应 Python EmbeddingAPIAdapter
type EmbeddingAPIAdapter struct {
	batchSize            int
	maxConcurrent        int
	defaultDimension     int
	enableCache          bool
	modelName            string
	dimensionRequestMode string
	retryConfig          RetryConfig

	configSource ModelConfigProvider
	transport    EmbeddingTransport
	sleepFunc    func(ctx context.Context, duration time.Duration) error

	workersMu     sync.Mutex
	stateMu       sync.Mutex
	dimension     int
	detected      bool
	lastModel     string
	lastPrimary   string
	lastProvider  string
	lastDimension int
	lastConfigKey string

	totalEncoded   atomic.Int64
	totalErrors    atomic.Int64
	totalTimeNanos atomic.Int64
}

// NewEmbeddingAPIAdapter 构造适配器，对齐 Python __init__
func NewEmbeddingAPIAdapter(opt *EmbeddingAPIAdapterOptions) *EmbeddingAPIAdapter {
	if opt == nil {
		opt = &EmbeddingAPIAdapterOptions{}
	}
	batchSize := opt.BatchSize
	if batchSize == 0 {
		batchSize = defaultAdapterBatchSize
	}
	if batchSize < 1 {
		batchSize = 1
	}
	maxConcurrent := opt.MaxConcurrent
	if maxConcurrent == 0 {
		maxConcurrent = defaultAdapterMaxConcurrent
	}
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	defaultDimension := opt.DefaultDimension
	if defaultDimension == 0 {
		defaultDimension = defaultAdapterDimension
	}
	if defaultDimension < 1 {
		defaultDimension = 1
	}
	modelName := opt.ModelName
	if modelName == "" {
		modelName = defaultEmbeddingModelName
	}
	retryConfig := RetryConfigFromMap(opt.RetryConfig)
	adapter := &EmbeddingAPIAdapter{
		batchSize:            batchSize,
		maxConcurrent:        maxConcurrent,
		defaultDimension:     defaultDimension,
		enableCache:          opt.EnableCache,
		modelName:            modelName,
		dimensionRequestMode: normalizeDimensionRequestMode(opt.DimensionRequestMode),
		retryConfig:          retryConfig,
		configSource:         opt.ConfigSource,
		sleepFunc:            opt.Sleep,
	}
	if opt.Transport != nil {
		adapter.transport = opt.Transport
	} else {
		transport := NewHTTPEmbeddingTransport(opt.HTTPClient)
		if opt.Sleep != nil {
			transport.Sleep = opt.Sleep
		}
		adapter.transport = transport
	}
	adapterLogger.Debug(fmt.Sprintf(
		"Embedding 初始化: batch=%d, concurrent=%d, dim=%d, model=%s, dimension_request_mode=%s",
		batchSize,
		maxConcurrent,
		defaultDimension,
		modelName,
		adapter.dimensionRequestMode,
	))
	return adapter
}

// CreateEmbeddingAPIAdapter 对齐 create_embedding_api_adapter，透传构造参数
func CreateEmbeddingAPIAdapter(opt *EmbeddingAPIAdapterOptions) *EmbeddingAPIAdapter {
	return NewEmbeddingAPIAdapter(opt)
}

// modelConfigFile 获取模型配置，未初始化时返回与 Python 一致的文案
func (a *EmbeddingAPIAdapter) modelConfigFile() (*maibot.ModelConfigFile, error) {
	if a.configSource == nil {
		return nil, ErrModelConfigNotInitialized
	}
	file := a.configSource.ModelConfigFile()
	if file == nil {
		return nil, ErrModelConfigNotInitialized
	}
	return file, nil
}

// findModelInfo 查找模型条目，对齐 _find_model_info
func (a *EmbeddingAPIAdapter) findModelInfo(file *maibot.ModelConfigFile, name string) (maibot.ModelInfo, error) {
	for _, model := range file.Models {
		if model.Name == name {
			return model, nil
		}
	}
	return maibot.ModelInfo{}, fmt.Errorf("未找到 embedding 模型: %s", name)
}

// findProvider 查找 Provider 条目，对齐 _find_provider
func (a *EmbeddingAPIAdapter) findProvider(file *maibot.ModelConfigFile, name string) (maibot.APIProvider, error) {
	for _, provider := range file.ApiProviders {
		if provider.Name == name {
			return provider, nil
		}
	}
	return maibot.APIProvider{}, fmt.Errorf("未找到 embedding provider: %s", name)
}

// resolveCandidateModelNames 解析候选模型列表，对齐 _resolve_candidate_model_names
func (a *EmbeddingAPIAdapter) resolveCandidateModelNames(file *maibot.ModelConfigFile) []string {
	configured := append([]string(nil), file.ModelTaskConfig.Embedding.ModelList...)
	if a.modelName != "" && a.modelName != defaultEmbeddingModelName {
		candidates := []string{a.modelName}
		for _, name := range configured {
			if name != a.modelName {
				candidates = append(candidates, name)
			}
		}
		return candidates
	}
	return configured
}

// resolveModelProviderName 解析模型对应的 Provider 名称，对齐 _resolve_model_provider_name
func (a *EmbeddingAPIAdapter) resolveModelProviderName(file *maibot.ModelConfigFile, modelName string) string {
	token := strings.TrimSpace(modelName)
	if token == "" {
		return ""
	}
	info, err := a.findModelInfo(file, token)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(info.ApiProvider)
}

// normalizeDimensionRequestMode 归一化维度请求模式，对齐 _normalize_dimension_request_mode
func normalizeDimensionRequestMode(mode string) string {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	switch normalized {
	case "auto", "explicit", "on_demand", "on-demand":
		return "explicit"
	case "always", "force", "forced":
		return "always"
	case "never", "none", "natural":
		return "never"
	case "":
		return defaultDimensionRequestMode
	default:
		adapterLogger.Warn(fmt.Sprintf(
			"未知 embedding.dimension_request_mode='%s'，回退 explicit",
			mode,
		))
		return defaultDimensionRequestMode
	}
}

// normalizeDimensionOverride 归一化显式维度参数，对齐 _normalize_dimension_override
func normalizeDimensionOverride(dimensions *int) *int {
	if dimensions == nil {
		return nil
	}
	value := *dimensions
	if value < 1 {
		value = 1
	}
	return &value
}

// resolveCanonicalDimension 解析实际请求维度，对齐 _resolve_canonical_dimension
func (a *EmbeddingAPIAdapter) resolveCanonicalDimension(dimensions *int) int {
	if override := normalizeDimensionOverride(dimensions); override != nil {
		return *override
	}
	return a.GetRequestedDimension()
}

// GetRequestedDimension 返回请求维度，尚未探测时回落到配置维度
func (a *EmbeddingAPIAdapter) GetRequestedDimension() int {
	a.stateMu.Lock()
	dimension := a.dimension
	a.stateMu.Unlock()
	if dimension > 0 {
		return dimension
	}
	return a.defaultDimension
}

// GetEmbeddingDimension 返回已探测维度，未探测时告警并返回配置维度
func (a *EmbeddingAPIAdapter) GetEmbeddingDimension() int {
	a.stateMu.Lock()
	dimension := a.dimension
	a.stateMu.Unlock()
	if dimension > 0 {
		return dimension
	}
	adapterLogger.Warn(fmt.Sprintf("维度尚未检测，返回 configured_dimension: %d", a.defaultDimension))
	return a.defaultDimension
}

// shouldIncludeDimension 判断是否在请求中携带维度参数
func (a *EmbeddingAPIAdapter) shouldIncludeDimension(dimensions *int, include bool) bool {
	if !include {
		return false
	}
	switch a.dimensionRequestMode {
	case "never":
		return false
	case "always":
		return true
	default:
		return dimensions != nil
	}
}

// stripDimensionControlKeys 复制 extra_params 并去掉维度控制键，对齐 _strip_dimension_control_keys
func stripDimensionControlKeys(extraParams map[string]any) map[string]any {
	stripped := make(map[string]any, len(extraParams))
	for key, value := range extraParams {
		if key == "dimensions" || key == "output_dimensionality" {
			continue
		}
		stripped[key] = value
	}
	return stripped
}

// buildRequestExtraParams 构造请求附加参数，对齐 _build_request_extra_params
func (a *EmbeddingAPIAdapter) buildRequestExtraParams(
	provider maibot.APIProvider,
	baseExtraParams map[string]any,
	requestedDimension *int,
	includeDimension bool,
) map[string]any {
	extraParams := stripDimensionControlKeys(baseExtraParams)
	if !includeDimension || requestedDimension == nil {
		return extraParams
	}
	switch strings.ToLower(strings.TrimSpace(provider.ClientType)) {
	case "gemini", "google":
		extraParams["output_dimensionality"] = *requestedDimension
	case "openai":
		extraParams["dimensions"] = *requestedDimension
	}
	return extraParams
}

// desiredBatchSize 返回实际使用的批次大小
func (a *EmbeddingAPIAdapter) desiredBatchSize(batchSize int) int {
	if batchSize > 0 {
		return batchSize
	}
	return a.batchSize
}

// currentMaxConcurrent 返回当前并发上限
func (a *EmbeddingAPIAdapter) currentMaxConcurrent() int {
	a.workersMu.Lock()
	defer a.workersMu.Unlock()
	return a.maxConcurrent
}

// setMaxConcurrent 临时调整并发上限，返回旧值
func (a *EmbeddingAPIAdapter) setMaxConcurrent(limit int) int {
	a.workersMu.Lock()
	defer a.workersMu.Unlock()
	previous := a.maxConcurrent
	a.maxConcurrent = limit
	return previous
}

// GetModelInfo 获取适配器统计信息，对齐 get_model_info
func (a *EmbeddingAPIAdapter) GetModelInfo() map[string]any {
	a.stateMu.Lock()
	dimension := a.dimension
	detected := a.detected
	a.stateMu.Unlock()
	encoded := a.totalEncoded.Load()
	averageTime := 0.0
	if encoded > 0 {
		averageTime = float64(a.totalTimeNanos.Load()) / float64(time.Second) / float64(encoded)
	}
	return map[string]any{
		"model_name":             a.modelName,
		"dimension":              a.GetEmbeddingDimension(),
		"configured_dimension":   a.defaultDimension,
		"requested_dimension":    a.GetRequestedDimension(),
		"detected_dimension":     dimension,
		"dimension_detected":     detected,
		"dimension_request_mode": a.dimensionRequestMode,
		"batch_size":             a.batchSize,
		"max_concurrent":         a.currentMaxConcurrent(),
		"total_encoded":          int(encoded),
		"total_errors":           int(a.totalErrors.Load()),
		"avg_time_per_text":      averageTime,
	}
}

// GetStatistics 对齐 get_statistics，返回与 GetModelInfo 相同的字典
func (a *EmbeddingAPIAdapter) GetStatistics() map[string]any {
	return a.GetModelInfo()
}

// IsModelLoaded 适配器恒为已加载，对齐 is_model_loaded
func (a *EmbeddingAPIAdapter) IsModelLoaded() bool {
	return true
}

// String 对齐 __repr__
func (a *EmbeddingAPIAdapter) String() string {
	a.stateMu.Lock()
	dimension := a.dimension
	a.stateMu.Unlock()
	return fmt.Sprintf(
		"EmbeddingAPIAdapter(configured=%d, requested=%d, detected=%d, dimension_request_mode=%s, encoded=%d)",
		a.defaultDimension,
		a.GetRequestedDimension(),
		dimension,
		a.dimensionRequestMode,
		a.totalEncoded.Load(),
	)
}
