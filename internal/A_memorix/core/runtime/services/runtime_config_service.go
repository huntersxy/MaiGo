package services

// 运行时配置服务，对齐 Python core/runtime/services/runtime_config_service.py
// Python 侧把内核对象直接塞进运行时配置，Go 侧改为显式 Deps 注入属性端口

import (
	"context"
)

// SearchRuntimeBundle 对齐 build_search_runtime 的返回结构
type SearchRuntimeBundle struct {
	// Ready 检索运行时是否可用
	Ready bool
	// Error 失败原因，为空表示无错误
	Error string
	// Retriever 检索器
	Retriever any
	// ThresholdFilter 动态阈值过滤器
	ThresholdFilter any
	// SparseIndex 稀疏索引
	SparseIndex any
}

// RuntimeConfigDeps 运行时配置服务依赖的内核能力
// 字段为 nil 表示该能力未启用，服务按 Python 语义原样放入配置
type RuntimeConfigDeps struct {
	// Config 返回共享配置，应用 profile 成功时原地更新
	Config func() map[string]any
	// DualVectorPoolsEnabled 判断双向量池是否就绪，对齐 _dual_vector_pools_enabled
	DualVectorPoolsEnabled func() bool
	// RecordEmbeddingInput 对齐 _vector_space_service.record_input
	RecordEmbeddingInput any
	// VectorStore 主向量库
	VectorStore any
	// ParagraphVectorStore 段向量库
	ParagraphVectorStore any
	// GraphVectorStore 图向量库
	GraphVectorStore any
	// LegacyVectorStore 旧版单向量库视图，对齐 _legacy_vector_view
	LegacyVectorStore any
	// GraphStore 图存储
	GraphStore any
	// MetadataStore 元数据存储
	MetadataStore any
	// EmbeddingManager embedding 管理器
	EmbeddingManager any
	// SparseIndex 稀疏索引
	SparseIndex any
	// RelationWriteService 关系写入服务
	RelationWriteService any
	// RuntimeFacade 运行时外观对象，对齐 _runtime_facade
	RuntimeFacade any
	// RuntimeCapabilities 运行时能力声明，对齐 _runtime_capabilities
	RuntimeCapabilities map[string]any
	// BuildSearchRuntime 按运行时配置重建检索运行时
	BuildSearchRuntime func(runtimeConfig map[string]any) (*SearchRuntimeBundle, error)
	// ApplyRuntimeBundle 把重建结果写回内核
	ApplyRuntimeBundle func(bundle *SearchRuntimeBundle)
	// RefreshRuntimeDependents 刷新依赖检索运行时的服务
	RefreshRuntimeDependents func(preserveManagers bool)
	// ApplyRuntimeSparseMode 重新计算稀疏检索模式
	ApplyRuntimeSparseMode func()
}

// RuntimeConfigService 对齐 MemoryRuntimeConfigService
type RuntimeConfigService struct {
	Deps RuntimeConfigDeps
}

// NewRuntimeConfigService 构造运行时配置服务
func NewRuntimeConfigService(deps RuntimeConfigDeps) *RuntimeConfigService {
	return &RuntimeConfigService{Deps: deps}
}

// config 返回共享配置，未注入时返回空对象
func (s *RuntimeConfigService) config() map[string]any {
	if s == nil || s.Deps.Config == nil {
		return map[string]any{}
	}
	values := s.Deps.Config()
	if values == nil {
		return map[string]any{}
	}
	return values
}

