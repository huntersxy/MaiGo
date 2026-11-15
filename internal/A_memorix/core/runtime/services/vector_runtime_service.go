package services

// 向量运行时服务，对齐 vector_runtime_service.py 的 MemoryVectorRuntimeService
// 说明：重建/回填/编码的批处理内核保留为依赖端口，Kernel 装配时注入，避免在 services 内反向依赖 runtime

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"MaiGo/internal/A_memorix/core/storage"
)

// VectorRuntimeDeps 向量运行时依赖端口
type VectorRuntimeDeps struct {
	Metadata func() *storage.Store
	Cfg      func(key string, fallback any) any
	// FullConfig 返回完整运行时配置，供 get_config 回显
	FullConfig func() map[string]any
	Persist    func()

	StoredVectorDimension                    func() *int
	CurrentEmbeddingStatusDimension          func() int
	VectorPersistBlockedUntilRebuild         func() bool
	VectorRebuildSourceDimension             func() *int
	CurrentEmbeddingFingerprintForValidation func() map[string]any
	StoredEmbeddingFingerprint               func() map[string]any
	EmbeddingFingerprintStatus               func(current map[string]any, stored map[string]any, hasStoredVectors bool) string
	VectorMismatchError                      func(storedDimension int, detectedDimension int) string

	ParagraphBackfillCounts       func() map[string]int
	RunParagraphBackfillOnce      func(ctx context.Context, limit *int, maxRetry *int, trigger string) (map[string]any, error)
	RecoverEmbeddingOnce          func(ctx context.Context, sampleText string) (map[string]any, error)
	RefreshRuntimeSelfCheck       func(ctx context.Context, sampleText string) (map[string]any, error)
	ApplySelfCheckDimensionResult func(report map[string]any) string
	SetEmbeddingDegraded          func(active bool, reason string, checkedAt *float64)
	IsEmbeddingDegraded           func() bool
	EmbeddingDegradedSnapshot     func() map[string]any
	EmbeddingFallbackEnabled      func() bool
	RuntimeCapabilityStatus       func() map[string]any
	SetCfg                        func(key string, value any)

	VectorStore                   func() *storage.VectorStore
	ParagraphVectorStore          func() *storage.VectorStore
	GraphVectorStore              func() *storage.VectorStore
	VectorPoolMode                func(ctx context.Context) string
	DualVectorPoolsEnabled        func(ctx context.Context) bool
	DualVectorReadyManifestPath   func(ctx context.Context) string
	DualVectorAutoMigrationStatus func() map[string]any
	WriteDualVectorReadyManifest  func(ctx context.Context)

	RefreshRuntimeDependents             func(ctx context.Context, preserveManagers bool)
	BackfillMissingDualVectorPoolEntries func(ctx context.Context, batchSize int) (map[string]any, error)
	EncodeAndAddRebuildVectors           func(ctx context.Context, params map[string]any) (map[string]any, error)
	CopyOrEncodeDualRebuildVectors       func(ctx context.Context, params map[string]any) (map[string]any, error)
	RebuildAllVectors                    func(ctx context.Context, options map[string]any) (map[string]any, error)
	RebuildAllVectorsLocked              func(ctx context.Context, options map[string]any) (map[string]any, error)
	DetectEmbeddingDimension             func(ctx context.Context) (int, error)

	RelationVectorsEnabled func() bool
	ListVectorSpaces       func(ctx context.Context) (map[string]any, error)
	DeleteVectorSpace      func(ctx context.Context, spaceID string) (map[string]any, error)
}

// VectorRuntimeService 向量运行时服务
type VectorRuntimeService struct {
	*Base
	deps VectorRuntimeDeps
}

// NewVectorRuntimeService 构造向量运行时服务
func NewVectorRuntimeService(base *Base, deps VectorRuntimeDeps) *VectorRuntimeService {
	return &VectorRuntimeService{Base: base, deps: deps}
}

func (s *VectorRuntimeService) store() *storage.Store {
	if s.deps.Metadata == nil {
		return nil
	}
	return s.deps.Metadata()
}

func (s *VectorRuntimeService) cfg(key string, fallback any) any {
	if s.deps.Cfg != nil {
		return s.deps.Cfg(key, fallback)
	}
	return s.Cfg(key, fallback)
}

