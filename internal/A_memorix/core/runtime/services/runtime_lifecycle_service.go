package services

// 运行时生命周期服务，对齐 Python core/runtime/services/runtime_lifecycle_service.py
// Python 版在本服务内直接构造存储与检索对象；Go 版内核持有全部共享状态，
// 因此这里保留"串行化 + 按序构造 + 失败清理 + 关闭顺序"的编排，
// 具体构造步骤通过 Deps 端口交给 runtime 包实现

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// RuntimeLifecycleDeps 生命周期服务依赖的内核状态与构造端口
type RuntimeLifecycleDeps struct {
	// InitLock 对齐 _runtime_initialization_lock
	InitLock *sync.Mutex
	// Initialized 读取 _initialized 标记
	Initialized func() bool
	// SetInitialized 写入 _initialized 标记
	SetInitialized func(initialized bool)
	// WriterLockHeld 判断数据目录写者锁是否持有，对齐 _runtime_writer_lock.held
	WriterLockHeld func() bool
	// AcquireWriterLock 获取数据目录写者锁
	AcquireWriterLock func() error
	// ReleaseWriterLock 释放数据目录写者锁
	ReleaseWriterLock func()
	// HasWritableStore 判断是否仍有可写存储
	HasWritableStore func() bool
	// EmbeddingDimension 读取配置维度，对齐 self.embedding_dimension
	EmbeddingDimension func() int
	// SetRuntimeCapability 对齐 _set_runtime_capability
	SetRuntimeCapability func(channel string, available bool)
	// ReadRuntimeCapability 读取运行时能力快照
	ReadRuntimeCapability func(channel string) bool
	// VectorHealth 读取向量健康状态，对齐 self._vector_health
	VectorHealth func() map[string]any
	// SetEmbeddingDegraded 对齐 _set_embedding_degraded
	SetEmbeddingDegraded func(active bool, reason string, checkedAt float64)
	// RunStartupFormatMigration 对齐 run_startup_format_migration
	RunStartupFormatMigration func(dataDir string) error
	// CreateEmbeddingAdapter 按 Python 同名参数构造嵌入适配器
	CreateEmbeddingAdapter func(params map[string]any) (any, error)
	// SetEmbeddingManager 写回 embedding_manager
	SetEmbeddingManager func(manager any)
	// ReadActiveVectorSpace 对齐 _vector_space_service._read_active_space
	ReadActiveVectorSpace func() (string, error)
	// ReadVectorSpaceInputs 对齐 _vector_space_service._read_inputs
	ReadVectorSpaceInputs func() (map[string]string, error)
	// StoredVectorDimension 对齐 _stored_vector_dimension
	StoredVectorDimension func() (dimension int, ok bool, err error)
	// OpenMetadataStore 构造并连接元数据存储
	OpenMetadataStore func() error
	// EnsureImageRuntime 构造图片记忆运行时，nil 表示跳过
	EnsureImageRuntime func(imageConfig map[string]any) error
	// RecoverImageAssets 对齐 await image_memory_runtime.recover_assets()
	RecoverImageAssets func() error
	// EnsureGraphStore 构造并装载图存储
	EnsureGraphStore func(matrixFormat string) error
	// ReconcileRelationGraphProjectionJobs 对齐 _reconcile_relation_graph_projection_jobs
	ReconcileRelationGraphProjectionJobs func(resetLeases bool, batchSize int) error
	// EnsureSparseIndex 构造稀疏索引
	EnsureSparseIndex func(sparseConfig map[string]any) error
	// SparseIndexEnabled 读取 self.sparse_index.config.enabled
	SparseIndexEnabled func() bool
	// SparseWarmup 对齐 sparse_index.warmup()
	SparseWarmup func() (map[string]any, error)
	// MakeVectorStores 构造三套向量存储并完成旧目录清理与恢复
	MakeVectorStores func(dimension int) error
	// CleanupStaleDualVectorBuildDirs 对齐 _cleanup_stale_dual_vector_build_dirs
	CleanupStaleDualVectorBuildDirs func()
	// ResumeVectorRecoveryIfNeeded 对齐 _resume_vector_recovery_if_needed，失败必须关闭向量通道
	ResumeVectorRecoveryIfNeeded func(ctx context.Context) error
	// LoadVectorStoresForRuntime 对齐 _embedding_state_service._load_vector_stores_for_runtime
	LoadVectorStoresForRuntime func() error
	// MarkVectorStoresHealthy 对齐 vector_read 可用时的 vector health 初始化
	MarkVectorStoresHealthy func()
	// RecoverKnownVectorFailure 对齐 _recover_known_vector_failure
	RecoverKnownVectorFailure func(err error) bool
	// DisableVectorChannel 对齐 _disable_vector_channel
	DisableVectorChannel func(err error)
	// RefreshRelationWriteService 对齐 _refresh_relation_write_service
	RefreshRelationWriteService func()
	// BuildRuntimeConfig 对齐 _build_runtime_config
	BuildRuntimeConfig func() (map[string]any, error)
	// BuildSearchRuntime 对齐 kernel_module.build_search_runtime
	BuildSearchRuntime func(config map[string]any) (SearchRuntimeBundle, error)
	// ApplySearchRuntimeBundle 把就绪的检索运行时写回内核
	ApplySearchRuntimeBundle func(bundle SearchRuntimeBundle) error
	// ApplyRuntimeSparseMode 对齐 _apply_runtime_sparse_mode
	ApplyRuntimeSparseMode func()
	// RefreshRuntimeDependents 对齐 _refresh_runtime_dependents
	RefreshRuntimeDependents func(preserveManagers bool)
	// CreateTaskManagers 构造导入与调优任务管理器
	CreateTaskManagers func() error
	// VectorPoolsStatus 对齐 _vector_pools_status
	VectorPoolsStatus func() map[string]any
	// MarkStartupSelfCheckDeferred 对齐 _mark_startup_self_check_deferred
	MarkStartupSelfCheckDeferred func()
	// StartBackgroundTasks 对齐 await self._start_background_tasks()
	StartBackgroundTasks func(ctx context.Context) error
	// StopBackgroundTasks 对齐 await self._stop_background_tasks()
	StopBackgroundTasks func(ctx context.Context) error
	// CancelAllBackgroundTasks 取消失败初始化后的全部后台任务
	CancelAllBackgroundTasks func()
	// ShutdownImportManager 对齐 await import_task_manager.shutdown()
	ShutdownImportManager func(ctx context.Context) error
	// ShutdownTuningManager 对齐 await retrieval_tuning_manager.shutdown()
	ShutdownTuningManager func(ctx context.Context) error
	// CloseRuntime 持久化并释放全部存储，对齐 _close_runtime 的收尾段
	CloseRuntime func() error
}