// BuildRuntimeConfig 对齐 _build_runtime_config
func (s *RuntimeConfigService) BuildRuntimeConfig(ctx context.Context, baseConfig map[string]any) (map[string]any, error) {
	_ = ctx
	source := baseConfig
	if source == nil {
		source = s.config()
	}
	runtimeConfig := make(map[string]any, len(source)+12)
	for key, value := range source {
		runtimeConfig[key] = value
	}
	runtimeSection := map[string]any{}
	if existing, ok := runtimeConfig["runtime"].(map[string]any); ok && existing != nil {
		runtimeSection = make(map[string]any, len(existing)+1)
		for key, value := range existing {
			runtimeSection[key] = value
		}
	}
	dualReady := false
	if s.Deps.DualVectorPoolsEnabled != nil {
		dualReady = s.Deps.DualVectorPoolsEnabled()
	}
	runtimeSection["vector_pools_ready"] = dualReady
	runtimeConfig["runtime"] = runtimeSection

	paragraphStore := s.Deps.ParagraphVectorStore
	if paragraphStore == nil {
		paragraphStore = s.Deps.VectorStore
	}
	graphVectorStore := s.Deps.GraphVectorStore
	if graphVectorStore == nil {
		graphVectorStore = s.Deps.VectorStore
	}
	capabilities := make(map[string]any, len(s.Deps.RuntimeCapabilities))
	for key, value := range s.Deps.RuntimeCapabilities {
		capabilities[key] = value
	}
	runtimeConfig["record_embedding_input"] = s.Deps.RecordEmbeddingInput
	runtimeConfig["vector_store"] = s.Deps.VectorStore
	runtimeConfig["paragraph_vector_store"] = paragraphStore
	runtimeConfig["graph_vector_store"] = graphVectorStore
	runtimeConfig["legacy_vector_store"] = s.Deps.LegacyVectorStore
	runtimeConfig["graph_store"] = s.Deps.GraphStore
	runtimeConfig["metadata_store"] = s.Deps.MetadataStore
	runtimeConfig["embedding_manager"] = s.Deps.EmbeddingManager
	runtimeConfig["sparse_index"] = s.Deps.SparseIndex
	runtimeConfig["relation_write_service"] = s.Deps.RelationWriteService
	runtimeConfig["plugin_instance"] = s.Deps.RuntimeFacade
	runtimeConfig["runtime_capabilities"] = capabilities
	return runtimeConfig, nil
}

// MergeRuntimeConfigPatch 对齐 _merge_runtime_config_patch：深拷贝后递归合并
func (s *RuntimeConfigService) MergeRuntimeConfigPatch(base map[string]any, patch map[string]any) map[string]any {
	merged := map[string]any{}
	if base != nil {
		if copied, ok := deepCopyAny(base).(map[string]any); ok {
			merged = copied
		}
	}
	for key, value := range patch {
		if patchMap, ok := value.(map[string]any); ok {
			if baseMap, ok := merged[key].(map[string]any); ok {
				merged[key] = s.MergeRuntimeConfigPatch(baseMap, patchMap)
				continue
			}
		}
		merged[key] = deepCopyAny(value)
	}
	return merged
}

// ApplyRetrievalTuningProfile 对齐 apply_retrieval_tuning_profile
func (s *RuntimeConfigService) ApplyRetrievalTuningProfile(ctx context.Context, profile map[string]any, validate bool) (map[string]any, error) {
	if profile == nil {
		return tuningFailure("profile 必须是字典"), nil
	}
	nextConfig := s.MergeRuntimeConfigPatch(s.config(), profile)
	runtimeConfig, err := s.BuildRuntimeConfig(ctx, nextConfig)
	if err != nil {
		return nil, err
	}
	if s.Deps.BuildSearchRuntime == nil {
		return tuningFailure("检索运行时热重建失败"), nil
	}
	bundle, err := s.Deps.BuildSearchRuntime(runtimeConfig)
	if err != nil {
		return nil, err
	}
	if bundle == nil {
		bundle = &SearchRuntimeBundle{}
	}
	if validate && !bundle.Ready {
		return tuningFailure(bundleError(bundle)), nil
	}
	if !bundle.Ready {
		return tuningFailure(bundleError(bundle)), nil
	}
	current := s.config()
	for key := range current {
		delete(current, key)
	}
	for key, value := range nextConfig {
		current[key] = value
	}
	if s.Deps.ApplyRuntimeBundle != nil {
		s.Deps.ApplyRuntimeBundle(bundle)
	}
	if s.Deps.RefreshRuntimeDependents != nil {
		s.Deps.RefreshRuntimeDependents(true)
	}
	if s.Deps.ApplyRuntimeSparseMode != nil {
		s.Deps.ApplyRuntimeSparseMode()
	}
	return map[string]any{
		"success":           true,
		"runtime_rebuilt":   true,
		"validation_passed": true,
		"error":             "",
	}, nil
}

// tuningFailure 构造热重建失败结果
func tuningFailure(message string) map[string]any {
	return map[string]any{
		"success":           false,
		"runtime_rebuilt":   false,
		"validation_passed": false,
		"error":             message,
	}
}

// bundleError 取出失败原因并回退默认文案
func bundleError(bundle *SearchRuntimeBundle) string {
	if bundle != nil && bundle.Error != "" {
		return bundle.Error
	}
	return "检索运行时热重建失败"
}