// VectorRebuildStatus 对齐 _vector_rebuild_status
func (s *VectorRuntimeService) VectorRebuildStatus(ctx context.Context) (map[string]any, error) {
	var storedDimension *int
	if s.deps.StoredVectorDimension != nil {
		storedDimension = s.deps.StoredVectorDimension()
	}
	if s.deps.VectorPersistBlockedUntilRebuild != nil && s.deps.VectorPersistBlockedUntilRebuild() && s.deps.VectorRebuildSourceDimension != nil {
		if source := s.deps.VectorRebuildSourceDimension(); source != nil {
			storedDimension = source
		}
	}
	currentDimension := 0
	if s.deps.CurrentEmbeddingStatusDimension != nil {
		currentDimension = s.deps.CurrentEmbeddingStatusDimension()
	}
	dimensionRebuildRequired := storedDimension != nil && *storedDimension != currentDimension
	currentFingerprint := map[string]any{}
	if s.deps.CurrentEmbeddingFingerprintForValidation != nil {
		currentFingerprint = s.deps.CurrentEmbeddingFingerprintForValidation()
	}
	storedFingerprint := map[string]any{}
	if s.deps.StoredEmbeddingFingerprint != nil {
		storedFingerprint = s.deps.StoredEmbeddingFingerprint()
	}
	fingerprintStatus := "unknown"
	if s.deps.EmbeddingFingerprintStatus != nil {
		fingerprintStatus = s.deps.EmbeddingFingerprintStatus(currentFingerprint, storedFingerprint, storedDimension != nil)
	}
	fingerprintRebuildRequired := fingerprintStatus == "missing" || fingerprintStatus == "mismatched"
	message := ""
	switch {
	case dimensionRebuildRequired:
		storedValue := 0
		if storedDimension != nil {
			storedValue = *storedDimension
		}
		if s.deps.VectorMismatchError != nil {
			message = s.deps.VectorMismatchError(storedValue, currentDimension)
		}
	case fingerprintStatus == "mismatched":
		message = "检测到 embedding 模型指纹与现有向量库不一致，请重建向量。"
	case fingerprintStatus == "missing":
		message = "现有向量库缺少 embedding 模型指纹，无法确认模型一致性，建议重建向量。"
	case fingerprintStatus == "unknown":
		message = "当前 embedding 模型指纹不可用，无法确认向量库模型一致性。"
	}
	storedValue := 0
	if storedDimension != nil {
		storedValue = *storedDimension
	}
	if currentFingerprint == nil {
		currentFingerprint = map[string]any{}
	}
	if storedFingerprint == nil {
		storedFingerprint = map[string]any{}
	}
	return map[string]any{
		"stored_vector_dimension":      storedValue,
		"embedding_dimension":          currentDimension,
		"vector_rebuild_required":      dimensionRebuildRequired || fingerprintRebuildRequired,
		"message":                      message,
		"embedding_fingerprint":        currentFingerprint,
		"stored_embedding_fingerprint": storedFingerprint,
		"embedding_fingerprint_status": fingerprintStatus,
	}, nil
}

// CountVectorRebuildTargets 对齐 _count_vector_rebuild_targets
func (s *VectorRuntimeService) CountVectorRebuildTargets(ctx context.Context) map[string]int {
	out := map[string]int{"paragraphs": 0, "entities": 0, "relations": 0}
	store := s.store()
	if store == nil {
		return out
	}
	paragraphWhere := s.ActiveRowFilterSQL(ctx, "paragraphs")
	entityWhere := s.ActiveRowFilterSQL(ctx, "entities")
	relationWhere := s.ActiveRowFilterSQL(ctx, "relations")
	rows, err := store.Query(fmt.Sprintf(`
		SELECT
			(SELECT COUNT(*) FROM paragraphs WHERE %s) AS paragraphs,
			(SELECT COUNT(*) FROM entities WHERE %s) AS entities,
			(SELECT COUNT(*) FROM relations WHERE %s) AS relations`, paragraphWhere, entityWhere, relationWhere))
	if err != nil || len(rows) == 0 {
		return out
	}
	row := rows[0]
	out["paragraphs"] = intOr(row["paragraphs"], 0, math.MinInt)
	out["entities"] = intOr(row["entities"], 0, math.MinInt)
	out["relations"] = intOr(row["relations"], 0, math.MinInt)
	return out
}