// RuntimeLifecycleOrchestrator 对齐 MemoryRuntimeLifecycleService
type RuntimeLifecycleOrchestrator struct {
	*Base
	Deps RuntimeLifecycleDeps
}

// NewRuntimeLifecycleService 构造生命周期服务
func NewRuntimeLifecycleService(base *Base, deps RuntimeLifecycleDeps) *RuntimeLifecycleOrchestrator {
	if deps.InitLock == nil {
		deps.InitLock = &sync.Mutex{}
	}
	return &RuntimeLifecycleOrchestrator{Base: base, Deps: deps}
}

// Initialize 对齐 initialize，持有初始化锁后串行执行
func (s *RuntimeLifecycleOrchestrator) Initialize(ctx context.Context) error {
	s.Deps.InitLock.Lock()
	defer s.Deps.InitLock.Unlock()
	return s.initializeSerialized(ctx)
}

// initializeSerialized 对齐 _initialize_serialized
func (s *RuntimeLifecycleOrchestrator) initializeSerialized(ctx context.Context) error {
	if s.Deps.Initialized() {
		if !s.Deps.WriterLockHeld() {
			return fmt.Errorf("A_Memorix 活动运行时丢失数据目录写者锁")
		}
		return s.initializeWithWriterLock(ctx)
	}
	if err := s.ensureDataDir(); err != nil {
		return err
	}
	if s.Deps.AcquireWriterLock != nil {
		if err := s.Deps.AcquireWriterLock(); err != nil {
			return err
		}
	}
	if err := s.initializeWithWriterLock(ctx); err != nil {
		s.discardFailedInitialization(ctx)
		if s.Deps.ReleaseWriterLock != nil {
			s.Deps.ReleaseWriterLock()
		}
		return err
	}
	return nil
}

