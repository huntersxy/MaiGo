package native

// 生命周期端口实现，对齐 Python RuntimeLifecycleService 的 deps 语义
// 端口直接操作本包装配出的真实对象，缺失能力显式记录降级

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/A_memorix/core/embedding"
	"MaiGo/internal/A_memorix/core/retrieval"
	memruntime "MaiGo/internal/A_memorix/core/runtime"
	"MaiGo/internal/A_memorix/core/runtime/services"
	"MaiGo/internal/A_memorix/core/storage"
)

// lifecycleDeps 绑定内核状态与本包存储对象
func (r *Runtime) lifecycleDeps() services.RuntimeLifecycleDeps {
	kernel := r.kernel
	return services.RuntimeLifecycleDeps{
		InitLock:          &r.initLock,
		Initialized:       kernel.Initialized,
		SetInitialized:    kernel.SetInitialized,
		WriterLockHeld:    r.writerLock.Held,
		AcquireWriterLock: r.writerLock.Acquire,
		ReleaseWriterLock: func() {
			if err := r.writerLock.Release(); err != nil {
				r.logger.Warning(fmt.Sprintf("[native] 释放数据目录写者锁失败: %v", err))
			}
		},
		HasWritableStore: func() bool {
			return r.metadataStore != nil || r.vectorStore != nil || r.graphStore != nil
		},
		EmbeddingDimension: kernel.EmbeddingDimension,
		SetRuntimeCapability: func(channel string, available bool) {
			if err := kernel.SetRuntimeCapability(channel, available); err != nil {
				r.logger.Warning(fmt.Sprintf("[native] 写入运行时能力失败: %v", err))
			}
		},
		ReadRuntimeCapability: func(channel string) bool { return kernel.RuntimeCapabilities()[channel] },
		VectorHealth:          kernel.VectorHealth,
		SetEmbeddingDegraded: func(active bool, reason string, checkedAt float64) {
			kernel.SetEmbeddingDegraded(active, reason, &checkedAt)
		},
		RunStartupFormatMigration: func(dataDir string) error {
			_, err := storage.RunStartupFormatMigration(dataDir)
			return err
		},
		CreateEmbeddingAdapter: r.createEmbeddingAdapter,
		SetEmbeddingManager: func(manager any) {
			adapter, _ := manager.(*embedding.EmbeddingAPIAdapter)
			r.embeddingManager = adapter
			r.kernel.SetEmbeddingManager(adapter)
		},
		ReadActiveVectorSpace:                r.readActiveVectorSpace,
		ReadVectorSpaceInputs:                r.readVectorSpaceInputs,
		StoredVectorDimension:                r.storedVectorDimension,
		OpenMetadataStore:                    r.openMetadataStore,
		EnsureImageRuntime:                   r.ensureImageRuntime,
		RecoverImageAssets:                   r.recoverImageAssets,
		EnsureGraphStore:                     r.ensureGraphStore,
		ReconcileRelationGraphProjectionJobs: r.reconcileRelationGraphProjectionJobs,
		EnsureSparseIndex:                    r.ensureSparseIndex,
		SparseIndexEnabled:                   func() bool { return r.sparseEnabled },
		SparseWarmup:                         r.sparseWarmup,
		MakeVectorStores:                     r.makeVectorStores,
		CleanupStaleDualVectorBuildDirs:      r.kernel.CleanupStaleDualVectorBuildDirs,
		ResumeVectorRecoveryIfNeeded: func(ctx context.Context) error {
			return r.kernel.ResumeVectorRecoveryIfNeeded()
		},
		LoadVectorStoresForRuntime:   r.loadVectorStoresForRuntime,
		MarkVectorStoresHealthy:      r.markVectorStoresHealthy,
		RecoverKnownVectorFailure:    func(err error) bool { return r.kernel.RecoverKnownVectorFailure(err) },
		DisableVectorChannel:         r.disableVectorChannel,
		RefreshRelationWriteService:  r.refreshRelationWriteService,
		BuildRuntimeConfig:           r.buildRuntimeConfig,
		BuildSearchRuntime:           r.buildSearchRuntime,
		ApplySearchRuntimeBundle:     r.applySearchRuntimeBundle,
		ApplyRuntimeSparseMode:       kernel.ApplyRuntimeSparseMode,
		RefreshRuntimeDependents:     r.refreshRuntimeDependents,
		CreateTaskManagers:           r.createTaskManagers,
		VectorPoolsStatus:            r.vectorPoolsStatus,
		MarkStartupSelfCheckDeferred: kernel.MarkStartupSelfCheckDeferred,
		StartBackgroundTasks:         r.startBackgroundTasks,
		StopBackgroundTasks:          r.stopBackgroundTasks,
		CancelAllBackgroundTasks:     r.kernel.CancelAllBackgroundTasks,
		ShutdownImportManager:        r.shutdownImportManager,
		ShutdownTuningManager:        r.shutdownTuningManager,
		CloseRuntime:                 r.closeRuntime,
	}
}