// TableHasColumn 对齐 _table_has_column
func (s *VectorRuntimeService) TableHasColumn(ctx context.Context, table string, column string) bool {
	store := s.store()
	token := strings.TrimSpace(table)
	col := strings.TrimSpace(column)
	if store == nil || col == "" {
		return false
	}
	switch token {
	case "paragraphs", "entities", "relations":
	default:
		return false
	}
	rows, err := store.Query(fmt.Sprintf("PRAGMA table_info(%s)", token))
	if err != nil {
		return false
	}
	for _, row := range rows {
		if asString(row["name"]) == col {
			return true
		}
	}
	return false
}

// ActiveRowFilterSQL 对齐 _active_row_filter_sql
func (s *VectorRuntimeService) ActiveRowFilterSQL(ctx context.Context, table string) string {
	if strings.TrimSpace(table) == "relations" && s.TableHasColumn(ctx, "relations", "is_inactive") {
		return "is_inactive IS NULL OR is_inactive = 0"
	}
	if s.TableHasColumn(ctx, table, "is_deleted") {
		return "is_deleted IS NULL OR is_deleted = 0"
	}
	return "1 = 1"
}

// BackfillMissingDualVectorPoolEntries 对齐 _backfill_missing_dual_vector_pool_entries
func (s *VectorRuntimeService) BackfillMissingDualVectorPoolEntries(ctx context.Context, batchSize int) (map[string]any, error) {
	if s.deps.BackfillMissingDualVectorPoolEntries == nil {
		return map[string]any{"success": false, "error": "dual_vector_backfill_unavailable"}, nil
	}
	return s.deps.BackfillMissingDualVectorPoolEntries(ctx, batchSize)
}

// RefreshRuntimeDependents 对齐 _refresh_runtime_dependents
func (s *VectorRuntimeService) RefreshRuntimeDependents(ctx context.Context, preserveManagers bool) {
	if s.deps.RefreshRuntimeDependents != nil {
		s.deps.RefreshRuntimeDependents(ctx, preserveManagers)
	}
}

// EncodeAndAddRebuildVectors 对齐 _encode_and_add_rebuild_vectors
func (s *VectorRuntimeService) EncodeAndAddRebuildVectors(ctx context.Context, params map[string]any) (map[string]any, error) {
	if s.deps.EncodeAndAddRebuildVectors == nil {
		return nil, fmt.Errorf("向量重建编码器未注入")
	}
	return s.deps.EncodeAndAddRebuildVectors(ctx, params)
}

// CopyRebuildVectorsFromStore 对齐 _copy_rebuild_vectors_from_store
func (s *VectorRuntimeService) CopyRebuildVectorsFromStore(ctx context.Context, params map[string]any) (map[string]int, error) {
	source, _ := params["source_store"].(*storage.VectorStore)
	target, _ := params["target_store"].(*storage.VectorStore)
	result := map[string]int{"copied": 0, "missing": 0}
	if source == nil || target == nil {
		return result, nil
	}
	ids := stringTokens(params["ids"])
	if len(ids) == 0 {
		return result, nil
	}
	vectors, err := source.GetVectors(ids)
	if err != nil {
		return nil, err
	}
	copiedIDs := make([]string, 0)
	copiedVectors := make([][]float32, 0)
	for _, id := range ids {
		vector, ok := vectors[id]
		if !ok {
			result["missing"]++
			continue
		}
		copiedIDs = append(copiedIDs, id)
		copiedVectors = append(copiedVectors, vector)
	}
	if len(copiedIDs) > 0 {
		if _, err := target.Add(copiedVectors, copiedIDs); err != nil {
			return nil, err
		}
		result["copied"] = len(copiedIDs)
	}
	return result, nil
}

// CopyOrEncodeDualRebuildVectors 对齐 _copy_or_encode_dual_rebuild_vectors
func (s *VectorRuntimeService) CopyOrEncodeDualRebuildVectors(ctx context.Context, params map[string]any) (map[string]any, error) {
	if s.deps.CopyOrEncodeDualRebuildVectors == nil {
		return nil, fmt.Errorf("双池向量复制或编码器未注入")
	}
	return s.deps.CopyOrEncodeDualRebuildVectors(ctx, params)
}

// RebuildAllVectors 对齐 _rebuild_all_vectors
func (s *VectorRuntimeService) RebuildAllVectors(ctx context.Context) (map[string]any, error) {
	return s.rebuildAllVectors(ctx, false)
}

