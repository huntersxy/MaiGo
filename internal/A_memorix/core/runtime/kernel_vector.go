package memruntime

// 向量池状态、向量运行时与向量恢复的内核转发，对齐 sdk_memory_kernel.py 的向量相关方法

import (
	"context"

	"maibot-go/internal/A_memorix/core/storage"
)

// storageVectorStore 是本包内对 storage.VectorStore 的短别名
type storageVectorStore = storage.VectorStore

// VectorPoolMode 对齐 _vector_pool_mode
func (k *Kernel) VectorPoolMode() string {
	if k.services.DualVectorState == nil {
		return "dual"
	}
	return k.services.DualVectorState.VectorPoolMode(k.context())
}

// DualVectorPoolsConfigEnabled 对齐 _dual_vector_pools_config_enabled 的导出版本
func (k *Kernel) DualVectorPoolsConfigEnabled() bool {
	return k.dualVectorPoolsConfigEnabled()
}

// VectorsRoot 对齐 _vectors_root
func (k *Kernel) VectorsRoot() string {
	if k.services.DualVectorState == nil {
		return ""
	}
	return k.services.DualVectorState.VectorsRoot(k.context())
}

// ParagraphVectorDir 对齐 _paragraph_vector_dir
func (k *Kernel) ParagraphVectorDir() string {
	if k.services.DualVectorState == nil {
		return ""
	}
	return k.services.DualVectorState.ParagraphVectorDir(k.context())
}

// GraphVectorDir 对齐 _graph_vector_dir
func (k *Kernel) GraphVectorDir() string {
	if k.services.DualVectorState == nil {
		return ""
	}
	return k.services.DualVectorState.GraphVectorDir(k.context())
}

// DualVectorReadyManifestPath 对齐 _dual_vector_ready_manifest_path
func (k *Kernel) DualVectorReadyManifestPath() string {
	if k.services.DualVectorState == nil {
		return ""
	}
	return k.services.DualVectorState.DualVectorReadyManifestPath(k.context())
}

// ReadDualVectorReadyManifest 对齐 _read_dual_vector_ready_manifest
func (k *Kernel) ReadDualVectorReadyManifest() map[string]any {
	if k.services.DualVectorState == nil {
		return nil
	}
	return k.services.DualVectorState.ReadDualVectorReadyManifest(k.context())
}

// DualVectorReady 对齐 _dual_vector_ready
func (k *Kernel) DualVectorReady(expectedDimension *int) bool {
	if k.services.DualVectorState == nil {
		return false
	}
	return k.services.DualVectorState.DualVectorReady(k.context(), expectedDimension)
}

// WriteDualVectorReadyManifest 对齐 _write_dual_vector_ready_manifest
func (k *Kernel) WriteDualVectorReadyManifest(stats map[string]map[string]int, migrationStats map[string]map[string]int, generationReason string, buildFingerprint map[string]any) {
	if k.services.DualVectorState == nil {
		return
	}
	k.services.DualVectorState.WriteDualVectorReadyManifest(k.context(), stats, migrationStats, generationReason, buildFingerprint)
}

// RemoveDualVectorReadyManifest 对齐 _remove_dual_vector_ready_manifest
func (k *Kernel) RemoveDualVectorReadyManifest() {
	if k.services.DualVectorState == nil {
		return
	}
	k.services.DualVectorState.RemoveDualVectorReadyManifest(k.context())
}

// RefreshDualVectorReadyManifestFromStores 对齐 _refresh_dual_vector_ready_manifest_from_stores
func (k *Kernel) RefreshDualVectorReadyManifestFromStores() {
	if k.services.DualVectorState == nil {
		return
	}
	k.services.DualVectorState.RefreshDualVectorReadyManifestFromStores(k.context())
}

// ClearLegacySingleVectorFilesAfterDualReady 对齐 _clear_legacy_single_vector_files_after_dual_ready
func (k *Kernel) ClearLegacySingleVectorFilesAfterDualReady() {
	if k.services.DualVectorState == nil {
		return
	}
	k.services.DualVectorState.ClearLegacySingleVectorFilesAfterDualReady(k.context())
}