// ensureDataDir 对齐 self.data_dir.mkdir(parents=True, exist_ok=True)
func (s *RuntimeLifecycleOrchestrator) ensureDataDir() error {
	dataDir := s.DataDir()
	if dataDir == "" {
		return nil
	}
	return os.MkdirAll(dataDir, 0o755)
}

// discardFailedInitialization 对齐 _discard_failed_initialization
func (s *RuntimeLifecycleOrchestrator) discardFailedInitialization(ctx context.Context) {
	if s.Deps.StopBackgroundTasks != nil {
		if err := s.Deps.StopBackgroundTasks(ctx); err != nil {
			s.LoggerWarn("初始化失败后的后台任务清理异常: %v", err)
			if s.Deps.CancelAllBackgroundTasks != nil {
				s.Deps.CancelAllBackgroundTasks()
			}
		}
	}
	if s.Deps.ShutdownImportManager != nil {
		if err := s.Deps.ShutdownImportManager(ctx); err != nil {
			s.LoggerWarn("初始化失败后的任务管理器清理异常: %v", err)
		}
	}
	if s.Deps.ShutdownTuningManager != nil {
		if err := s.Deps.ShutdownTuningManager(ctx); err != nil {
			s.LoggerWarn("初始化失败后的任务管理器清理异常: %v", err)
		}
	}
	if s.Deps.CloseRuntime != nil {
		if err := s.Deps.CloseRuntime(); err != nil {
			s.LoggerWarn("初始化失败后的元数据连接清理异常: %v", err)
		}
	}
	if s.Deps.SetInitialized != nil {
		s.Deps.SetInitialized(false)
	}
}

// initializeWithWriterLock 对齐 _initialize_with_writer_lock
func (s *RuntimeLifecycleOrchestrator) initializeWithWriterLock(ctx context.Context) error {
	if s.Deps.Initialized() {
		if s.Deps.ApplyRuntimeSparseMode != nil {
			s.Deps.ApplyRuntimeSparseMode()
		}
		return s.startBackgroundTasks(ctx)
	}
	if err := s.ensureDataDir(); err != nil {
		return err
	}
	if s.Deps.RunStartupFormatMigration != nil {
		if err := s.Deps.RunStartupFormatMigration(s.DataDir()); err != nil {
			s.Logger().Error(fmt.Sprintf("[sdk] 历史格式转换失败，将由各存储通道独立校验并降级: %v", err))
		}
	}
	if err := s.initializeEmbedding(); err != nil {
		return err
	}
	if err := s.readVectorSpaceState(); err != nil {
		return err
	}
	provisionalDimension := s.Deps.EmbeddingDimension()
	if s.Deps.StoredVectorDimension != nil {
		stored, ok, err := s.Deps.StoredVectorDimension()
		if err != nil {
			s.LoggerWarn("读取历史向量维度失败，将使用配置维度并隔离向量加载: %v", err)
		} else if ok && stored != provisionalDimension {
			s.LoggerWarn("历史向量维度与当前配置不同，将保留原世代并等待显式重建: stored=%d, current=%d", stored, provisionalDimension)
		}
	}
	if s.Deps.OpenMetadataStore != nil {
		if err := s.Deps.OpenMetadataStore(); err != nil {
			return err
		}
	}
	s.setCapability("metadata", true)
	if err := s.initializeImageRuntime(); err != nil {
		return err
	}
	s.initializeGraph()
	s.initializeSparse()
	s.initializeVectorStores(ctx, provisionalDimension)
	if s.Deps.RefreshRelationWriteService != nil {
		s.Deps.RefreshRelationWriteService()
	}
	if err := s.initializeSearchRuntime(); err != nil {
		return err
	}
	if s.Deps.RefreshRuntimeDependents != nil {
		s.Deps.RefreshRuntimeDependents(true)
	}
	if s.Deps.CreateTaskManagers != nil {
		if err := s.Deps.CreateTaskManagers(); err != nil {
			return err
		}
	}
	s.logVectorPoolsStatus()
	if s.Deps.MarkStartupSelfCheckDeferred != nil {
		s.Deps.MarkStartupSelfCheckDeferred()
	}
	if s.Deps.SetInitialized != nil {
		s.Deps.SetInitialized(true)
	}
	return s.startBackgroundTasks(ctx)
}

