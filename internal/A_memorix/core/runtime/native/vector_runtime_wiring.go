package native

// 向量运行时与向量恢复服务的依赖装配
// 端口直接读取内核实时状态，重建/回填等动作交给内核或原生回填执行器

import (
	"context"
	"fmt"

	"maibot-go/internal/A_memorix/core/runtime/services"
	"maibot-go/internal/A_memorix/core/storage"
)

// recoverEmbeddingOnce 直接调用 embedding 状态服务，避免经内核回环递归
func (r *Runtime) recoverEmbeddingOnce(ctx context.Context, sampleText string) (map[string]any, error) {
	if r.embeddingState == nil {
		return map[string]any{"success": false, "error": "embedding 状态服务未初始化"}, nil
	}
	return r.embeddingState.RecoverEmbeddingOnce(ctx, sampleText)
}

// vectorRuntimeDeps 绑定向量重建、回填与运行时管理动作
func (r *Runtime) vectorRuntimeDeps() services.VectorRuntimeDeps {
	return services.VectorRuntimeDeps{
		Metadata: func() *storage.Store { return r.metadataStore },
		Cfg:        r.kernel.Cfg,
		FullConfig: r.kernel.Config,
		Persist:  r.persistKernel,
		StoredVectorDimension: func() *int {
			return r.kernel.StoredVectorDimension(r.vectorStore)
		},
		CurrentEmbeddingStatusDimension:  r.kernel.CurrentEmbeddingStatusDimension,
		VectorPersistBlockedUntilRebuild: r.kernel.VectorPersistBlockedUntilRebuild,
		VectorRebuildSourceDimension:     r.kernel.VectorRebuildSourceDimension,
		CurrentEmbeddingFingerprintForValidation: func() map[string]any {
			return r.kernel.CurrentEmbeddingFingerprintForValidation(nil)
		},
		StoredEmbeddingFingerprint: func() map[string]any {
			return r.kernel.StoredEmbeddingFingerprint(r.vectorStore)
		},
		EmbeddingFingerprintStatus: r.kernel.EmbeddingFingerprintStatus,
		VectorMismatchError:        r.kernel.VectorMismatchError,
		ParagraphBackfillCounts:    r.kernel.ParagraphVectorBackfillCounts,
		RunParagraphBackfillOnce:   r.runParagraphBackfillOnce,
		RecoverEmbeddingOnce:       r.recoverEmbeddingOnce,
		RefreshRuntimeSelfCheck: func(ctx context.Context, sampleText string) (map[string]any, error) {
			return r.kernel.RefreshRuntimeSelfCheck(sampleText)
		},
		ApplySelfCheckDimensionResult: r.kernel.ApplySelfCheckDimensionResult,
		SetEmbeddingDegraded: func(active bool, reason string, checkedAt *float64) {
			r.kernel.SetEmbeddingDegraded(active, reason, checkedAt)
		},
		IsEmbeddingDegraded:       r.kernel.IsEmbeddingDegraded,
		EmbeddingDegradedSnapshot: r.kernel.EmbeddingDegradedSnapshot,
		EmbeddingFallbackEnabled:  r.kernel.EmbeddingFallbackEnabled,
		RuntimeCapabilityStatus:   r.kernel.RuntimeCapabilityStatus,
		SetCfg:                    r.kernel.SetCfg,
		VectorStore:               func() *storage.VectorStore { return r.vectorStore },
		ParagraphVectorStore:      func() *storage.VectorStore { return r.paragraphVectorStore },
		GraphVectorStore:          func() *storage.VectorStore { return r.graphVectorStore },
		VectorPoolMode:            func(ctx context.Context) string { return r.kernel.VectorPoolMode() },
		DualVectorPoolsEnabled:    func(ctx context.Context) bool { return r.kernel.DualVectorPoolsEnabled() },
		DualVectorReadyManifestPath: func(ctx context.Context) string {
			return r.kernel.DualVectorReadyManifestPath()
		},
		DualVectorAutoMigrationStatus: r.migrationStatusSnapshot,
		WriteDualVectorReadyManifest: func(ctx context.Context) {
			r.kernel.RefreshDualVectorReadyManifestFromStores()
		},
		RefreshRuntimeDependents: func(ctx context.Context, preserveManagers bool) {
			r.refreshRuntimeDependents(preserveManagers)
		},
		BackfillMissingDualVectorPoolEntries: func(ctx context.Context, batchSize int) (map[string]any, error) {
			return r.BackfillMissingDualVectorPoolEntriesNative(ctx, batchSize)
		},
		EncodeAndAddRebuildVectors: func(ctx context.Context, params map[string]any) (map[string]any, error) {
			return r.EncodeAndAddRebuildVectorsNative(ctx, params)
		},
		CopyOrEncodeDualRebuildVectors: func(ctx context.Context, params map[string]any) (map[string]any, error) {
			return r.CopyOrEncodeDualRebuildVectorsNative(ctx, params)
		},
		RebuildAllVectors: func(ctx context.Context, options map[string]any) (map[string]any, error) {
			return r.RebuildAllVectorsNative(ctx, options)
		},
		RebuildAllVectorsLocked: func(ctx context.Context, options map[string]any) (map[string]any, error) {
			return r.RebuildAllVectorsLockedNative(ctx, options)
		},
		RelationVectorsEnabled: func() bool { return r.kernel.RelationVectorsEnabled() },
		DetectEmbeddingDimension: func(ctx context.Context) (int, error) {
			if r.embeddingManager == nil {
				return 0, fmt.Errorf("embedding 管理器未初始化")
			}
			return r.embeddingManager.DetectDimension(ctx)
		},
		ListVectorSpaces: func(ctx context.Context) (map[string]any, error) {
			if r.vectorSpace == nil {
				return map[string]any{"success": false, "items": []any{}}, nil
			}
			items, err := r.vectorSpace.ListSpaces(ctx, r.kernel.TargetVectorSpaceID())
			if err != nil {
				return nil, err
			}
			return map[string]any{"success": true, "items": items}, nil
		},
		DeleteVectorSpace: func(ctx context.Context, spaceID string) (map[string]any, error) {
			if r.vectorSpace == nil {
				return map[string]any{"success": false, "error": "向量空间服务未初始化"}, nil
			}
			return r.vectorSpace.DeleteSpace(ctx, spaceID)
		},
	}
}