// PrepareDualVectorBuildDirs 对齐 _prepare_dual_vector_build_dirs
func (k *Kernel) PrepareDualVectorBuildDirs() (string, string, string) {
	if k.services.DualVectorState == nil {
		return "", "", ""
	}
	return k.services.DualVectorState.PrepareDualVectorBuildDirs(k.context())
}

// ActivateDualVectorBuildDirs 对齐 _activate_dual_vector_build_dirs
func (k *Kernel) ActivateDualVectorBuildDirs(buildRoot string) {
	if k.services.DualVectorState == nil {
		return
	}
	k.services.DualVectorState.ActivateDualVectorBuildDirs(k.context(), buildRoot)
}

// CleanupStaleDualVectorBuildDirs 对齐 _cleanup_stale_dual_vector_build_dirs
func (k *Kernel) CleanupStaleDualVectorBuildDirs() {
	if k.services.DualVectorState == nil {
		return
	}
	k.services.DualVectorState.CleanupStaleDualVectorBuildDirs(k.context())
}

// MakeVectorStore 对齐 _make_vector_store
func (k *Kernel) MakeVectorStore(dataDir string, dimension *int) (*storageVectorStore, error) {
	if k.services.DualVectorState == nil {
		return nil, nil
	}
	return k.services.DualVectorState.MakeVectorStore(k.context(), dataDir, dimension)
}

// SaveVectorStore 对齐 _save_vector_store
func (k *Kernel) SaveVectorStore(store *storageVectorStore) {
	if k.services.DualVectorState == nil {
		return
	}
	k.services.DualVectorState.SaveVectorStore(k.context(), store)
}

// ReloadDualVectorStoresFromDisk 对齐 _reload_dual_vector_stores_from_disk
func (k *Kernel) ReloadDualVectorStoresFromDisk() bool {
	if k.services.DualVectorState == nil {
		return false
	}
	return k.services.DualVectorState.ReloadDualVectorStoresFromDisk(k.context())
}

// TryRecoverDualReadyManifest 对齐 _try_recover_dual_ready_manifest
func (k *Kernel) TryRecoverDualReadyManifest() bool {
	if k.services.DualVectorState == nil {
		return false
	}
	return k.services.DualVectorState.TryRecoverDualReadyManifest(k.context())
}

// DropDualBuildRoot 对齐 _drop_dual_build_root
func (k *Kernel) DropDualBuildRoot(buildRoot string) {
	if k.services.DualVectorState == nil {
		return
	}
	k.services.DualVectorState.DropDualBuildRoot(k.context(), buildRoot)
}

// ParagraphVectorStore 对齐 paragraph_vector_store 属性
func (k *Kernel) ParagraphVectorStore() *storageVectorStore { return k.paragraphVectorStore }

// SetParagraphVectorStore 写入段落池向量存储
func (k *Kernel) SetParagraphVectorStore(store *storageVectorStore) { k.paragraphVectorStore = store }

// GraphVectorStore 对齐 graph_vector_store 属性
func (k *Kernel) GraphVectorStore() *storageVectorStore { return k.graphVectorStore }

// SetGraphVectorStore 写入图谱池向量存储
func (k *Kernel) SetGraphVectorStore(store *storageVectorStore) { k.graphVectorStore = store }

// GraphVectorID 对齐 _graph_vector_id
func (k *Kernel) GraphVectorID(itemType string, hashValue string) string {
	if k.services.DualVectorState == nil {
		return ""
	}
	return k.services.DualVectorState.GraphVectorID(itemType, hashValue)
}

// VectorRebuildStatus 对齐 get_vector_rebuild_status
func (k *Kernel) VectorRebuildStatus() (map[string]any, error) {
	if k.services.VectorRuntime == nil {
		return map[string]any{}, nil
	}
	return k.services.VectorRuntime.VectorRebuildStatus(k.context())
}

// VectorStoreSnapshot 对齐 get_vector_store_snapshot
func (k *Kernel) VectorStoreSnapshot(store *storageVectorStore) map[string]any {
	if k.services.VectorRuntime == nil {
		return map[string]any{}
	}
	return k.services.VectorRuntime.VectorStoreSnapshot(k.context(), store)
}