// RebuildAllVectorsLocked 对齐 _rebuild_all_vectors_locked
func (s *VectorRuntimeService) RebuildAllVectorsLocked(ctx context.Context) (map[string]any, error) {
	return s.rebuildAllVectors(ctx, true)
}

func (s *VectorRuntimeService) rebuildAllVectors(ctx context.Context, locked bool) (map[string]any, error) {
	if locked {
		if s.deps.RebuildAllVectorsLocked == nil {
			return nil, fmt.Errorf("向量重建内核未注入")
		}
		return s.deps.RebuildAllVectorsLocked(ctx, map[string]any{})
	}
	if s.deps.RebuildAllVectors == nil {
		return nil, fmt.Errorf("向量重建内核未注入")
	}
	return s.deps.RebuildAllVectors(ctx, map[string]any{})
}

// DetectCurrentEmbeddingDimensionForRebuild 对齐 _detect_current_embedding_dimension_for_rebuild
func (s *VectorRuntimeService) DetectCurrentEmbeddingDimensionForRebuild(ctx context.Context) int {
	if s.deps.DetectEmbeddingDimension == nil {
		return max(1, intOr(s.cfg("embedding.dimension", 0), 1, 1))
	}
	dimension, err := s.deps.DetectEmbeddingDimension(ctx)
	if err != nil {
		s.LoggerWarn("embedding 维度检测失败: %v", err)
		return max(1, intOr(s.cfg("embedding.dimension", 0), 1, 1))
	}
	if dimension <= 0 {
		s.LoggerWarn("embedding 维度检测结果非法: %d", dimension)
		return max(1, intOr(s.cfg("embedding.dimension", 0), 1, 1))
	}
	return dimension
}

// VectorStoreSnapshot 对齐 _vector_store_snapshot
func (s *VectorRuntimeService) VectorStoreSnapshot(ctx context.Context, store *storage.VectorStore) map[string]any {
	return vectorStoreSnapshotOrEmpty(store)
}

// VectorPoolsStatus 对齐 _vector_pools_status
func (s *VectorRuntimeService) VectorPoolsStatus(ctx context.Context) map[string]any {
	configuredMode := "single"
	if s.deps.VectorPoolMode != nil {
		configuredMode = s.deps.VectorPoolMode(ctx)
	}
	ready := s.deps.DualVectorPoolsEnabled != nil && s.deps.DualVectorPoolsEnabled(ctx)
	effectiveMode := "single"
	if configuredMode == "dual" && ready {
		effectiveMode = "dual"
	}
	manifestPath := ""
	if s.deps.DualVectorReadyManifestPath != nil {
		manifestPath = s.deps.DualVectorReadyManifestPath(ctx)
	}
	autoMigration := map[string]any{}
	if s.deps.DualVectorAutoMigrationStatus != nil {
		for key, value := range s.deps.DualVectorAutoMigrationStatus() {
			autoMigration[key] = value
		}
	}
	var single, paragraph, graph *storage.VectorStore
	if s.deps.VectorStore != nil {
		single = s.deps.VectorStore()
	}
	if s.deps.ParagraphVectorStore != nil {
		paragraph = s.deps.ParagraphVectorStore()
	}
	if s.deps.GraphVectorStore != nil {
		graph = s.deps.GraphVectorStore()
	}
	return map[string]any{
		"configured_mode": configuredMode, "effective_mode": effectiveMode, "ready": ready,
		"single_pool":    s.VectorStoreSnapshot(ctx, single),
		"paragraph_pool": s.VectorStoreSnapshot(ctx, paragraph),
		"graph_pool":     s.VectorStoreSnapshot(ctx, graph),
		"ready_manifest": manifestPath, "auto_migration": autoMigration,
	}
}

func (s *VectorRuntimeService) paragraphBackfillCounts() map[string]int {
	out := map[string]int{"pending": 0, "running": 0, "failed": 0, "done": 0}
	if s.deps.ParagraphBackfillCounts == nil {
		return out
	}
	for key, value := range s.deps.ParagraphBackfillCounts() {
		out[key] = value
	}
	return out
}

