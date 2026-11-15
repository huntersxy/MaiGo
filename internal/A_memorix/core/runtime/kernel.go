package memruntime

// A_memorix 统一运行时内核，对齐 core/runtime/sdk_memory_kernel.py
// 内核持有存储、检索与后台任务的共享状态，具体业务由注入的运行时服务完成

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"MaiGo/internal/A_memorix"
	"MaiGo/internal/A_memorix/core/embedding"
	"MaiGo/internal/A_memorix/core/retrieval"
	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/utils"
)

// 双池自动迁移的初始延迟与加锁重试间隔，对齐同名常量
const (
	DualVectorAutoMigrationInitialDelaySeconds = 5.0
)

// DualVectorAutoMigrationLockRetryDelaysSeconds 对齐 DUAL_VECTOR_AUTO_MIGRATION_LOCK_RETRY_DELAYS_SECONDS
var DualVectorAutoMigrationLockRetryDelaysSeconds = []float64{2.0, 5.0, 10.0}

// SdkMemoryKernelLoggerName 对齐 get_logger("A_Memorix.SDKMemoryKernel")
const SdkMemoryKernelLoggerName = "A_Memorix.SDKMemoryKernel"

// kernelTopLevelConfigKeys 对齐 _cfg 的单层直取键集合
var kernelTopLevelConfigKeys = map[string]struct{}{
	"storage": {}, "embedding": {}, "retrieval": {}, "graph": {}, "episode": {},
	"web": {}, "advanced": {}, "threshold": {}, "summarization": {},
	"person_profile": {}, "image_memory": {},
}

// Kernel 对齐 SDKMemoryKernel
type Kernel struct {
	pluginRoot             string
	config                 map[string]any
	dataDir                string
	embeddingDimension     int
	relationVectorsEnabled bool
	logger                 utils.Logger

	embeddingManager           *embedding.EmbeddingAPIAdapter
	vectorStore                *storage.VectorStore
	paragraphVectorStore       *storage.VectorStore
	graphVectorStore           *storage.VectorStore
	graphStore                 *storage.GraphStore
	metadataStore              *storage.Store
	imageMemoryRuntime         any
	relationWriteService       RelationWriteService
	sparseIndex                *retrieval.SparseBM25Index
	retriever                  *retrieval.DualPathRetriever
	thresholdFilter            *retrieval.DynamicThresholdFilter
	episodeRetriever           any
	aggregateQueryService      *utils.AggregateQueryService
	personProfileService       any
	episodeSegmentationService any
	episodeService             any
	summaryImporter            any
	importTaskManager          any
	retrievalTuningManager     any
	runtimeBundle              SearchRuntimeBundle
	runtimeFacade              *KernelRuntimeFacade

	// stateMu 保护运行期共享状态 map 与标量，读路径持读锁复制快照
	stateMu                   sync.RWMutex
	initialized               bool
	runtimeInitializationLock sync.Mutex
	lastMaintenanceAt         *float64
	requestDedupTasks         map[string]any
	vectorRebuildLock         sync.Mutex
	vectorRebuildConfigKey    string
	vectorRebuildFingerprint  map[string]any
	activeVectorSpaceID       string
	targetVectorSpaceID       string
	vectorSpaceInputs         map[string]string
	vectorSpaceSources        map[string]*storage.VectorStore
	relationGraphProjectLock  sync.Mutex
	runtimeWriterLock         *RuntimeWriterLock
	storageCleanupLock        sync.Mutex
	vectorPersistBlocked      bool
	vectorRebuildSourceDim    *int
	dualVectorPoolsReady      bool
	dualVectorMigrationTried  bool
	dualVectorMigrationStatus map[string]any
	backgroundTasks           map[string]any
	backgroundLock            sync.Mutex
	backgroundStopping        bool
	activePersonTimestamps    map[string]float64
	embeddingDegraded         map[string]any
	runtimeCapabilities       map[string]bool
	vectorHealth              map[string]any
	legacyVectorView          any
	effectiveFilterCache      map[string]any
	feedbackClassifier        any
	fuzzyModifyPlanner        any
	feedbackIntegration       *utils.FeedbackIntegrationConfig
	// statusReporter 原生装配注册的状态快照回调，供宿主暴露降级原因
	statusReporter func() map[string]any

	services KernelServices
}