// VectorPoolsStatus 对齐 get_vector_pools_status
func (k *Kernel) VectorPoolsStatus() map[string]any {
	if k.services.VectorRuntime == nil {
		return map[string]any{}
	}
	return k.services.VectorRuntime.VectorPoolsStatus(k.context())
}

// CountVectorRebuildTargets 对齐 _count_vector_rebuild_targets
func (k *Kernel) CountVectorRebuildTargets() map[string]int {
	if k.services.VectorRuntime == nil {
		return map[string]int{}
	}
	return k.services.VectorRuntime.CountVectorRebuildTargets(k.context())
}

// TableHasColumn 对齐 _table_has_column
func (k *Kernel) TableHasColumn(table string, column string) bool {
	if k.services.VectorRuntime == nil {
		return false
	}
	return k.services.VectorRuntime.TableHasColumn(k.context(), table, column)
}

// ActiveRowFilterSQL 对齐 _active_row_filter_sql
func (k *Kernel) ActiveRowFilterSQL(table string) string {
	if k.services.VectorRuntime == nil {
		return ""
	}
	return k.services.VectorRuntime.ActiveRowFilterSQL(k.context(), table)
}

// BackfillMissingDualVectorPoolEntries 对齐 backfill_missing_dual_vector_pool_entries
func (k *Kernel) BackfillMissingDualVectorPoolEntries(batchSize int) (map[string]any, error) {
	if k.services.VectorRuntime == nil {
		return map[string]any{}, nil
	}
	return k.services.VectorRuntime.BackfillMissingDualVectorPoolEntries(k.context(), batchSize)
}

// RefreshRuntimeDependents 对齐 _refresh_runtime_dependents
func (k *Kernel) RefreshRuntimeDependents(preserveManagers bool) {
	if k.services.VectorRuntime == nil {
		return
	}
	k.services.VectorRuntime.RefreshRuntimeDependents(k.context(), preserveManagers)
}

// EncodeAndAddRebuildVectors 对齐 _encode_and_add_rebuild_vectors
func (k *Kernel) EncodeAndAddRebuildVectors(params map[string]any) (map[string]any, error) {
	if k.services.VectorRuntime == nil {
		return map[string]any{}, nil
	}
	return k.services.VectorRuntime.EncodeAndAddRebuildVectors(k.context(), params)
}

// CopyRebuildVectorsFromStore 对齐 _copy_rebuild_vectors_from_store
func (k *Kernel) CopyRebuildVectorsFromStore(params map[string]any) (map[string]int, error) {
	if k.services.VectorRuntime == nil {
		return map[string]int{}, nil
	}
	return k.services.VectorRuntime.CopyRebuildVectorsFromStore(k.context(), params)
}

// CopyOrEncodeDualRebuildVectors 对齐 _copy_or_encode_dual_rebuild_vectors
func (k *Kernel) CopyOrEncodeDualRebuildVectors(params map[string]any) (map[string]any, error) {
	if k.services.VectorRuntime == nil {
		return map[string]any{}, nil
	}
	return k.services.VectorRuntime.CopyOrEncodeDualRebuildVectors(k.context(), params)
}

// RebuildAllVectors 对齐 rebuild_all_vectors
func (k *Kernel) RebuildAllVectors() (map[string]any, error) {
	if k.services.VectorRuntime == nil {
		return map[string]any{}, nil
	}
	return k.services.VectorRuntime.RebuildAllVectors(k.context())
}

// RebuildAllVectorsLocked 对齐 _rebuild_all_vectors_locked
func (k *Kernel) RebuildAllVectorsLocked() (map[string]any, error) {
	if k.services.VectorRuntime == nil {
		return map[string]any{}, nil
	}
	return k.services.VectorRuntime.RebuildAllVectorsLocked(k.context())
}

// DetectCurrentEmbeddingDimensionForRebuild 对齐 _detect_current_embedding_dimension_for_rebuild
func (k *Kernel) DetectCurrentEmbeddingDimensionForRebuild() int {
	if k.services.VectorRuntime == nil {
		return k.EmbeddingDimension()
	}
	return k.services.VectorRuntime.DetectCurrentEmbeddingDimensionForRebuild(k.context())
}

