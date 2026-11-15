package memruntime

// 嵌入状态与向量自检的内核转发，对齐 sdk_memory_kernel.py 的嵌入相关方法

import (
	"strings"
	"time"

	"MaiGo/internal/A_memorix/core/storage"
)

// DefaultSelfCheckSampleText 对齐 _refresh_runtime_self_check 的默认样例
const DefaultSelfCheckSampleText = "A_Memorix runtime self check"

// StoredVectorDimension 对齐 _stored_vector_dimension
func (k *Kernel) StoredVectorDimension(store *storage.VectorStore) *int {
	if k.services.EmbeddingState == nil {
		return nil
	}
	return k.services.EmbeddingState.StoredVectorDimension(k.context(), store)
}

// NormalizeEmbeddingFingerprint 对齐 _normalize_embedding_fingerprint
func (k *Kernel) NormalizeEmbeddingFingerprint(value any) map[string]any {
	if k.services.EmbeddingState == nil {
		return nil
	}
	return k.services.EmbeddingState.NormalizeEmbeddingFingerprint(value)
}

// CurrentEmbeddingStatusDimension 对齐 _current_embedding_status_dimension
func (k *Kernel) CurrentEmbeddingStatusDimension() int {
	if k.services.EmbeddingState == nil {
		return k.EmbeddingDimension()
	}
	return k.services.EmbeddingState.CurrentEmbeddingStatusDimension(k.context())
}

// CurrentEmbeddingFingerprint 对齐 _current_embedding_fingerprint
func (k *Kernel) CurrentEmbeddingFingerprint(dimension *int) map[string]any {
	if k.services.EmbeddingState == nil {
		return nil
	}
	return k.services.EmbeddingState.CurrentEmbeddingFingerprint(k.context(), dimension)
}

// CurrentEmbeddingFingerprintForValidation 对齐 _current_embedding_fingerprint_for_validation
func (k *Kernel) CurrentEmbeddingFingerprintForValidation(dimension *int) map[string]any {
	if k.services.EmbeddingState == nil {
		return nil
	}
	return k.services.EmbeddingState.CurrentEmbeddingFingerprintForValidation(k.context(), dimension)
}

// StoredEmbeddingFingerprint 对齐 _stored_embedding_fingerprint
func (k *Kernel) StoredEmbeddingFingerprint(store *storage.VectorStore) map[string]any {
	if k.services.EmbeddingState == nil {
		return nil
	}
	return k.services.EmbeddingState.StoredEmbeddingFingerprint(k.context(), store)
}

// StampMissingEmbeddingFingerprintIfDimensionMatches 对齐同名方法
func (k *Kernel) StampMissingEmbeddingFingerprintIfDimensionMatches(store *storage.VectorStore) bool {
	if k.services.EmbeddingState == nil {
		return false
	}
	return k.services.EmbeddingState.StampMissingEmbeddingFingerprintIfDimensionMatches(k.context(), store)
}

// EmbeddingFingerprintStatus 对齐 _embedding_fingerprint_status
func (k *Kernel) EmbeddingFingerprintStatus(current map[string]any, stored map[string]any, hasStoredVectors bool) string {
	if k.services.EmbeddingState == nil {
		return ""
	}
	return k.services.EmbeddingState.EmbeddingFingerprintStatus(current, stored, hasStoredVectors)
}

// StoredVectorsCompatibleWithCurrentEmbedding 对齐同名方法
func (k *Kernel) StoredVectorsCompatibleWithCurrentEmbedding(store *storage.VectorStore) bool {
	if k.services.EmbeddingState == nil {
		return false
	}
	return k.services.EmbeddingState.StoredVectorsCompatibleWithCurrentEmbedding(k.context(), store)
}

// VectorMismatchError 对齐 _vector_mismatch_error
func (k *Kernel) VectorMismatchError(storedDimension int, detectedDimension int) string {
	if k.services.EmbeddingState == nil {
		return ""
	}
	return k.services.EmbeddingState.VectorMismatchError(k.context(), storedDimension, detectedDimension)
}

// EmbeddingFallbackEnabled 对齐 _embedding_fallback_enabled
func (k *Kernel) EmbeddingFallbackEnabled() bool {
	if k.services.EmbeddingState == nil {
		return false
	}
	return k.services.EmbeddingState.EmbeddingFallbackEnabled(k.context())
}

// EmbeddingProbeIntervalSeconds 对齐 _embedding_probe_interval_seconds
func (k *Kernel) EmbeddingProbeIntervalSeconds() float64 {
	if k.services.EmbeddingState == nil {
		return 0
	}
	return k.services.EmbeddingState.EmbeddingProbeIntervalSeconds(k.context())
}

// ParagraphVectorBackfillEnabled 对齐 _paragraph_vector_backfill_enabled
func (k *Kernel) ParagraphVectorBackfillEnabled() bool {
	if k.services.EmbeddingState == nil {
		return false
	}
	return k.services.EmbeddingState.ParagraphVectorBackfillEnabled(k.context())
}

// ParagraphVectorBackfillIntervalSeconds 对齐 _paragraph_vector_backfill_interval_seconds
func (k *Kernel) ParagraphVectorBackfillIntervalSeconds() float64 {
	if k.services.EmbeddingState == nil {
		return 0
	}
	return k.services.EmbeddingState.ParagraphVectorBackfillIntervalSeconds(k.context())
}