// MemoryRuntimeAdmin 对齐 memory_runtime_admin
func (s *VectorRuntimeService) MemoryRuntimeAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	act := strings.ToLower(strings.TrimSpace(action))
	switch act {
	case "save":
		if s.deps.Persist != nil {
			s.deps.Persist()
		}
		return map[string]any{"success": true, "saved": true, "data_dir": s.DataDir()}, nil
	case "list_vector_spaces":
		if s.deps.ListVectorSpaces == nil {
			return map[string]any{"success": false, "spaces": []any{}}, nil
		}
		return s.deps.ListVectorSpaces(ctx)
	case "delete_vector_space":
		if s.deps.DeleteVectorSpace == nil {
			return map[string]any{"success": false, "error": "向量空间服务未注入"}, nil
		}
		return s.deps.DeleteVectorSpace(ctx, asString(payload["space_id"]))
	case "get_config":
		return s.runtimeConfigStatus(ctx)
	case "self_check", "refresh_self_check":
		sample := firstNonEmptyString(asString(payload["sample_text"]), "A_Memorix runtime self check")
		if s.deps.RefreshRuntimeSelfCheck == nil {
			return map[string]any{"success": false, "report": map[string]any{}}, nil
		}
		report, err := s.deps.RefreshRuntimeSelfCheck(ctx, sample)
		if err != nil {
			return nil, err
		}
		if s.deps.ApplySelfCheckDimensionResult != nil && s.deps.SetEmbeddingDegraded != nil {
			if mismatch := s.deps.ApplySelfCheckDimensionResult(report); mismatch != "" {
				checkedAt := selfCheckTimestamp(report)
				s.deps.SetEmbeddingDegraded(true, mismatch, &checkedAt)
			} else if pyTruthy(report["ok"]) {
				checkedAt := selfCheckTimestamp(report)
				s.deps.SetEmbeddingDegraded(false, "", &checkedAt)
			} else if s.deps.EmbeddingFallbackEnabled != nil && s.deps.EmbeddingFallbackEnabled() {
				checkedAt := selfCheckTimestamp(report)
				s.deps.SetEmbeddingDegraded(true, firstNonEmptyString(asString(report["message"]), "runtime self-check failed"), &checkedAt)
			}
		}
		return map[string]any{"success": pyTruthy(report["ok"]), "report": report}, nil
	case "set_auto_save":
		enabled := pyTruthy(payload["enabled"])
		if s.deps.SetCfg != nil {
			s.deps.SetCfg("advanced.enable_auto_save", enabled)
		}
		return map[string]any{"success": true, "auto_save": enabled}, nil
	case "recover_embedding":
		if s.deps.RecoverEmbeddingOnce == nil {
			return map[string]any{"success": false, "error": "embedding 恢复探针未注入"}, nil
		}
		result, err := s.deps.RecoverEmbeddingOnce(ctx, firstNonEmptyString(asString(payload["sample_text"]), "A_Memorix runtime self check"))
		if err != nil {
			return nil, err
		}
		degraded := s.embeddingDegradedSnapshot()
		result["embedding_degraded"] = pyTruthy(degraded["active"])
		result["embedding_state"] = degraded
		result["backfill_counts"] = s.paragraphBackfillCounts()
		return result, nil
	case "rebuild_all_vectors":
		options := map[string]any{}
		if value, ok := payload["batch_size"]; ok && value != nil {
			options["batch_size"] = value
		}
		if value, ok := payload["include_relations"]; ok {
			if _, isBool := value.(bool); isBool {
				options["include_relations"] = value
			}
		}
		options["dry_run"] = pyTruthy(payload["dry_run"])
		result, err := s.RebuildAllVectorsWithOptions(ctx, options)
		if err != nil {
			return nil, err
		}
		degraded := s.embeddingDegradedSnapshot()
		result["embedding_degraded"] = pyTruthy(degraded["active"])
		result["backfill_counts"] = s.paragraphBackfillCounts()
		return result, nil
	case "paragraph_backfill_once":
		if s.deps.RunParagraphBackfillOnce == nil {
			return map[string]any{"success": false, "error": "段落回填执行器未注入"}, nil
		}
		var limit *int
		if value, ok := payload["limit"]; ok && value != nil {
			parsed := intOr(value, 0, math.MinInt)
			limit = &parsed
		}
		var maxRetry *int
		if value, ok := payload["max_retry"]; ok && value != nil {
			parsed := intOr(value, 0, math.MinInt)
			maxRetry = &parsed
		}
		result, err := s.deps.RunParagraphBackfillOnce(ctx, limit, maxRetry, "manual")
		if err != nil {
			return nil, err
		}
		degraded := s.embeddingDegradedSnapshot()
		result["embedding_degraded"] = pyTruthy(degraded["active"])
		result["backfill_counts"] = s.paragraphBackfillCounts()
		return result, nil
	case "vector_rebuild_status":
		return s.VectorRebuildStatus(ctx)
	case "vector_pools_status":
		return s.VectorPoolsStatus(ctx), nil
	case "backfill_dual_pool_entries":
		batchSize := intOr(payload["batch_size"], 256, 1)
		return s.BackfillMissingDualVectorPoolEntries(ctx, batchSize)
	case "detect_embedding_dimension":
		return s.DetectCurrentEmbeddingDimensionForRebuild(ctx), nil
	default:
		return map[string]any{"success": false, "error": fmt.Sprintf("不支持的运行时操作: %s", act)}, nil
	}
}