// MemoryRuntimeAdmin 对齐 memory_runtime_admin
func (k *Kernel) MemoryRuntimeAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if k.services.VectorRuntime == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = k.context()
	}
	return k.services.VectorRuntime.MemoryRuntimeAdmin(ctx, action, payload)
}

// SetVectorHealth 对齐 _set_vector_health
func (k *Kernel) SetVectorHealth(patch map[string]any) {
	if k.services.VectorRecovery == nil {
		k.applyVectorHealthPatch(patch)
		return
	}
	k.services.VectorRecovery.SetVectorHealth(k.context(), patch)
}

// DisableVectorChannel 对齐 _disable_vector_channel
func (k *Kernel) DisableVectorChannel(err error) {
	if k.services.VectorRecovery == nil {
		return
	}
	k.services.VectorRecovery.DisableVectorChannel(k.context(), err)
}

// RecoverKnownVectorFailure 对齐 _recover_known_vector_failure
func (k *Kernel) RecoverKnownVectorFailure(err error) bool {
	if k.services.VectorRecovery == nil {
		return false
	}
	return k.services.VectorRecovery.RecoverKnownVectorFailure(k.context(), err)
}

// ResumeVectorRecoveryIfNeeded 对齐 _resume_vector_recovery_if_needed
func (k *Kernel) ResumeVectorRecoveryIfNeeded() error {
	if k.services.VectorRecovery == nil {
		return nil
	}
	return k.services.VectorRecovery.ResumeVectorRecoveryIfNeeded(k.context())
}

// CopyLegacyVectorsOnce 对齐 copy_legacy_vectors_once
func (k *Kernel) CopyLegacyVectorsOnce(batchSize int) (map[string]any, error) {
	if k.services.VectorRecovery == nil {
		return map[string]any{}, nil
	}
	return k.services.VectorRecovery.CopyLegacyVectorsOnce(k.context(), batchSize)
}

// CleanupVectorQuarantine 对齐 cleanup_vector_quarantine
func (k *Kernel) CleanupVectorQuarantine() (map[string]any, error) {
	if k.services.VectorRecovery == nil {
		return map[string]any{}, nil
	}
	return k.services.VectorRecovery.CleanupVectorQuarantine(k.context())
}

// RecoverEmbeddingOnce 对齐 recover_embedding_once
func (k *Kernel) RecoverEmbeddingOnce(sampleText string) (map[string]any, error) {
	if k.services.VectorRecovery == nil {
		return map[string]any{}, nil
	}
	return k.services.VectorRecovery.RecoverEmbeddingOnce(k.context(), sampleText)
}

// DeleteVectorsByType 对齐 delete_vectors_by_type
func (k *Kernel) DeleteVectorsByType(storeType string) (map[string]any, error) {
	if k.services.VectorDelete == nil {
		return map[string]any{}, nil
	}
	return k.services.VectorDelete.DeleteVectorsByType(k.context(), storeType)
}

// ShouldStartDualVectorAutoMigration 对齐 should_start_dual_vector_auto_migration
func (k *Kernel) ShouldStartDualVectorAutoMigration() bool {
	if k.services.DualVectorMigration == nil {
		return false
	}
	return k.services.DualVectorMigration.ShouldStartAutoMigration(k.context())
}

// NormalizeDualVectorAutoMigrationProgress 对齐 _normalize_dual_vector_auto_migration_progress
func (k *Kernel) NormalizeDualVectorAutoMigrationProgress(progress any) map[string]any {
	if k.services.DualVectorMigration == nil {
		return map[string]any{}
	}
	return k.services.DualVectorMigration.NormalizeAutoMigrationProgress(k.context(), progress)
}

// UpdateDualVectorAutoMigrationStage 对齐 _update_dual_vector_auto_migration_stage
func (k *Kernel) UpdateDualVectorAutoMigrationStage(stage string, progress map[string]any) {
	if k.services.DualVectorMigration == nil {
		return
	}
	k.services.DualVectorMigration.UpdateAutoMigrationStage(k.context(), stage, progress)
}