// ParagraphVectorBackfillBatchSize 对齐 _paragraph_vector_backfill_batch_size
func (k *Kernel) ParagraphVectorBackfillBatchSize() int {
	if k.services.EmbeddingState == nil {
		return 0
	}
	return k.services.EmbeddingState.ParagraphVectorBackfillBatchSize(k.context())
}

// ParagraphVectorBackfillMaxRetry 对齐 _paragraph_vector_backfill_max_retry
func (k *Kernel) ParagraphVectorBackfillMaxRetry() int {
	if k.services.EmbeddingState == nil {
		return 0
	}
	return k.services.EmbeddingState.ParagraphVectorBackfillMaxRetry(k.context())
}

// EmbeddingDegradedSnapshot 对齐 _embedding_degraded_snapshot
func (k *Kernel) EmbeddingDegradedSnapshot() map[string]any {
	if k.services.EmbeddingState == nil {
		k.stateMu.RLock()
		defer k.stateMu.RUnlock()
		return copyAnyMap(k.embeddingDegraded)
	}
	return k.services.EmbeddingState.EmbeddingDegradedSnapshot(k.context())
}

// SetEmbeddingDegraded 对齐 _set_embedding_degraded
func (k *Kernel) SetEmbeddingDegraded(active bool, reason string, checkedAt *float64) {
	if k.services.EmbeddingState == nil {
		k.SetEmbeddingDegradedSnapshot(active, reason, checkedAt)
		return
	}
	k.services.EmbeddingState.SetEmbeddingDegraded(k.context(), active, reason, checkedAt)
}

// SetEmbeddingDegradedSnapshot 按 Python 语义整体替换降级状态，避免逐字段写产生半状态
// 对齐 embedding_state_service.py:210-227：now 缺省取当前时间，active 且此前 active 时保留 since
func (k *Kernel) SetEmbeddingDegradedSnapshot(active bool, reason string, checkedAt *float64) {
	now := float64(time.Now().UnixNano()) / 1e9
	if checkedAt != nil {
		now = *checkedAt
	}
	k.stateMu.Lock()
	defer k.stateMu.Unlock()
	previousActive, _ := k.embeddingDegraded["active"].(bool)
	var since any
	if active {
		if previousActive {
			since = k.embeddingDegraded["since"]
		} else {
			since = now
		}
	}
	k.embeddingDegraded = map[string]any{
		"active":     active,
		"reason":     strings.TrimSpace(reason),
		"since":      since,
		"last_check": now,
	}
}

// ApplyRuntimeSparseMode 对齐 _apply_runtime_sparse_mode
func (k *Kernel) ApplyRuntimeSparseMode() {
	if k.services.EmbeddingState == nil {
		return
	}
	k.services.EmbeddingState.ApplyRuntimeSparseMode(k.context())
}

// RefreshRuntimeSelfCheck 对齐 _refresh_runtime_self_check
func (k *Kernel) RefreshRuntimeSelfCheck(sampleText string) (map[string]any, error) {
	if k.services.EmbeddingState == nil {
		return map[string]any{}, nil
	}
	if sampleText == "" {
		sampleText = DefaultSelfCheckSampleText
	}
	return k.services.EmbeddingState.RefreshRuntimeSelfCheck(k.context(), sampleText)
}

// MarkStartupSelfCheckDeferred 对齐 _mark_startup_self_check_deferred
func (k *Kernel) MarkStartupSelfCheckDeferred() {
	if k.services.EmbeddingState == nil {
		return
	}
	k.services.EmbeddingState.MarkStartupSelfCheckDeferred(k.context())
}

// IsStartupSelfCheckDeferred 对齐 _is_startup_self_check_deferred
func (k *Kernel) IsStartupSelfCheckDeferred() bool {
	if k.services.EmbeddingState == nil {
		return false
	}
	return k.services.EmbeddingState.IsStartupSelfCheckDeferred(k.context())
}

// SelfCheckEffectiveDimension 对齐 _self_check_effective_dimension
func (k *Kernel) SelfCheckEffectiveDimension(report map[string]any) int {
	if k.services.EmbeddingState == nil {
		return k.EmbeddingDimension()
	}
	return k.services.EmbeddingState.SelfCheckEffectiveDimension(report)
}

// ApplySelfCheckDimensionResult 对齐 _apply_self_check_dimension_result
func (k *Kernel) ApplySelfCheckDimensionResult(report map[string]any) string {
	if k.services.EmbeddingState == nil {
		return ""
	}
	return k.services.EmbeddingState.ApplySelfCheckDimensionResult(k.context(), report)
}

// EnqueueParagraphVectorBackfill 对齐 _enqueue_paragraph_vector_backfill
func (k *Kernel) EnqueueParagraphVectorBackfill(paragraphHash string, errorText string) {
	if k.services.EmbeddingState == nil {
		return
	}
	k.services.EmbeddingState.EnqueueParagraphVectorBackfill(k.context(), paragraphHash, errorText)
}

// ParagraphVectorBackfillCounts 对齐 _paragraph_vector_backfill_counts
func (k *Kernel) ParagraphVectorBackfillCounts() map[string]int {
	if k.services.EmbeddingState == nil {
		return map[string]int{}
	}
	return k.services.EmbeddingState.ParagraphVectorBackfillCounts(k.context())
}

// RunParagraphBackfillOnce 对齐 _run_paragraph_backfill_once
func (k *Kernel) RunParagraphBackfillOnce(limit int) (map[string]any, error) {
	if k.services.EmbeddingState == nil {
		return map[string]any{}, nil
	}
	return k.services.EmbeddingState.RunParagraphBackfillOnce(k.context(), limit)
}