// NewKernel 对齐 SDKMemoryKernel.__init__ 的字段默认值
func NewKernel(pluginRoot string, config map[string]any, logger utils.Logger) *Kernel {
	root := filepath.Clean(pluginRoot)
	if absolute, err := filepath.Abs(root); err == nil {
		root = absolute
	}
	if config == nil {
		config = map[string]any{}
	}
	kernel := &Kernel{
		pluginRoot:             root,
		config:                 config,
		logger:                 utils.OrLogger(logger),
		requestDedupTasks:      map[string]any{},
		vectorSpaceInputs:      map[string]string{},
		vectorSpaceSources:     map[string]*storage.VectorStore{},
		backgroundTasks:        map[string]any{},
		activePersonTimestamps: map[string]float64{},
		effectiveFilterCache:   map[string]any{"checked_at": 0.0, "needed": false},
	}
	storageConfig := SafeDict(kernel.Cfg("storage", nil))
	rawDataDir := "./data"
	if value, ok := storageConfig["data_dir"]; ok && PyTruthy(value) {
		rawDataDir = PyStr(value)
	}
	kernel.dataDir = memorix.ResolveRepoPath(rawDataDir, memorix.DefaultDataDir())
	dimension, err := PyInt(kernel.Cfg("embedding.dimension", 1024))
	if err != nil || dimension < 1 {
		dimension = 1
	}
	kernel.embeddingDimension = dimension
	kernel.relationVectorsEnabled = PyTruthy(kernel.Cfg("retrieval.relation_vectorization.enabled", false))
	kernel.runtimeWriterLock = NewRuntimeWriterLock(filepath.Join(kernel.dataDir, ".a_memorix_runtime_writer.lock"))
	kernel.dualVectorMigrationStatus = map[string]any{
		"running":   false,
		"attempted": false,
		"success":   false,
		"stage":     "idle",
		"progress": map[string]any{
			"total":                       0,
			"processed":                   0,
			"percent":                     0.0,
			"elapsed_seconds":             0.0,
			"estimated_remaining_seconds": nil,
		},
		"last_error":  "",
		"started_at":  nil,
		"finished_at": nil,
		"updated_at":  nil,
	}
	kernel.embeddingDegraded = map[string]any{
		"active":     false,
		"reason":     "",
		"since":      nil,
		"last_check": nil,
	}
	kernel.runtimeCapabilities = map[string]bool{
		"metadata":     false,
		"sparse":       false,
		"graph":        false,
		"vector_read":  false,
		"vector_write": false,
		"embedding":    false,
	}
	kernel.vectorHealth = map[string]any{
		"state":            "not_initialized",
		"error_code":       "",
		"reason":           "",
		"trusted_coverage": 0.0,
		"recovery_stage":   "idle",
		"operation_id":     "",
		"copy_progress":    map[string]any{},
		"updated_at":       nil,
	}
	kernel.runtimeFacade = NewKernelRuntimeFacade(kernel)
	return kernel
}

// Logger 返回内核日志器
func (k *Kernel) Logger() utils.Logger { return k.logger }

// PluginRoot 对齐 plugin_root
func (k *Kernel) PluginRoot() string { return k.pluginRoot }

// Config 返回插件配置引用，对齐 config
func (k *Kernel) Config() map[string]any { return k.config }

// DataDir 对齐 data_dir
func (k *Kernel) DataDir() string { return k.dataDir }

// EmbeddingDimension 对齐 embedding_dimension
func (k *Kernel) EmbeddingDimension() int {
	k.stateMu.RLock()
	defer k.stateMu.RUnlock()
	return k.embeddingDimension
}

// RelationVectorsEnabled 对齐 relation_vectors_enabled
func (k *Kernel) RelationVectorsEnabled() bool { return k.relationVectorsEnabled }

// Services 返回注入的服务集合
func (k *Kernel) Services() *KernelServices { return &k.services }

// SetServices 替换服务集合
func (k *Kernel) SetServices(services KernelServices) { k.services = services }

// RuntimeFacade 对齐 _runtime_facade
func (k *Kernel) RuntimeFacade() *KernelRuntimeFacade { return k.runtimeFacade }

// Cfg 对齐 _cfg：顶层键直取，其余按点号逐层取值
func (k *Kernel) Cfg(key string, defaultValue any) any {
	if _, ok := kernelTopLevelConfigKeys[key]; ok && k.config != nil {
		value, exists := k.config[key]
		if !exists {
			return defaultValue
		}
		return value
	}
	return ConfigValue(k.config, key, defaultValue)
}

// SetCfg 对齐 _set_cfg：按点号建字典后写入
func (k *Kernel) SetCfg(key string, value any) {
	if k.config == nil {
		k.config = map[string]any{}
	}
	parts := make([]string, 0, 4)
	for _, part := range strings.Split(key, ".") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return
	}
	current := k.config
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}

