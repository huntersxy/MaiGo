package native

// 后台任务、检索命中与向量空间服务的接线
// 这些服务都依赖内核实时状态，端口在调用时读取当前对象，避免初始化顺序耦合

import (
	"context"
	"sync"
	"time"

	memruntime "MaiGo/internal/A_memorix/core/runtime"
	"MaiGo/internal/A_memorix/core/runtime/services"
	"MaiGo/internal/A_memorix/core/storage"
)

// dualMigrationService 懒构造双池迁移服务，状态保存在原生运行时中
func (r *Runtime) dualMigrationService() *services.DualVectorMigrationService {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dualMigration != nil {
		return r.dualMigration
	}
	if r.migrationStatus == nil {
		r.migrationStatus = map[string]any{}
	}
	r.dualMigration = services.NewDualVectorMigrationService(services.DualVectorMigrationDeps{
		Status:       r.migrationStatusSnapshot,
		UpdateStatus: r.mergeMigrationStatus,
		Now:          func() float64 { return float64(time.Now().UnixNano()) / 1e9 },
	})
	return r.dualMigration
}

// migrationStatusSnapshot 返回自动迁移状态副本
func (r *Runtime) migrationStatusSnapshot() map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]any, len(r.migrationStatus))
	for key, value := range r.migrationStatus {
		out[key] = value
	}
	return out
}

// mergeMigrationStatus 合并写回自动迁移状态
func (r *Runtime) mergeMigrationStatus(patch map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.migrationStatus == nil {
		r.migrationStatus = map[string]any{}
	}
	for key, value := range patch {
		r.migrationStatus[key] = value
	}
}

// vectorSpaceAdapter 在目录服务之上补齐双池自动迁移的三个状态方法
type vectorSpaceAdapter struct {
	inner     *services.VectorSpaceService
	migration *services.DualVectorMigrationService
}

var _ memruntime.VectorSpaceService = (*vectorSpaceAdapter)(nil)

func (a *vectorSpaceAdapter) VectorRebuildStatus(ctx context.Context) (map[string]any, error) {
	return a.inner.VectorRebuildStatus(ctx)
}

func (a *vectorSpaceAdapter) VectorStoreSnapshot(ctx context.Context, store any) map[string]any {
	return a.inner.VectorStoreSnapshot(ctx, store)
}

func (a *vectorSpaceAdapter) VectorPoolsStatus(ctx context.Context) map[string]any {
	return a.inner.VectorPoolsStatus(ctx)
}

func (a *vectorSpaceAdapter) ShouldStartDualVectorAutoMigration(ctx context.Context) bool {
	if a.migration == nil {
		return false
	}
	return a.migration.ShouldStartAutoMigration(ctx)
}

func (a *vectorSpaceAdapter) NormalizeDualVectorAutoMigrationProgress(ctx context.Context, progress any) map[string]any {
	if a.migration == nil {
		return map[string]any{}
	}
	return a.migration.NormalizeAutoMigrationProgress(ctx, progress)
}

func (a *vectorSpaceAdapter) UpdateDualVectorAutoMigrationStage(ctx context.Context, stage string, progress map[string]any) {
	if a.migration == nil {
		return
	}
	a.migration.UpdateAutoMigrationStage(ctx, stage, progress)
}

// vectorSpaceDeps 绑定当前向量空间指纹与目录状态
func (r *Runtime) vectorSpaceDeps() services.VectorSpaceDeps {
	return services.VectorSpaceDeps{
		CurrentEmbeddingFingerprintForValidation: func(ctx context.Context, dimension *int) map[string]any {
			return r.kernel.CurrentEmbeddingFingerprintForValidation(dimension)
		},
		StoredEmbeddingFingerprint: func(ctx context.Context) map[string]any {
			return r.kernel.StoredEmbeddingFingerprint(r.vectorStore)
		},
		ActiveVectorSpaceID: func() string {
			space, err := r.readActiveVectorSpace()
			if err != nil {
				return ""
			}
			return space
		},
		TargetVectorSpaceID: func() string { return r.kernel.TargetVectorSpaceID() },
		RebuildLock:         func() *sync.Mutex { return r.kernel.VectorRebuildLock() },
		SaveInputs:          r.kernel.SaveVectorSpaceInputs,
		VectorRebuildStatus: func(ctx context.Context) (map[string]any, error) {
			return r.kernel.VectorRebuildStatus()
		},
		VectorStoreSnapshot: func(ctx context.Context, store any) map[string]any {
			typed, _ := store.(*storage.VectorStore)
			return r.kernel.VectorStoreSnapshot(typed)
		},
		VectorPoolsStatus: func(ctx context.Context) map[string]any {
			return r.vectorPoolsStatus()
		},
	}
}

