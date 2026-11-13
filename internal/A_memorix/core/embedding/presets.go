package embedding

// 嵌入模型配置，对齐 A_memorix/core/embedding/presets.py

// Python dataclass 的默认值，Go 结构体零值无法表达 Normalization 默认为 true
const (
	defaultMaxSeqLength = 512
	defaultBatchSize    = 32
	defaultModelSizeMB  = 100
	defaultPooling      = "mean"
)

// EmbeddingModelConfig 嵌入模型配置，字段与 presets.EmbeddingModelConfig 逐一对应
type EmbeddingModelConfig struct {
	ModelName     string // 模型描述名称
	ModelPath     string // 实际加载路径（Local or HF）
	Dimension     int    // 嵌入向量维度
	MaxSeqLength  int    // 最大序列长度，默认 512
	BatchSize     int    // 编码批次大小，默认 32
	ModelSizeMB   int    // 估计显存占用，默认 100
	Description   string // 模型说明，默认空串
	Normalization bool   // 是否自动归一化，默认 true
	Pooling       string // 池化策略 mean/cls/max，默认 mean
	CacheDir      string // 模型缓存目录，空串对齐 Python None
}

// EmbeddingModelConfigOptions get_custom_config 的 **kwargs 等价物
// 零值表示沿用 Python dataclass 默认值，Normalization 用指针区分“未设置”与显式 false
type EmbeddingModelConfigOptions struct {
	MaxSeqLength  int
	BatchSize     int
	ModelSizeMB   int
	Description   string
	Normalization *bool
	Pooling       string
	CacheDir      string
}

// NewEmbeddingModelConfig 构造配置并补齐 Python dataclass 默认值；opt 为 nil 时全部取默认
func NewEmbeddingModelConfig(modelName, modelPath string, dimension int, opt *EmbeddingModelConfigOptions) EmbeddingModelConfig {
	config := EmbeddingModelConfig{
		ModelName:     modelName,
		ModelPath:     modelPath,
		Dimension:     dimension,
		MaxSeqLength:  defaultMaxSeqLength,
		BatchSize:     defaultBatchSize,
		ModelSizeMB:   defaultModelSizeMB,
		Normalization: true,
		Pooling:       defaultPooling,
	}
	if opt == nil {
		return config
	}
	if opt.MaxSeqLength != 0 {
		config.MaxSeqLength = opt.MaxSeqLength
	}
	if opt.BatchSize != 0 {
		config.BatchSize = opt.BatchSize
	}
	if opt.ModelSizeMB != 0 {
		config.ModelSizeMB = opt.ModelSizeMB
	}
	if opt.Description != "" {
		config.Description = opt.Description
	}
	if opt.Normalization != nil {
		config.Normalization = *opt.Normalization
	}
	if opt.Pooling != "" {
		config.Pooling = opt.Pooling
	}
	config.CacheDir = opt.CacheDir
	return config
}

// ValidateConfigCompatibility 检查两个配置是否兼容（只看维度），对齐 validate_config_compatibility
func ValidateConfigCompatibility(config1, config2 EmbeddingModelConfig) bool {
	return config1.Dimension == config2.Dimension
}

// AreModelsCompatible 检查模型是否完全相同（用于热切换判断），对齐 are_models_compatible
func AreModelsCompatible(config1, config2 EmbeddingModelConfig) bool {
	return config1.ModelPath == config2.ModelPath &&
		config1.Dimension == config2.Dimension &&
		config1.Pooling == config2.Pooling
}

// GetCustomConfig 创建自定义模型配置，对齐 get_custom_config
// cacheDir 对应 Python 的 cache_dir 位置参数，优先于 opt.CacheDir
func GetCustomConfig(modelName, modelPath string, dimension int, cacheDir string, opt *EmbeddingModelConfigOptions) EmbeddingModelConfig {
	config := NewEmbeddingModelConfig(modelName, modelPath, dimension, opt)
	config.CacheDir = cacheDir
	return config
}