// IsRuntimeReady 对齐 is_runtime_ready
func (k *Kernel) IsRuntimeReady() bool {
	k.stateMu.RLock()
	initialized := k.initialized
	k.stateMu.RUnlock()
	return initialized && k.metadataStore != nil
}

// SetInitialized 供生命周期服务回写初始化标记
func (k *Kernel) SetInitialized(value bool) {
	k.stateMu.Lock()
	k.initialized = value
	k.stateMu.Unlock()
}

// Initialized 返回初始化标记
func (k *Kernel) Initialized() bool {
	k.stateMu.RLock()
	defer k.stateMu.RUnlock()
	return k.initialized
}

// SetRuntimeCapability 对齐 _set_runtime_capability
func (k *Kernel) SetRuntimeCapability(channel string, available bool) error {
	token := strings.TrimSpace(channel)
	k.stateMu.Lock()
	defer k.stateMu.Unlock()
	if _, ok := k.runtimeCapabilities[token]; !ok {
		return fmt.Errorf("未知运行时能力: %s", token)
	}
	k.runtimeCapabilities[token] = available
	return nil
}

// RuntimeCapabilities 返回能力位图副本
func (k *Kernel) RuntimeCapabilities() map[string]bool {
	k.stateMu.RLock()
	defer k.stateMu.RUnlock()
	out := make(map[string]bool, len(k.runtimeCapabilities))
	for name, value := range k.runtimeCapabilities {
		out[name] = value
	}
	return out
}

// runtimeCapabilityStatus 对齐 _runtime_capability_status
func (k *Kernel) runtimeCapabilityStatus() map[string]any {
	orderedChannels := []string{"metadata", "sparse", "graph", "vector_read", "vector_write", "embedding"}
	capabilities := make(map[string]bool, len(orderedChannels))
	available := make([]string, 0, len(orderedChannels))
	unavailable := make([]string, 0, len(orderedChannels))
	k.stateMu.RLock()
	for _, name := range orderedChannels {
		value := k.runtimeCapabilities[name]
		capabilities[name] = value
		if value {
			available = append(available, name)
		} else {
			unavailable = append(unavailable, name)
		}
	}
	k.stateMu.RUnlock()
	vectorRetrievalAvailable := capabilities["vector_read"] && capabilities["embedding"]
	retrievalChannels := make([]string, 0, 3)
	for _, name := range []string{"sparse", "graph"} {
		if capabilities[name] {
			retrievalChannels = append(retrievalChannels, name)
		}
	}
	if vectorRetrievalAvailable {
		retrievalChannels = append([]string{"vector_read"}, retrievalChannels...)
	}
	retrievalReady := capabilities["metadata"] && len(retrievalChannels) > 0
	retrievalMode := "unavailable"
	switch {
	case vectorRetrievalAvailable && (capabilities["sparse"] || capabilities["graph"]):
		retrievalMode = "hybrid"
	case vectorRetrievalAvailable:
		retrievalMode = "vector"
	case capabilities["sparse"] && capabilities["graph"]:
		retrievalMode = "sparse_graph"
	case capabilities["sparse"]:
		retrievalMode = "sparse"
	case capabilities["graph"]:
		retrievalMode = "graph"
	case capabilities["metadata"]:
		retrievalMode = "metadata_only"
	}
	k.stateMu.RLock()
	vectorState := ""
	if raw, ok := k.vectorHealth["state"]; ok && PyTruthy(raw) {
		vectorState = PyStr(raw)
	}
	k.stateMu.RUnlock()
	degraded := false
	if capabilities["metadata"] {
		for _, name := range orderedChannels[1:] {
			if !capabilities[name] {
				degraded = true
				break
			}
		}
		if !degraded {
			switch vectorState {
			case "degraded", "recovering", "unavailable":
				degraded = true
			}
		}
	}
	return map[string]any{
		"memory_enabled":       PyTruthy(k.Cfg("plugin.enabled", true)),
		"runtime_ready":        k.IsRuntimeReady(),
		"retrieval_ready":      retrievalReady,
		"degraded":             degraded,
		"retrieval_mode":       retrievalMode,
		"available_channels":   available,
		"unavailable_channels": unavailable,
		"capabilities":         capabilities,
		"vector_health":        k.VectorHealthSnapshot(),
	}
}

// RuntimeCapabilityStatus 对外暴露能力状态
func (k *Kernel) RuntimeCapabilityStatus() map[string]any { return k.runtimeCapabilityStatus() }