// createTaskManagers 在检索运行时就绪后构造导入与调优管理器并重建服务表
func (r *Runtime) createTaskManagers() error {
	r.refreshRuntimeDependents(true)
	r.ensureImportManager()
	r.ensureTuningManager()
	r.startTaskWorkers()
	r.rebuildServices()
	return nil
}

// buildRuntimeConfig 复用运行时配置服务构造带组件引用的配置
func (r *Runtime) buildRuntimeConfig() (map[string]any, error) {
	service := services.NewRuntimeConfigService(r.runtimeConfigDeps())
	return service.BuildRuntimeConfig(ensureContext(nil), r.kernel.Config())
}

// buildSearchRuntime 调用导出的统一初始化器构造检索器与阈值过滤器
// 必须显式注入当前运行时存储，否则初始化器会从全局组件表取到切换前的旧引用
func (r *Runtime) buildSearchRuntime(config map[string]any) (services.SearchRuntimeBundle, error) {
	enriched := make(map[string]any, len(config)+8)
	for key, value := range config {
		enriched[key] = value
	}
	if r.vectorStore != nil {
		enriched["vector_store"] = r.vectorStore
	}
	if r.paragraphVectorStore != nil {
		enriched["paragraph_vector_store"] = r.paragraphVectorStore
	}
	if r.graphVectorStore != nil {
		enriched["graph_vector_store"] = r.graphVectorStore
	}
	if r.legacyVectorView != nil {
		enriched["legacy_vector_store"] = r.legacyVectorView
	}
	if r.graphStore != nil {
		enriched["graph_store"] = r.graphStore
	}
	if r.metadataStore != nil {
		enriched["metadata_store"] = r.metadataStore
	}
	if r.embeddingManager != nil {
		enriched["embedding_manager"] = r.embeddingManager
	}
	if r.sparseIndex != nil {
		enriched["sparse_index"] = r.sparseIndex
	}
	bundle := memruntime.BuildSearchRuntimeWithOptions(enriched, r.kernel.Logger(), "sdk_kernel", memruntime.SearchRuntimeOptions{
		LogPrefix:             "[sdk]",
		DualVectorPoolsEnable: func() bool { return r.kernel.DualVectorPoolsEnabled() },
	})
	out := services.SearchRuntimeBundle{Ready: bundle.Ready(), Error: bundle.Error}
	if bundle.Retriever != nil {
		out.Retriever = bundle.Retriever
	}
	if bundle.ThresholdFilter != nil {
		out.ThresholdFilter = bundle.ThresholdFilter
	}
	if bundle.SparseIndex != nil {
		out.SparseIndex = bundle.SparseIndex
	}
	return out, nil
}

// applySearchRuntimeBundle 把检索运行时写回内核，未就绪时清空
func (r *Runtime) applySearchRuntimeBundle(bundle services.SearchRuntimeBundle) error {
	retriever, _ := bundle.Retriever.(*retrieval.DualPathRetriever)
	threshold, _ := bundle.ThresholdFilter.(*retrieval.DynamicThresholdFilter)
	sparse, _ := bundle.SparseIndex.(*retrieval.SparseBM25Index)
	legacyView := memruntime.NewLegacyVectorSearcher(r.legacyVectorView)
	full := memruntime.SearchRuntimeBundle{
		VectorStore:          r.vectorStore,
		ParagraphVectorStore: r.paragraphVectorStore,
		GraphVectorStore:     r.graphVectorStore,
		LegacyVectorStore:    legacyView,
		GraphStore:           r.graphStore,
		MetadataStore:        r.metadataStore,
		EmbeddingManager:     r.embeddingManager,
		SparseIndex:          sparse,
		Retriever:            retriever,
		ThresholdFilter:      threshold,
		Error:                bundle.Error,
	}
	r.retriever = retriever
	r.thresholdFilter = threshold
	if sparse != nil {
		r.sparseIndex = sparse
	}
	r.runtimeBundle = full
	r.kernel.SetRuntimeBundle(full)
	r.kernel.SetRetriever(retriever)
	r.kernel.SetThresholdFilter(threshold)
	if sparse != nil {
		r.kernel.SetSparseIndex(sparse)
	}
	if retriever == nil {
		r.markDegraded("检索运行时未就绪，仅元数据与已装配通道可用")
	}
	return nil
}