// readVectorSpaceState 对齐读取活动向量空间与输入映射。
// 写回副作用由 Read* 实现负责（native.readActiveVectorSpace / readVectorSpaceInputs）。
func (s *RuntimeLifecycleOrchestrator) readVectorSpaceState() error {
	if s.Deps.ReadActiveVectorSpace != nil {
		if _, err := s.Deps.ReadActiveVectorSpace(); err != nil {
			return err
		}
	}
	if s.Deps.ReadVectorSpaceInputs != nil {
		if _, err := s.Deps.ReadVectorSpaceInputs(); err != nil {
			return err
		}
	}
	return nil
}

// initializeEmbedding 对齐 embedding_manager 构造与降级处理
func (s *RuntimeLifecycleOrchestrator) initializeEmbedding() error {
	if s.Deps.CreateEmbeddingAdapter == nil {
		return nil
	}
	params := map[string]any{
		"batch_size":             intOr(s.Cfg("embedding.batch_size", 32), 32, 1),
		"max_concurrent":         intOr(s.Cfg("embedding.max_concurrent", 5), 5, 1),
		"default_dimension":      s.Deps.EmbeddingDimension(),
		"enable_cache":           pyTruthy(s.Cfg("embedding.enable_cache", false)),
		"model_name":             pyStringOr(s.Cfg("embedding.model_name", "auto"), "auto"),
		"dimension_request_mode": pyStringOr(s.Cfg("embedding.dimension_request_mode", "explicit"), "explicit"),
		"retry_config":           s.CfgMap("embedding.retry"),
	}
	adapter, err := s.Deps.CreateEmbeddingAdapter(params)
	if err != nil {
		if s.Deps.SetEmbeddingManager != nil {
			s.Deps.SetEmbeddingManager(nil)
		}
		s.setCapability("embedding", false)
		if s.Deps.SetEmbeddingDegraded != nil {
			s.Deps.SetEmbeddingDegraded(true, truncateRunes(err.Error(), 500), float64(time.Now().Unix()))
		}
		s.Logger().Error(fmt.Sprintf("[sdk] Embedding 通道初始化失败，核心运行时继续启动: %v", err))
		return nil
	}
	if s.Deps.SetEmbeddingManager != nil {
		s.Deps.SetEmbeddingManager(adapter)
	}
	s.setCapability("embedding", true)
	return nil
}

// initializeImageRuntime 对齐图片记忆运行时的构造
func (s *RuntimeLifecycleOrchestrator) initializeImageRuntime() error {
	imageConfig := s.CfgMap("image_memory")
	if !pyTruthy(imageConfig["enabled"]) && imageConfig["enabled"] != nil {
		return nil
	}
	if s.Deps.EnsureImageRuntime == nil {
		return nil
	}
	if err := s.Deps.EnsureImageRuntime(imageConfig); err != nil {
		return err
	}
	if s.Deps.RecoverImageAssets != nil {
		return s.Deps.RecoverImageAssets()
	}
	return nil
}

// initializeGraph 对齐图谱通道构造，失败时仅关闭图谱能力
func (s *RuntimeLifecycleOrchestrator) initializeGraph() {
	if s.Deps.EnsureGraphStore == nil {
		return
	}
	matrixFormat := normalizeAction(pyStringOr(s.Cfg("graph.sparse_matrix_format", "csr"), "csr"))
	format := "csr"
	if matrixFormat == "csc" {
		format = "csc"
	}
	if err := s.Deps.EnsureGraphStore(format); err != nil {
		s.setCapability("graph", false)
		s.Logger().Error(fmt.Sprintf("[sdk] 图谱通道初始化失败，元数据与其他检索通道继续运行: %v", err))
		return
	}
	if s.Deps.ReconcileRelationGraphProjectionJobs != nil {
		if err := s.Deps.ReconcileRelationGraphProjectionJobs(true, 10000); err != nil {
			s.setCapability("graph", false)
			s.Logger().Error(fmt.Sprintf("[sdk] 图谱通道初始化失败，元数据与其他检索通道继续运行: %v", err))
			return
		}
	}
	s.setCapability("graph", true)
}