// vectorRecoveryDeps 绑定向量健康、旧视图与恢复动作
func (r *Runtime) vectorRecoveryDeps() services.VectorRecoveryDeps {
	return services.VectorRecoveryDeps{
		Metadata:           func() *storage.Store { return r.metadataStore },
		EmbeddingDimension: r.kernel.EmbeddingDimension,
		CurrentEmbeddingFingerprint: func() map[string]any {
			return r.kernel.CurrentEmbeddingFingerprint(nil)
		},
		VectorHealth:    r.kernel.VectorHealth,
		SetVectorHealth: r.kernel.ApplyVectorHealthPatch,
		SetRuntimeCapability: func(kind string, enabled bool) {
			_ = r.kernel.SetRuntimeCapability(kind, enabled)
		},
		DualVectorPoolsReady:    r.kernel.DualVectorPoolsReady,
		SetDualVectorPoolsReady: r.kernel.SetDualVectorPoolsReady,
		VectorStore:             func() *storage.VectorStore { return r.vectorStore },
		SetVectorStore: func(store *storage.VectorStore) {
			r.vectorStore = store
			r.kernel.SetVectorStore(store)
		},
		ParagraphVectorStore: func() *storage.VectorStore { return r.paragraphVectorStore },
		SetParagraphVectorStore: func(store *storage.VectorStore) {
			r.paragraphVectorStore = store
			r.kernel.SetParagraphVectorStore(store)
		},
		GraphVectorStore: func() *storage.VectorStore { return r.graphVectorStore },
		SetGraphVectorStore: func(store *storage.VectorStore) {
			r.graphVectorStore = store
			r.kernel.SetGraphVectorStore(store)
		},
		LegacyView:         func() *storage.ReadOnlyVectorStoreView { return r.legacyVectorView },
		SetLegacyView:      func(view *storage.ReadOnlyVectorStoreView) { r.legacyVectorView = view },
		VectorsRoot:        r.kernel.VectorsRoot,
		ParagraphVectorDir: r.kernel.ParagraphVectorDir,
		GraphVectorDir:     r.kernel.GraphVectorDir,
		MakeVectorStore: func(ctx context.Context, dataDir string) (*storage.VectorStore, error) {
			return r.kernel.MakeVectorStore(dataDir, nil)
		},
		SaveVectorStore: func(ctx context.Context, store *storage.VectorStore) {
			r.kernel.SaveVectorStore(store)
		},
		WriteDualVectorReadyManifest: func(ctx context.Context, stats map[string]map[string]int, migrationStats map[string]map[string]int, generationReason string) {
			r.kernel.WriteDualVectorReadyManifest(stats, migrationStats, generationReason, nil)
		},
		RefreshDualVectorReadyManifestFromStores: func(ctx context.Context) {
			r.kernel.RefreshDualVectorReadyManifestFromStores()
		},
		RecoverEmbedding: r.recoverEmbeddingOnce,
	}
}

// dryRunRebuildAllVectors 对齐 _execute_vector_rebuild_locked 的 dry_run 分支
// 只统计目标数量并附带重建状态，不修改运行时和磁盘
func (r *Runtime) dryRunRebuildAllVectors(ctx context.Context, options map[string]any) (map[string]any, error) {
	lock := r.kernel.VectorRebuildLock()
	if lock != nil {
		if !lock.TryLock() {
			return map[string]any{
				"success": false,
				"error":   "vector_rebuild_running",
				"detail":  "已有向量重建任务正在运行",
			}, nil
		}
		defer lock.Unlock()
	}
	counts := r.kernel.CountVectorRebuildTargets()
	for _, key := range []string{"paragraphs", "entities", "relations"} {
		if _, ok := counts[key]; !ok {
			counts[key] = 0
		}
	}
	relationEnabled := r.kernel.RelationVectorsEnabled()
	if value, ok := options["include_relations"].(bool); ok {
		relationEnabled = value
	}
	if !relationEnabled {
		counts["relations"] = 0
	}
	total := counts["paragraphs"] + counts["entities"] + counts["relations"]
	status, err := r.kernel.VectorRebuildStatus()
	if err != nil {
		return nil, err
	}
	result := make(map[string]any, len(status)+4)
	for key, value := range status {
		result[key] = value
	}
	result["success"] = true
	result["dry_run"] = true
	result["counts"] = counts
	result["total"] = total
	return result, nil
}