// refreshRuntimeDependents 重新绑定存储引用并刷新依赖服务
func (r *Runtime) refreshRuntimeDependents(preserveManagers bool) {
	r.kernel.SetVectorStore(r.vectorStore)
	r.kernel.SetParagraphVectorStore(r.paragraphVectorStore)
	r.kernel.SetGraphVectorStore(r.graphVectorStore)
	r.kernel.SetGraphStore(r.graphStore)
	r.kernel.SetMetadataStore(r.metadataStore)
	r.kernel.SetSparseIndex(r.sparseIndex)
	r.kernel.SetRelationWriteService(r.relationWriteService)
	r.refreshRelationWriteService()
	// 不能回调 kernel.RefreshRuntimeDependents：该入口会再次进入向量运行时服务形成递归
	// 服务端口都按调用时读取运行时字段，这里只需重绑引用与关系写入服务
}

// startBackgroundTasks 启动后台任务，服务缺失时如实记录
func (r *Runtime) startBackgroundTasks(ctx context.Context) error {
	r.stopping.Store(false)
	if r.kernel.Services().BackgroundTask == nil {
		r.markDegraded("后台任务服务缺失，记忆维护与双池迁移循环未启动")
		return nil
	}
	return r.kernel.StartBackgroundTasks(ctx)
}

// stopBackgroundTasks 停止后台任务
func (r *Runtime) stopBackgroundTasks(ctx context.Context) error {
	r.stopping.Store(true)
	return r.kernel.StopBackgroundTasks(ctx)
}

// runtimeConfigDeps 绑定当前内核状态，供配置服务构造运行时配置
func (r *Runtime) runtimeConfigDeps() services.RuntimeConfigDeps {
	capabilities := map[string]any{}
	for channel, available := range r.kernel.RuntimeCapabilities() {
		capabilities[channel] = available
	}
	deps := services.RuntimeConfigDeps{
		Config:                 func() map[string]any { return r.kernel.Config() },
		DualVectorPoolsEnabled: func() bool { return r.kernel.DualVectorPoolsEnabled() },
		VectorStore:            storeRef(r.vectorStore),
		LegacyVectorStore:      storeRef(r.legacyVectorView),
		ParagraphVectorStore:   storeRef(r.paragraphVectorStore),
		GraphVectorStore:       storeRef(r.graphVectorStore),
		GraphStore:             storeRef(r.graphStore),
		MetadataStore:          storeRef(r.metadataStore),
		EmbeddingManager:       storeRef(r.embeddingManager),
		SparseIndex:            storeRef(r.sparseIndex),
		RelationWriteService:   r.relationWriteService,
		RecordEmbeddingInput: func(itemType string, itemID string, text string) {
			r.kernel.RecordVectorSpaceInput(itemType, itemID, text)
		},
		RuntimeFacade:       r.kernel.RuntimeFacade(),
		RuntimeCapabilities: capabilities,
		BuildSearchRuntime: func(config map[string]any) (*services.SearchRuntimeBundle, error) {
			bundle, err := r.buildSearchRuntime(config)
			if err != nil {
				return nil, err
			}
			return &bundle, nil
		},
		ApplyRuntimeBundle: func(bundle *services.SearchRuntimeBundle) {
			if bundle == nil {
				_ = r.applySearchRuntimeBundle(services.SearchRuntimeBundle{})
				return
			}
			if err := r.applySearchRuntimeBundle(*bundle); err != nil {
				r.logger.Warning(fmt.Sprintf("[native] 写回检索运行时失败: %v", err))
			}
		},
		RefreshRuntimeDependents: r.refreshRuntimeDependents,
		ApplyRuntimeSparseMode:   r.kernel.ApplyRuntimeSparseMode,
	}
	if r.sparseIndex != nil {
		deps.SparseIndex = r.sparseIndex
	}
	return deps
}

