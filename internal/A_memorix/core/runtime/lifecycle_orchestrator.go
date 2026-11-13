package memruntime

// 生命周期编排，对齐 core/runtime/lifecycle_orchestrator.py
// 插件侧对象通过 LifecyclePlugin 接口注入，存储与自检构造通过 LifecycleDeps 注入

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"maibot-go/internal/A_memorix"
	"maibot-go/internal/A_memorix/core/embedding"
	"maibot-go/internal/A_memorix/core/retrieval"
	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// LifecycleOrchestratorLoggerName 对齐 get_logger("A_Memorix.LifecycleOrchestrator")
const LifecycleOrchestratorLoggerName = "A_Memorix.LifecycleOrchestrator"

// BackgroundTask 对齐 asyncio.Task 在编排中使用的 done/cancel/await 语义
type BackgroundTask interface {
	Done() bool
	Cancel()
	Wait() error
}

// LifecyclePlugin 生命周期编排需要读写的插件对象
// 说明：Python 版为鸭子类型，Go 版把被访问的全部成员显式化为接口
type LifecyclePlugin interface {
	Initialized() bool
	SetInitialized(initialized bool)
	SetRuntimeReady(ready bool)
	CheckStorageReady() bool
	InitLock() *sync.Mutex
	ValidateRuntimeConfig() error
	UpdatePluginConfig()
	GetConfig(key string, defaultValue any) any
	SetConfigValue(key string, value any)
	SelfCheckCache() *utils.SelfCheckCache

	DualVectorPoolsReady() bool
	SetDualVectorPoolsReady(ready bool)

	EmbeddingManager() *embedding.EmbeddingAPIAdapter
	SetEmbeddingManager(manager *embedding.EmbeddingAPIAdapter)
	VectorStore() *storage.VectorStore
	SetVectorStore(store *storage.VectorStore)
	ParagraphVectorStore() *storage.VectorStore
	SetParagraphVectorStore(store *storage.VectorStore)
	GraphVectorStore() *storage.VectorStore
	SetGraphVectorStore(store *storage.VectorStore)
	GraphStore() *storage.GraphStore
	SetGraphStore(store *storage.GraphStore)
	MetadataStore() *storage.Store
	SetMetadataStore(store *storage.Store)
	SparseIndex() *retrieval.SparseBM25Index
	SetSparseIndex(index *retrieval.SparseBM25Index)
	RelationWriteService() RelationWriteService
	SetRelationWriteService(service RelationWriteService)
}

// LifecycleLoops 汇总插件侧后台循环，nil 表示该方法不存在
type LifecycleLoops struct {
	ScheduledImport           func() error
	AutoSave                  func() error
	PersonProfileRefresh      func() error
	PersonProfileRefreshQueue func() error
	MemoryMaintenance         func() error
	RelationVectorBackfill    func() error
	EpisodeGeneration         func() error
}

// TaskSpawner 创建后台任务，默认实现为 goroutine 包装
type TaskSpawner func(name string, loop func() error) BackgroundTask

// RelationWriteParams 对齐 RelationWriteService 的构造参数
type RelationWriteParams struct {
	MetadataStore       *storage.Store
	GraphStore          *storage.GraphStore
	VectorStore         *storage.VectorStore
	GraphVectorStore    *storage.VectorStore
	EmbeddingManager    *embedding.EmbeddingAPIAdapter
	UseTypedRelationIDs bool
}

// LifecycleDeps 生命周期编排可注入的外部构造能力，nil 走默认实现
type LifecycleDeps struct {
	Logger                  utils.Logger
	NewVectorStore          func(cfg storage.VectorStoreConfig) (*storage.VectorStore, error)
	NewGraphStore           func(format storage.SparseMatrixFormat, dataDir string) *storage.GraphStore
	NewMetadataStore        func(dataDir string, dbName string) *storage.Store
	NewSparseIndex          func(store *storage.Store, cfg retrieval.SparseBM25Config) *retrieval.SparseBM25Index
	NewRelationWriteService func(params RelationWriteParams) RelationWriteService
	RunFormatMigration      func(dataDir string) error
	EnsureSelfCheck         func(ctx context.Context, plugin LifecyclePlugin, force bool) (map[string]any, error)
	Spawner                 TaskSpawner
}

// lifecycleLogger 返回注入的日志器
func (d *LifecycleDeps) lifecycleLogger() utils.Logger {
	if d == nil {
		return utils.NopLogger{}
	}
	return utils.OrLogger(d.Logger)
}

// DualVectorReady 对齐 _dual_vector_ready
func DualVectorReady(dataDir string, expectedDimension int) bool {
	manifestPath := filepath.Join(dataDir, "vectors", "dual_ready.json")
	if _, err := os.Stat(manifestPath); err != nil {
		return false
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		lifecycleWarnf("读取双池 ready manifest 失败: %v", err)
		return false
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		lifecycleWarnf("读取双池 ready manifest 失败: %v", err)
		return false
	}
	manifest, ok := payload.(map[string]any)
	if !ok || PyStr(manifest["status"]) != "ready" {
		return false
	}
	manifestDimension, _ := PyInt(derefAny(manifest["dimension"], 0))
	if manifestDimension != 0 && manifestDimension != expectedDimension {
		lifecycleWarnf("双池 ready manifest 维度不匹配: manifest=%d, expected=%d", manifestDimension, expectedDimension)
		return false
	}
	paragraphDir := filepath.Join(dataDir, "vectors", "paragraph")
	graphDir := filepath.Join(dataDir, "vectors", "graph")
	return dirExists(paragraphDir) && dirExists(graphDir)
}