// initializeSparse 对齐稀疏索引构造与预热
func (s *RuntimeLifecycleOrchestrator) initializeSparse() {
	if s.Deps.EnsureSparseIndex == nil {
		return
	}
	if err := s.Deps.EnsureSparseIndex(s.CfgMap("retrieval.sparse")); err != nil {
		s.setCapability("sparse", false)
		s.Logger().Error(fmt.Sprintf("[sdk] 稀疏通道初始化失败，其他检索通道继续运行: %v", err))
		return
	}
	enabled := s.Deps.SparseIndexEnabled != nil && s.Deps.SparseIndexEnabled()
	if enabled && s.Deps.SparseWarmup != nil {
		summary, err := s.Deps.SparseWarmup()
		if err != nil {
			s.setCapability("sparse", false)
			s.Logger().Error(fmt.Sprintf("[sdk] 稀疏通道初始化失败，其他检索通道继续运行: %v", err))
			return
		}
		if !pyTruthy(summary["ok"]) {
			s.setCapability("sparse", false)
			s.Logger().Error(fmt.Sprintf("[sdk] 稀疏通道初始化失败，其他检索通道继续运行: %s", pyStringOr(summary["error"], "sparse_warmup_failed")))
			return
		}
		duration, _ := asFloat(summary["duration_ms"])
		s.Logger().Info(fmt.Sprintf(
			"[sdk] 稀疏索引预热完成: backend=%s, docs=%s, duration_ms=%.2f",
			asString(summary["backend"]),
			asString(summary["doc_count"]),
			duration,
		))
	}
	s.setCapability("sparse", enabled)
}

// initializeVectorStores 对齐向量存储构造与失败降级
func (s *RuntimeLifecycleOrchestrator) initializeVectorStores(ctx context.Context, dimension int) {
	if s.Deps.MakeVectorStores == nil {
		return
	}
	if err := s.Deps.MakeVectorStores(dimension); err != nil {
		if s.Deps.RecoverKnownVectorFailure != nil && s.Deps.RecoverKnownVectorFailure(err) {
			return
		}
		if s.Deps.DisableVectorChannel != nil {
			s.Deps.DisableVectorChannel(err)
		}
		return
	}
	if s.Deps.CleanupStaleDualVectorBuildDirs != nil {
		s.Deps.CleanupStaleDualVectorBuildDirs()
	}
	if s.Deps.ResumeVectorRecoveryIfNeeded != nil {
		if err := s.Deps.ResumeVectorRecoveryIfNeeded(ctx); err != nil {
			if s.Deps.DisableVectorChannel != nil {
				s.Deps.DisableVectorChannel(err)
			}
			return
		}
	}
	if s.Deps.LoadVectorStoresForRuntime != nil {
		if err := s.Deps.LoadVectorStoresForRuntime(); err != nil {
			if s.Deps.DisableVectorChannel != nil {
				s.Deps.DisableVectorChannel(err)
			}
			return
		}
	}
	s.setCapability("vector_read", true)
	s.setCapability("vector_write", true)
	if s.Deps.MarkVectorStoresHealthy != nil {
		s.Deps.MarkVectorStoresHealthy()
	}
}

// initializeSearchRuntime 对齐 runtime_config 与 build_search_runtime
func (s *RuntimeLifecycleOrchestrator) initializeSearchRuntime() error {
	if s.Deps.BuildRuntimeConfig == nil {
		return nil
	}
	runtimeConfig, err := s.Deps.BuildRuntimeConfig()
	if err != nil {
		return err
	}
	if s.Deps.BuildSearchRuntime == nil {
		return nil
	}
	bundle, err := s.Deps.BuildSearchRuntime(runtimeConfig)
	if err != nil {
		return err
	}
	if bundle.Ready {
		if s.Deps.ApplySearchRuntimeBundle != nil {
			if err := s.Deps.ApplySearchRuntimeBundle(bundle); err != nil {
				return err
			}
		}
		if s.Deps.ApplyRuntimeSparseMode != nil {
			s.Deps.ApplyRuntimeSparseMode()
		}
		return nil
	}
	if s.Deps.ApplySearchRuntimeBundle != nil {
		if err := s.Deps.ApplySearchRuntimeBundle(SearchRuntimeBundle{}); err != nil {
			return err
		}
	}
	message := bundle.Error
	if message == "" {
		message = "检索通道不可用，元数据核心仍保持运行"
	}
	s.LoggerWarn("%s", message)
	return nil
}