// runtimeDependencyDeps 绑定持久化回调
func (r *Runtime) runtimeDependencyDeps() services.RuntimeDependencyDeps {
	return services.RuntimeDependencyDeps{
		MetadataStore:          storeRef(r.metadataStore),
		VectorStore:            storeRef(r.vectorStore),
		ParagraphVectorStore:   storeRef(r.paragraphVectorStore),
		GraphVectorStore:       storeRef(r.graphVectorStore),
		GraphStore:             storeRef(r.graphStore),
		DualVectorPoolsEnabled: func() bool { return r.kernel.DualVectorPoolsEnabled() },
		BuildRelationWriteService: func() any {
			return r.relationWriteService
		},
		SetRelationWriteService: func(service any) {
			if service == nil {
				r.relationWriteService = nil
				r.kernel.SetRelationWriteService(nil)
				return
			}
			if typed, ok := service.(memruntime.RelationWriteService); ok {
				r.relationWriteService = typed
				r.kernel.SetRelationWriteService(typed)
			}
		},
		RebuildRuntimeDependents: r.refreshRuntimeDependents,
		VectorRebuildStatus:      r.vectorRebuildStatus,
		SaveVectorStore: func(store any) {
			// 对齐 Python _save_vector_store：只判断 store 是否为空，不用 has_data 拦截新增向量
			typed, ok := store.(*storage.VectorStore)
			if !ok || typed == nil {
				return
			}
			if err := typed.Save(typed.DataDir(), nil); err != nil {
				r.logger.Warning(fmt.Sprintf("[native] 持久化向量库失败: %v", err))
			}
		},
		SaveVectorInputs: func() {
			if err := r.kernel.SaveVectorSpaceInputs(); err != nil {
				r.logger.Warning(fmt.Sprintf("[native] 持久化向量空间输入映射失败: %v", err))
			}
		},
		SaveGraphStore: func() {
			// 对齐 Python _persist：图存储非空指针即提交，空图也要落元数据
			if r.graphStore == nil {
				return
			}
			if err := r.graphStore.Save(r.graphStore.DataDir()); err != nil {
				r.logger.Warning(fmt.Sprintf("[native] 持久化图存储失败: %v", err))
			}
		},
		SparseIndexEnabled: func() bool { return r.sparseEnabled },
		SparseIndexEnsureLoaded: func() {
			if r.sparseIndex == nil {
				return
			}
			if summary := r.sparseIndex.Warmup(nil, ""); !memruntime.PyTruthy(summary["ok"]) {
				r.logger.Warning(fmt.Sprintf("[native] 稀疏索引懒加载预热失败: %v", summary["error"]))
			}
		},
		ImageRuntimePersist: func() {
			if runtime := r.imageRuntime; runtime != nil {
				if store := runtime.VectorStore(); store != nil && store.HasData() {
					if err := store.Save(store.DataDir(), runtime.Fingerprint()); err != nil {
						r.logger.Warning(fmt.Sprintf("[native] 持久化图片向量库失败: %v", err))
					}
				}
			}
		},
		Logger: slogLoggerOrNil(),
	}
}

// statsDeps 绑定元数据统计查询，存储未打开时返回真实错误
func (r *Runtime) statsDeps() services.StatsDeps {
	requireStore := func() (*storage.Store, error) {
		if r.metadataStore == nil {
			return nil, fmt.Errorf("metadata store unavailable")
		}
		return r.metadataStore, nil
	}
	return services.StatsDeps{
		GetStatistics: func() (map[string]int, error) {
			store, err := requireStore()
			if err != nil {
				return nil, err
			}
			return store.GetStatistics()
		},
		GetParagraphBackfillCounts: func() (map[string]int, error) {
			store, err := requireStore()
			if err != nil {
				return nil, err
			}
			return store.GetParagraphVectorBackfillStatusCounts(nil)
		},
		GetEpisodeRebuildSummary: func(failedLimit int) (map[string]any, error) {
			store, err := requireStore()
			if err != nil {
				return nil, err
			}
			summary, err := store.GetEpisodeSourceRebuildSummary(failedLimit, nil)
			if err != nil {
				return nil, err
			}
			counts := map[string]any{}
			switch typed := summary["counts"].(type) {
			case map[string]int:
				for key, value := range typed {
					counts[key] = value
				}
			case map[string]any:
				for key, value := range typed {
					counts[key] = value
				}
			}
			summary["counts"] = counts
			return summary, nil
		},
		Query: func(sql string, params ...any) ([]map[string]any, error) {
			store, err := requireStore()
			if err != nil {
				return nil, err
			}
			return store.Query(sql, params...)
		},
		LastMaintenanceAt: func() *float64 {
			r.maintenanceMu.Lock()
			defer r.maintenanceMu.Unlock()
			value := r.lastMaintenance
			return &value
		},
	}
}

// describeMissing 拼接缺失服务描述
func describeMissing(missing []string) string {
	if len(missing) == 0 {
		return "none"
	}
	return strings.Join(missing, ",")
}