// lifecycleWarnf 在无插件上下文的场景复用包级 zap 日志
func lifecycleWarnf(format string, args ...any) {
	NewZapLogger(LifecycleOrchestratorLoggerName).Warning(fmt.Sprintf(format, args...))
}

// derefAny 返回值为 nil 时的默认值，对齐 value or default 语义
func derefAny(value any, fallback any) any {
	if value == nil || !PyTruthy(value) {
		return fallback
	}
	return value
}

// dirExists 判断路径是否为已存在目录
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// SetRuntimeVectorPoolsReady 对齐 _set_runtime_vector_pools_ready
// 说明：Python 会同时写 plugin.config 与 plugin._plugin_config 两份字典，Go 插件对象只暴露一份配置写入
func SetRuntimeVectorPoolsReady(plugin LifecyclePlugin, ready bool) {
	plugin.SetConfigValue("runtime.vector_pools_ready", ready)
	plugin.SetDualVectorPoolsReady(ready)
}

// EnsureInitialized 对齐 ensure_initialized
func EnsureInitialized(ctx context.Context, plugin LifecyclePlugin, deps *LifecycleDeps) error {
	logger := deps.lifecycleLogger()
	if plugin.Initialized() {
		plugin.SetRuntimeReady(plugin.CheckStorageReady())
		return nil
	}

	lock := plugin.InitLock()
	lock.Lock()
	defer lock.Unlock()
	if plugin.Initialized() {
		plugin.SetRuntimeReady(plugin.CheckStorageReady())
		return nil
	}

	logger.Info("A_Memorix 插件正在异步初始化存储组件...")
	if err := plugin.ValidateRuntimeConfig(); err != nil {
		return err
	}
	if err := InitializeStorageAsync(ctx, plugin, deps); err != nil {
		return err
	}
	report, err := runLifecycleSelfCheck(ctx, plugin, deps)
	if err != nil {
		return err
	}
	if ok, _ := report["ok"].(bool); !ok {
		message := PyStr(report["message"])
		if message == "" || message == "None" {
			message = "unknown"
		}
		logger.Error(fmt.Sprintf(
			"A_Memorix runtime self-check failed: %s; 建议执行 python src/A_memorix/scripts/runtime_self_check.py --json",
			message,
		))
	}

	if plugin.GraphStore() != nil && plugin.MetadataStore() != nil {
		relationCount, countErr := plugin.MetadataStore().CountRelations(false, false)
		if countErr != nil {
			return countErr
		}
		if relationCount > 0 && !plugin.GraphStore().HasEdgeHashMap() {
			return fmt.Errorf("检测到 relations 数据存在但 edge-hash-map 为空。 请先执行 scripts/release_vnext_migrate.py migrate。")
		}
	}

	plugin.SetInitialized(true)
	plugin.SetRuntimeReady(plugin.CheckStorageReady())
	plugin.UpdatePluginConfig()
	logger.Info("A_Memorix 插件异步初始化成功")
	return nil
}

// runLifecycleSelfCheck 对齐 ensure_runtime_self_check(plugin, force=True)
func runLifecycleSelfCheck(ctx context.Context, plugin LifecyclePlugin, deps *LifecycleDeps) (map[string]any, error) {
	if deps != nil && deps.EnsureSelfCheck != nil {
		return deps.EnsureSelfCheck(ctx, plugin, true)
	}
	params := &utils.SelfCheckParams{
		Config:               plugin.GetConfig,
		VectorStore:          nilIfNilVectorStore(plugin.VectorStore()),
		EmbeddingManager:     newLifecycleEmbeddingManager(plugin.EmbeddingManager()),
		ParagraphVectorStore: nilIfNilVectorStore(plugin.ParagraphVectorStore()),
		GraphVectorStore:     nilIfNilVectorStore(plugin.GraphVectorStore()),
		Logger:               deps.lifecycleLogger(),
	}
	report := utils.EnsureRuntimeSelfCheck(ctx, params, plugin.SelfCheckCache(), true)
	return report.ToDict(), nil
}

// nilIfNilVectorStore 归一 typed-nil 向量存储
func nilIfNilVectorStore(store *storage.VectorStore) utils.VectorStoreSnapshotSource {
	if store == nil {
		return nil
	}
	return store
}

// lifecycleEmbeddingManager 把嵌入适配器包装成自检需要的接口
type lifecycleEmbeddingManager struct {
	adapter *embedding.EmbeddingAPIAdapter
}

// newLifecycleEmbeddingManager 构造自检用嵌入管理器
func newLifecycleEmbeddingManager(adapter *embedding.EmbeddingAPIAdapter) utils.SelfCheckEmbeddingManager {
	if adapter == nil {
		return nil
	}
	return lifecycleEmbeddingManager{adapter: adapter}
}

// DetectDimension 对齐 _detect_dimension
func (m lifecycleEmbeddingManager) DetectDimension(ctx context.Context) (int, error) {
	return m.adapter.DetectDimension(ctx)
}

// Encode 对齐 encode(str) 的单条分支
func (m lifecycleEmbeddingManager) Encode(ctx context.Context, text string) ([]float32, error) {
	return m.adapter.EncodeOne(ctx, text, nil)
}

// resolveLifecycleDataDir 对齐 resolve_repo_path(get_config("storage.data_dir","./data"), fallback=default_data_dir())
func resolveLifecycleDataDir(plugin LifecyclePlugin) string {
	return memorix.ResolveRepoPath(PyStr(plugin.GetConfig("storage.data_dir", "./data")), memorix.DefaultDataDir())
}