// RebuildAllVectorsWithOptions 带选项的重建入口（runtime 侧参数透传）
func (s *VectorRuntimeService) RebuildAllVectorsWithOptions(ctx context.Context, options map[string]any) (map[string]any, error) {
	if s.deps.RebuildAllVectors == nil {
		return nil, fmt.Errorf("向量重建内核未注入")
	}
	return s.deps.RebuildAllVectors(ctx, options)
}

func selfCheckTimestamp(report map[string]any) float64 {
	if value, ok := report["checked_at"]; ok && value != nil {
		if parsed, ok := asFloat(value); ok && parsed != 0 {
			return parsed
		}
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (s *VectorRuntimeService) embeddingDegradedSnapshot() map[string]any {
	if s.deps.EmbeddingDegradedSnapshot == nil {
		return map[string]any{}
	}
	out := map[string]any{}
	for key, value := range s.deps.EmbeddingDegradedSnapshot() {
		out[key] = value
	}
	return out
}

func (s *VectorRuntimeService) runtimeConfigStatus(ctx context.Context) (map[string]any, error) {
	rebuildStatus, err := s.VectorRebuildStatus(ctx)
	if err != nil {
		return nil, err
	}
	poolsStatus := s.VectorPoolsStatus(ctx)
	degraded := s.embeddingDegradedSnapshot()
	backfill := s.paragraphBackfillCounts()
	capabilities := map[string]any{}
	if s.deps.RuntimeCapabilityStatus != nil {
		for key, value := range s.deps.RuntimeCapabilityStatus() {
			capabilities[key] = value
		}
	}
	config := map[string]any{}
	// 对齐 Python get_config：返回完整运行时配置
	if s.deps.FullConfig != nil {
		for key, item := range s.deps.FullConfig() {
			config[key] = item
		}
	}
	result := map[string]any{
		"success": true, "config": config, "data_dir": s.DataDir(),
		"embedding_dimension":               rebuildStatus["embedding_dimension"],
		"stored_vector_dimension":           rebuildStatus["stored_vector_dimension"],
		"vector_rebuild_required":           rebuildStatus["vector_rebuild_required"],
		"vector_rebuild_message":            rebuildStatus["message"],
		"embedding_fingerprint":             rebuildStatus["embedding_fingerprint"],
		"stored_embedding_fingerprint":      rebuildStatus["stored_embedding_fingerprint"],
		"embedding_fingerprint_status":      rebuildStatus["embedding_fingerprint_status"],
		"relation_vectors_enabled":          s.deps.RelationVectorsEnabled != nil && s.deps.RelationVectorsEnabled(),
		"auto_save":                         pyTruthy(s.cfg("advanced.enable_auto_save", true)),
		"vector_pools":                      poolsStatus,
		"vector_pools_ready":                pyTruthy(poolsStatus["ready"]),
		"vector_pools_effective_mode":       poolsStatus["effective_mode"],
		"embedding_degraded":                pyTruthy(degraded["active"]),
		"embedding_degraded_reason":         asString(degraded["reason"]),
		"embedding_degraded_since":          degraded["since"],
		"embedding_last_check":              degraded["last_check"],
		"paragraph_vector_backfill_pending": backfill["pending"],
		"paragraph_vector_backfill_running": backfill["running"],
		"paragraph_vector_backfill_failed":  backfill["failed"],
		"paragraph_vector_backfill_done":    backfill["done"],
	}
	for key, value := range capabilities {
		result[key] = value
	}
	return result, nil
}