// logVectorPoolsStatus 对齐向量池初始化日志
func (s *RuntimeLifecycleOrchestrator) logVectorPoolsStatus() {
	if s.Deps.VectorPoolsStatus == nil {
		return
	}
	status := s.Deps.VectorPoolsStatus()
	configured := pyStringOr(status["configured_mode"], "single")
	effective := pyStringOr(status["effective_mode"], "single")
	label := effective
	if configured != effective {
		label = fmt.Sprintf("%s(configured=%s)", effective, configured)
	}
	if s.capability("vector_read") {
		s.Logger().Info(fmt.Sprintf("[sdk] 向量存储初始化完成: dim=%d, mode=SQ8, vector_pools=%s", s.Deps.EmbeddingDimension(), label))
		return
	}
	health := map[string]any{}
	if s.Deps.VectorHealth != nil {
		health = s.Deps.VectorHealth()
	}
	s.Logger().Info(fmt.Sprintf(
		"[sdk] 记忆运行时初始化完成，向量通道尚不可用: state=%s, code=%s",
		pyStringOr(health["state"], "not_initialized"),
		asString(health["error_code"]),
	))
}

// startBackgroundTasks 对齐 await self._start_background_tasks()
func (s *RuntimeLifecycleOrchestrator) startBackgroundTasks(ctx context.Context) error {
	if s.Deps.StartBackgroundTasks == nil {
		return nil
	}
	return s.Deps.StartBackgroundTasks(ctx)
}

// Shutdown 对齐 shutdown
func (s *RuntimeLifecycleOrchestrator) Shutdown(ctx context.Context) error {
	s.Deps.InitLock.Lock()
	defer s.Deps.InitLock.Unlock()
	return s.shutdownSerialized(ctx)
}

// shutdownSerialized 对齐 _shutdown_serialized
func (s *RuntimeLifecycleOrchestrator) shutdownSerialized(ctx context.Context) error {
	if s.Deps.StopBackgroundTasks != nil {
		if err := s.Deps.StopBackgroundTasks(ctx); err != nil {
			return err
		}
	}
	var failures []error
	if s.Deps.ShutdownImportManager != nil {
		if err := s.Deps.ShutdownImportManager(ctx); err != nil {
			failures = append(failures, err)
			s.Logger().Error(fmt.Sprintf("关闭导入任务管理器失败: %v", err))
		}
	}
	if s.Deps.ShutdownTuningManager != nil {
		if err := s.Deps.ShutdownTuningManager(ctx); err != nil {
			failures = append(failures, err)
			s.Logger().Error(fmt.Sprintf("关闭调优任务管理器失败: %v", err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("A_Memorix 任务管理器未能安全退出，底层存储保持打开: %w", failures[0])
	}
	return s.closeRuntime()
}

// Close 对齐 close，非活动内核的同步释放
func (s *RuntimeLifecycleOrchestrator) Close() {
	if s.Deps.InitLock != nil && !s.Deps.InitLock.TryLock() {
		s.Logger().Error("A_Memorix 正在初始化或异步关闭，不能同步 close()")
		return
	}
	if s.Deps.InitLock != nil {
		defer s.Deps.InitLock.Unlock()
	}
	if s.Deps.Initialized() {
		s.Logger().Error("A_Memorix 运行时仍处于活动状态，请先 await shutdown()")
		return
	}
	if err := s.closeRuntime(); err != nil {
		s.Logger().Error(fmt.Sprintf("A_Memorix 关闭运行时失败: %v", err))
	}
}

// closeRuntime 对齐 _close_runtime
func (s *RuntimeLifecycleOrchestrator) closeRuntime() error {
	if s.Deps.HasWritableStore != nil && s.Deps.HasWritableStore() {
		if s.Deps.WriterLockHeld == nil || !s.Deps.WriterLockHeld() {
			return fmt.Errorf("A_Memorix 存储仍可写，但数据目录写者锁未持有")
		}
	}
	if s.Deps.CloseRuntime == nil {
		return nil
	}
	return s.Deps.CloseRuntime()
}

// setCapability 对齐 _set_runtime_capability
func (s *RuntimeLifecycleOrchestrator) setCapability(channel string, available bool) {
	if s.Deps.SetRuntimeCapability != nil {
		s.Deps.SetRuntimeCapability(channel, available)
	}
}

// capability 读取运行时能力快照中的单项
func (s *RuntimeLifecycleOrchestrator) capability(channel string) bool {
	if s.Deps.ReadRuntimeCapability != nil {
		return s.Deps.ReadRuntimeCapability(channel)
	}
	return false
}