// backgroundTaskDeps 绑定后台循环依赖的真实端口
func (r *Runtime) backgroundTaskDeps() services.BackgroundTaskDeps {
	migration := r.dualMigrationService()
	return services.BackgroundTaskDeps{
		Cfg:     r.kernel.Cfg,
		Persist: func() { r.persistKernel() },
		Sleep:   sleepContext,
		MaintenanceLoop: func(ctx context.Context) error {
			return r.kernel.MemoryMaintenanceLoop(ctx)
		},
		DualVectorPoolsEnabled: func(ctx context.Context) bool {
			return r.kernel.DualVectorPoolsEnabled()
		},
		ShouldStartDualVectorAutoMigration: migration.ShouldStartAutoMigration,
		RebuildAllVectors: func(ctx context.Context) (map[string]any, error) {
			return r.kernel.RebuildAllVectors()
		},
		VectorRebuildLocked: func() bool {
			if lock := r.kernel.VectorRebuildLock(); lock != nil {
				if lock.TryLock() {
					lock.Unlock()
					return false
				}
				return true
			}
			return false
		},
		NormalizeAutoMigrationProgress:  migration.NormalizeAutoMigrationProgress,
		UpdateAutoMigrationStage:        migration.UpdateAutoMigrationStage,
		AutoMigrationStatus:             r.migrationStatusSnapshot,
		SetAutoMigrationStatus:          r.mergeMigrationStatus,
		DualVectorMigrationInitialDelay: func() float64 { return memruntime.DualVectorAutoMigrationInitialDelaySeconds },
		DualVectorMigrationLockRetries: func() []float64 {
			return append([]float64(nil), memruntime.DualVectorAutoMigrationLockRetryDelaysSeconds...)
		},
		EmbeddingManagerPresent: func() bool { return r.embeddingManager != nil },
		RecoverEmbeddingOnce: func(ctx context.Context) {
			// 首轮探测立即执行，与 Python _embedding_probe_loop 一致
			if r.embeddingManager == nil {
				return
			}
			result, err := r.kernel.RecoverEmbeddingOnce("A_Memorix runtime self check")
			if err != nil {
				r.logger.Warning("[native] embedding 恢复探测失败: " + err.Error())
				return
			}
			if memruntime.PyTruthy(result["recovered"]) {
				r.logger.Info("[native] embedding 恢复探测已恢复向量通道")
			}
		},
		LockVectorRebuild: func() (func(), bool) {
			lock := r.kernel.VectorRebuildLock()
			if lock == nil {
				return func() {}, true
			}
			// 与 Python async with self._vector_rebuild_lock 一致：首轮已判锁后等待释放
			lock.Lock()
			return lock.Unlock, true
		},
		ParagraphBackfillEnabled: func() bool {
			return r.kernel.ParagraphVectorBackfillEnabled()
		},
		IsEmbeddingDegraded: func() bool {
			return memruntime.PyTruthy(r.kernel.EmbeddingDegraded()["active"])
		},
		RunParagraphBackfillOnce: func(ctx context.Context, limit int, maxRetry int, trigger string) {
			if _, err := r.runParagraphBackfillOnce(ctx, &limit, &maxRetry, trigger); err != nil {
				r.logger.Warning("[native] 段落向量回填失败: " + err.Error())
			}
		},
		ProcessEpisodeSourceRebuildBatch: r.kernel.ProcessEpisodeSourceRebuildBatch,
		ProcessPersonProfileRefreshQueue: func(ctx context.Context, limit int) (map[string]any, error) {
			return r.kernel.ProcessPersonProfileRefreshQueueBatch(limit)
		},
		ProcessImageJobsOnce: func(ctx context.Context) error {
			if r.imageRuntime == nil {
				return nil
			}
			_, err := r.imageRuntime.ProcessJobsOnce(ctx)
			return err
		},
		HasLegacyVectorView: func() bool { return r.legacyVectorView != nil },
		CopyLegacyVectorsOnce: func(ctx context.Context, batchSize int) (map[string]any, error) {
			return r.kernel.CopyLegacyVectorsOnce(batchSize)
		},
		CleanupVectorQuarantine: func() {
			if _, err := r.kernel.CleanupVectorQuarantine(); err != nil {
				r.logger.Warning("[native] 清理向量隔离区失败: " + err.Error())
			}
		},
		// 对齐 Python _storage_cleanup_loop：只对账关系图投影作业，不跑完整维护周期
		MaintenanceReconcile: func() {
			if err := r.reconcileRelationGraphProjectionJobs(false, 0); err != nil {
				r.logger.Warning("relation graph projection loop 异常，将在下一轮重试: " + err.Error())
			}
		},
		VectorStores: func(ctx context.Context) map[string]*storage.VectorStore {
			// 与 Python _runtime_vector_training_targets 一致：双池只训练段落/图谱池，单池只训练主池
			if r.kernel.DualVectorPoolsEnabled() {
				return map[string]*storage.VectorStore{
					"paragraph": r.paragraphVectorStore, "graph": r.graphVectorStore,
				}
			}
			return map[string]*storage.VectorStore{"single": r.vectorStore}
		},
		SaveVectorStore: func(ctx context.Context, store *storage.VectorStore) {
			if store == nil {
				return
			}
			fingerprint := r.kernel.CurrentEmbeddingFingerprint(nil)
			if err := store.Save("", fingerprint); err != nil {
				r.logger.Warning("[native] 保存向量存储失败: " + err.Error())
			}
		},
		VectorPersistBlockedUntilRebuild: func() bool { return r.kernel.VectorPersistBlockedUntilRebuild() },
		EmbeddingProbeInterval: func() float64 {
			return r.kernel.EmbeddingProbeIntervalSeconds()
		},
		ParagraphBackfillIntervalSeconds: func() float64 {
			return r.kernel.ParagraphVectorBackfillIntervalSeconds()
		},
		ParagraphBackfillBatchSize: func() int {
			return r.kernel.ParagraphVectorBackfillBatchSize()
		},
		ParagraphBackfillMaxRetry: func() int {
			return r.kernel.ParagraphVectorBackfillMaxRetry()
		},
		ActivePersonTimestamps:         r.activePersonTimestamps,
		HasPendingPersonProfileRefresh: r.kernel.HasPendingPersonProfileRefresh,
		RefreshPersonProfile: func(ctx context.Context, personID string, limit int) (map[string]any, error) {
			// 对齐 Python background_task_service：后台刷新 mark_active=False
			return r.kernel.RefreshPersonProfile(ctx, personID, limit, false)
		},
		ProfileRefreshQueueInterval: func() float64 {
			return r.kernel.PersonProfileRefreshQueueIntervalSeconds()
		},
		ProfileRefreshQueueBatchSize: func() int {
			return r.kernel.PersonProfileRefreshQueueBatchSize()
		},
		ProcessStorageCleanup: func(ctx context.Context, limit int) (map[string]any, error) {
			servicesRef := r.kernel.Services()
			if servicesRef == nil || servicesRef.DeleteAdmin == nil {
				return map[string]any{"completed": 0, "cancelled": 0, "failed": 0, "deleted_vectors": 0}, nil
			}
			return servicesRef.DeleteAdmin.ProcessPendingStorageCleanupJobs(ctx, "", limit)
		},
		HasImageRuntime:       func() bool { return r.imageRuntime != nil },
		BackgroundStopping:    func() bool { return r.stopping.Load() },
		SetBackgroundStopping: func(value bool) { r.stopping.Store(value) },
	}
}

