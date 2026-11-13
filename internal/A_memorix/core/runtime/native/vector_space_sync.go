package native

// 向量空间同步，对齐 Python vector_space_service.synchronize / _synchronize_once

import (
	"context"
	"fmt"
	"strings"

	"maibot-go/internal/A_memorix/core/storage"
)

type vectorSpaceSyncOptions struct {
	Force            bool
	BatchSize        int
	IncludeRelations *bool
}

// SynchronizeVectorSpaceNative 对齐 synchronize：模型指纹变化时切换到对应向量空间并重建
func (r *Runtime) SynchronizeVectorSpaceNative(ctx context.Context, options map[string]any) (map[string]any, error) {
	opts := vectorSpaceSyncOptions{}
	if options != nil {
		opts.Force = memoryTruthy(options["force"])
		opts.BatchSize = intFromAny(options["batch_size"], 0)
		if value, ok := options["include_relations"].(bool); ok {
			opts.IncludeRelations = &value
		}
	}
	return r.synchronizeVectorSpaceOnce(ctx, opts)
}

func memoryTruthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		normalized := strings.ToLower(strings.TrimSpace(typed))
		return normalized != "" && normalized != "0" && normalized != "false" && normalized != "none"
	case nil:
		return false
	default:
		return true
	}
}

func (r *Runtime) synchronizeVectorSpaceOnce(ctx context.Context, opts vectorSpaceSyncOptions) (map[string]any, error) {
	if r.vectorSpace == nil {
		return map[string]any{"success": false, "error": "vector_space_service_missing"}, nil
	}
	fingerprint := r.kernel.CurrentEmbeddingFingerprintForValidation(nil)
	if fingerprint == nil {
		return map[string]any{"success": false, "error": "embedding_fingerprint_unavailable"}, nil
	}
	spaceID := r.vectorSpace.SpaceID(fingerprint)
	activeSpaceID := r.kernel.ActiveVectorSpaceID()
	readAvailable := r.kernel.RuntimeCapabilities()["vector_read"]
	if !opts.Force && spaceID == activeSpaceID && readAvailable {
		return map[string]any{"success": true, "changed": false}, nil
	}

	lock := r.kernel.VectorRebuildLock()
	if lock != nil {
		lock.Lock()
		defer lock.Unlock()
	}
	// 锁内复查，避免并发同步重复重建
	activeSpaceID = r.kernel.ActiveVectorSpaceID()
	if !opts.Force && spaceID == activeSpaceID && r.kernel.RuntimeCapabilities()["vector_read"] {
		return map[string]any{"success": true, "changed": false}, nil
	}
	r.kernel.SetTargetVectorSpaceID(spaceID)
	checkedAt := nowSeconds()
	r.kernel.SetEmbeddingDegraded(true, "正在同步当前模型向量库", &checkedAt)
	_ = r.kernel.SetRuntimeCapability("vector_read", false)
	_ = r.kernel.SetRuntimeCapability("vector_write", false)

	originalInputs := r.kernel.VectorSpaceInputs()
	oldFingerprint := r.kernel.StoredEmbeddingFingerprint(r.vectorStore)
	if oldFingerprint != nil {
		for _, store := range []*storage.VectorStore{r.vectorStore, r.paragraphVectorStore, r.graphVectorStore} {
			if store == nil {
				continue
			}
			if err := store.Save("", oldFingerprint); err != nil {
				r.logger.Warning("[native] 同步前保存旧模型向量失败: " + err.Error())
			}
		}
		_ = r.kernel.SaveVectorSpaceInputs()
	}
	if _, err := r.vectorSpace.MigrateLegacySpace(ctx); err != nil {
		r.logger.Warning("[native] 迁移旧向量空间失败: " + err.Error())
	}
	r.kernel.SetActiveVectorSpaceID(spaceID)
	_ = r.kernel.LoadVectorSpaceInputs()
	originalInputs = r.kernel.VectorSpaceInputs()
	r.kernel.SetDualVectorPoolsReady(false)

	dimension := r.kernel.CurrentEmbeddingStatusDimension()
	if dimension < 1 {
		dimension = r.kernel.EmbeddingDimension()
	}
	if dimension < 1 {
		dimension = 1
	}
	mainStore, err := r.kernel.MakeVectorStore(r.kernel.VectorsRoot(), &dimension)
	if err != nil {
		r.restoreVectorSpaceSync(originalInputs, err)
		return nil, err
	}
	r.setVectorStore(mainStore)
	r.setParagraphVectorStore(nil)
	r.setGraphVectorStore(nil)
	if _, err := r.metadataStore.ResetVectorProjectionState(); err != nil {
		r.logger.Warning("[native] 重置向量投影状态失败: " + err.Error())
	}
	if err := r.loadVectorStoresForRuntime(); err != nil {
		r.restoreVectorSpaceSync(originalInputs, err)
		return nil, err
	}

	rebuildOptions := map[string]any{
		"batch_size":  opts.BatchSize,
		"reuse_space": !opts.Force,
	}
	if opts.IncludeRelations != nil {
		rebuildOptions["include_relations"] = *opts.IncludeRelations
	}
	result, err := r.RebuildAllVectorsLockedNative(ctx, rebuildOptions)
	if err != nil {
		r.restoreVectorSpaceSync(originalInputs, err)
		return nil, err
	}
	if !memoryTruthy(result["success"]) {
		reason := strings.Join(toStrings(result["errors"]), "; ")
		if reason == "" {
			reason = fmt.Sprint(result["error"])
		}
		buildErr := fmt.Errorf("%s", firstNonEmptyRebuildText(reason, "向量库同步失败"))
		r.restoreVectorSpaceSync(originalInputs, buildErr)
		return result, buildErr
	}
	if err := r.vectorSpace.RegisterCurrentSpace(ctx, fingerprint); err != nil {
		r.restoreVectorSpaceSync(originalInputs, err)
		return nil, err
	}
	// 向量空间切换后重建检索器，避免继续持有切换前的空存储引用
	if bundle, err := r.buildSearchRuntime(r.kernel.Config()); err == nil {
		_ = r.applySearchRuntimeBundle(bundle)
	}
	_ = r.kernel.SaveVectorSpaceInputs()
	_ = r.kernel.SetRuntimeCapability("vector_read", true)
	_ = r.kernel.SetRuntimeCapability("vector_write", true)
	r.kernel.SetEmbeddingDegraded(false, "", nil)
	r.kernel.SetVectorHealth(map[string]any{
		"state": "healthy", "error_code": "", "reason": "", "trusted_coverage": 1.0,
		"recovery_stage": "idle", "operation_id": "",
	})
	r.kernel.SetTargetVectorSpaceID("")
	r.logger.Info(fmt.Sprintf("[native] 模型向量库已启用: model=%v space=%s", fingerprint["model"], spaceID))
	result["changed"] = true
	result["space_id"] = spaceID
	return result, nil
}

func firstNonEmptyRebuildText(values ...string) string {
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" && text != "<nil>" {
			return text
		}
	}
	return ""
}

func (r *Runtime) restoreVectorSpaceSync(originalInputs map[string]string, cause error) {
	r.kernel.SetVectorSpaceInputs(originalInputs)
	reason := "向量库同步已取消"
	if cause != nil {
		reason = cause.Error()
	}
	_ = r.kernel.SetRuntimeCapability("vector_read", false)
	_ = r.kernel.SetRuntimeCapability("vector_write", false)
	r.kernel.SetVectorHealth(map[string]any{
		"state": "unavailable", "error_code": "vector_space_sync_failed", "reason": reason,
	})
	checkedAt := nowSeconds()
	r.kernel.SetEmbeddingDegraded(true, reason, &checkedAt)
	r.kernel.SetTargetVectorSpaceID("")
}