// persistKernel 持久化内核状态并记录失败
func (r *Runtime) persistKernel() {
	if err := r.kernel.Persist(context.Background(), false); err != nil {
		r.logger.Warning("[native] 持久化内核状态失败: " + err.Error())
	}
}

// sleepContext 可取消的秒级等待
func sleepContext(ctx context.Context, seconds float64) error {
	if seconds <= 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// searchHitDeps 绑定检索命中处理依赖
func (r *Runtime) searchHitDeps() services.SearchHitDeps {
	cfg := r.kernel.Cfg
	return services.SearchHitDeps{
		Metadata: func() *storage.Store { return r.metadataStore },
		Cfg:      cfg,
		ChatFilterConfigAllows: func(filterConfig map[string]any, streamID string, groupID string, userID string, defaultWhenEmpty bool) bool {
			return r.kernel.ChatFilterConfigAllows(context.Background(), filterConfig, streamID, groupID, userID, defaultWhenEmpty)
		},
		EpisodeQueryBlockEnabled: func() bool {
			return memruntime.PyTruthy(cfg("feedback_correction.episode_query_block_enabled", true))
		},
		ParagraphHardFilterEnabled: func() bool {
			return memruntime.PyTruthy(cfg("feedback_correction.paragraph_hard_filter_enabled", true))
		},
		SessionLookup: func(streamID string) (string, string) {
			if r.sessionLookup == nil {
				return "", ""
			}
			return r.sessionLookup(streamID)
		},
		ProjectFactParagraph:   r.projectFactParagraph,
		FactRelationHasSupport: r.factRelationHasSupport,
		Now:                    func() float64 { return float64(time.Now().UnixNano()) / 1e9 },
		EffectiveFilterCache: func() map[string]any {
			return r.kernel.EffectiveFilterCache()
		},
	}
}

// filterHitsBySearchHit 调用命中服务过滤，失败时保留原命中并记录真实错误
func (r *Runtime) filterHitsBySearchHit(method string, hits []map[string]any, extra map[string]any) []map[string]any {
	service := r.kernel.SearchHitService()
	if service == nil {
		return hits
	}
	payload := map[string]any{"hits": hits}
	for key, value := range extra {
		payload[key] = value
	}
	var (
		result any
		err    error
	)
	switch method {
	case "episode":
		result, err = service.FilterEpisodeHits(context.Background(), payload)
	case "person":
		result, err = service.FilterHits(context.Background(), payload)
	case "visible":
		result, err = service.FilterUserVisibleHits(context.Background(), payload)
	case "scope":
		result, err = service.FilterHitsByRetrievalTypeScope(context.Background(), payload)
	default:
		return hits
	}
	if err != nil {
		r.logger.Warning("[native] 检索命中过滤失败: " + err.Error())
		return hits
	}
	return anyRows(result)
}
