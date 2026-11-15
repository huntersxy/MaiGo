package services

// 向量恢复服务，逐段对齐 vector_recovery_service.py 的 MemoryVectorRecoveryService

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"maibot-go/internal/A_memorix/core/storage"
)

var vectorRecoveryStages = []string{"prepared", "quarantined", "metadata_reset", "new_generation_ready", "copying", "completed"}

var knownRecoverableVectorCodes = map[string]struct{}{
	"v1_metadata_invalid": {}, "v1_metadata_id_collision": {}, "v1_tombstone_invalid": {},
	"v1_tombstone_orphaned": {}, "v2_commit_invalid": {}, "v2_commit_mismatch": {},
	"dual_pool_missing": {}, "vector_pair_missing": {}, "vector_pair_truncated": {}, "vector_pair_count_mismatch": {},
}

var embeddingCompatibilityVectorCodes = map[string]struct{}{
	"embedding_fingerprint_unavailable": {}, "v1_dimension_mismatch": {}, "v1_fingerprint_missing": {},
	"v1_fingerprint_mismatch": {}, "v2_dimension_mismatch": {}, "v2_fingerprint_missing": {}, "v2_fingerprint_mismatch": {},
}

// VectorRecoveryDeps 向量恢复依赖端口
type VectorRecoveryDeps struct {
	Metadata                    func() *storage.Store
	EmbeddingDimension          func() int
	CurrentEmbeddingFingerprint func() map[string]any

	VectorHealth            func() map[string]any
	SetVectorHealth         func(patch map[string]any)
	SetRuntimeCapability    func(kind string, enabled bool)
	DualVectorPoolsReady    func() bool
	SetDualVectorPoolsReady func(bool)

	VectorStore             func() *storage.VectorStore
	SetVectorStore          func(*storage.VectorStore)
	ParagraphVectorStore    func() *storage.VectorStore
	SetParagraphVectorStore func(*storage.VectorStore)
	GraphVectorStore        func() *storage.VectorStore
	SetGraphVectorStore     func(*storage.VectorStore)
	LegacyView              func() *storage.ReadOnlyVectorStoreView
	SetLegacyView           func(*storage.ReadOnlyVectorStoreView)

	VectorsRoot                              func() string
	ParagraphVectorDir                       func() string
	GraphVectorDir                           func() string
	MakeVectorStore                          func(ctx context.Context, dataDir string) (*storage.VectorStore, error)
	SaveVectorStore                          func(ctx context.Context, store *storage.VectorStore)
	WriteDualVectorReadyManifest             func(ctx context.Context, stats map[string]map[string]int, migrationStats map[string]map[string]int, generationReason string)
	RefreshDualVectorReadyManifestFromStores func(ctx context.Context)

	RecoverEmbedding func(ctx context.Context, sampleText string) (map[string]any, error)
}

// VectorRecoveryService 向量恢复服务
type VectorRecoveryService struct {
	*Base
	deps VectorRecoveryDeps

	resumeMu   sync.Mutex
	resumeDone bool
}

// NewVectorRecoveryService 构造向量恢复服务
func NewVectorRecoveryService(base *Base, deps VectorRecoveryDeps) *VectorRecoveryService {
	return &VectorRecoveryService{Base: base, deps: deps}
}

func (s *VectorRecoveryService) journalPath() string {
	return filepath.Join(s.DataDir(), "vector_recovery.json")
}

func (s *VectorRecoveryService) quarantineRoot() string {
	return filepath.Join(s.DataDir(), "vector_quarantine")
}

// V1ReconciliationEvidenceRoot 对齐 _v1_reconciliation_evidence_root
func (s *VectorRecoveryService) V1ReconciliationEvidenceRoot(ctx context.Context) string {
	return filepath.Join(s.DataDir(), "vector_reconciliation_evidence")
}

func (s *VectorRecoveryService) activeRowFilter(store *storage.Store, table string) string {
	if store == nil {
		return "1 = 1"
	}
	rows, err := store.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return "1 = 1"
	}
	hasColumn := func(name string) bool {
		for _, row := range rows {
			if asString(row["name"]) == name {
				return true
			}
		}
		return false
	}
	// 对齐 VectorRuntimeService.ActiveRowFilterSQL / Python _active_row_filter_sql
	if strings.TrimSpace(table) == "relations" && hasColumn("is_inactive") {
		return "is_inactive IS NULL OR is_inactive = 0"
	}
	if hasColumn("is_deleted") {
		return "is_deleted IS NULL OR is_deleted = 0"
	}
	return "1 = 1"
}

// V1ValidHashesForPool 对齐 _v1_valid_hashes_for_pool
func (s *VectorRecoveryService) V1ValidHashesForPool(ctx context.Context, pool string) []string {
	store := s.store()
	if store == nil {
		s.LoggerWarn("V1 向量对账时 MetadataStore 尚未连接")
		return nil
	}
	hashesByType := map[string][]string{}
	for _, item := range []struct{ itemType, table string }{{"paragraph", "paragraphs"}, {"entity", "entities"}, {"relation", "relations"}} {
		filter := s.activeRowFilter(store, item.table)
		rows, err := store.Query(fmt.Sprintf("SELECT hash FROM %s WHERE %s ORDER BY hash ASC", item.table, filter))
		if err != nil {
			s.LoggerWarn("V1 向量对账查询失败: table=%s error=%v", item.table, err)
			return nil
		}
		values := make([]string, 0, len(rows))
		for _, row := range rows {
			if hashValue := strings.TrimSpace(asString(row["hash"])); hashValue != "" {
				values = append(values, hashValue)
			}
		}
		hashesByType[item.itemType] = values
	}
	poolToken := strings.ToLower(strings.TrimSpace(pool))
	switch poolToken {
	case "paragraph":
		return hashesByType["paragraph"]
	case "graph":
		out := make([]string, 0)
		for _, itemType := range []string{"entity", "relation"} {
			for _, hashValue := range hashesByType[itemType] {
				out = append(out, itemType+":"+hashValue)
			}
		}
		return out
	case "single":
		ownerCounts := map[string]int{}
		for _, values := range hashesByType {
			for _, hashValue := range values {
				ownerCounts[hashValue]++
			}
		}
		out := make([]string, 0)
		for hashValue, count := range ownerCounts {
			if count == 1 {
				out = append(out, hashValue)
			}
		}
		sort.Strings(out)
		return out
	default:
		s.LoggerWarn("未知 V1 向量池类型: %s", pool)
		return nil
	}
}

func (s *VectorRecoveryService) readJournal() (map[string]any, error) {
	path := s.journalPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	payload := map[string]any{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (s *VectorRecoveryService) writeJournal(payload map[string]any) error {
	path := s.journalPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	candidate := cloneAnyMap(payload)
	candidate["updated_at"] = float64(time.Now().UnixNano()) / 1e9
	raw, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := strings.TrimSuffix(path, ".json") + ".json.tmp"
	if err := os.WriteFile(tmpPath, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func stageAtLeast(current string, target string) bool {
	currentIndex, targetIndex := -1, -1
	for index, stage := range vectorRecoveryStages {
		if stage == current {
			currentIndex = index
		}
		if stage == target {
			targetIndex = index
		}
	}
	if currentIndex < 0 || targetIndex < 0 {
		return false
	}
	return currentIndex >= targetIndex
}

func effectiveRecoveryStage(journal map[string]any) string {
	stage := asString(journal["stage"])
	copyState := asString(asMap(journal["copy"])["state"])
	if stage == "completed" && copyState != "completed" && copyState != "skipped" {
		return "copying"
	}
	return stage
}

// SetVectorHealth 对齐 _set_vector_health
func (s *VectorRecoveryService) SetVectorHealth(ctx context.Context, patch map[string]any) {
	health := s.vectorHealth()
	for key, value := range patch {
		health[key] = value
	}
	health["updated_at"] = float64(time.Now().UnixNano()) / 1e9
	if s.deps.SetVectorHealth != nil {
		s.deps.SetVectorHealth(health)
	}
}

func (s *VectorRecoveryService) vectorHealth() map[string]any {
	if s.deps.VectorHealth == nil {
		return map[string]any{}
	}
	current := s.deps.VectorHealth()
	if current == nil {
		return map[string]any{}
	}
	out := map[string]any{}
	for key, value := range current {
		out[key] = value
	}
	return out
}

// VectorHealthSnapshot 对齐 _vector_health_snapshot
func (s *VectorRecoveryService) VectorHealthSnapshot(ctx context.Context) map[string]any {
	snapshot := s.vectorHealth()
	copyProgress := map[string]any{}
	for key, value := range asMap(snapshot["copy_progress"]) {
		copyProgress[key] = value
	}
	snapshot["copy_progress"] = copyProgress
	return snapshot
}

func vectorIntegrityCode(err error) string {
	if typed, ok := err.(*storage.VectorStoreIntegrityError); ok {
		return typed.ErrorCode
	}
	return "vector_unclassified_error"
}

// DisableVectorChannel 对齐 _disable_vector_channel
func (s *VectorRecoveryService) DisableVectorChannel(ctx context.Context, err error) {
	errorCode := vectorIntegrityCode(err)
	state := "unavailable"
	recoveryStage := "not_started"
	if errorCode == "embedding_fingerprint_unavailable" {
		state = "verification_pending"
		recoveryStage = "waiting_embedding_probe"
	} else if _, ok := embeddingCompatibilityVectorCodes[errorCode]; ok {
		state = "incompatible"
		recoveryStage = "rebuild_required"
	} else if errorCode == "vector_generation_missing" {
		state = "unavailable"
		recoveryStage = "rebuild_required"
	}
	if s.deps.SetVectorStore != nil {
		s.deps.SetVectorStore(nil)
	}
	if s.deps.SetParagraphVectorStore != nil {
		s.deps.SetParagraphVectorStore(nil)
	}
	if s.deps.SetGraphVectorStore != nil {
		s.deps.SetGraphVectorStore(nil)
	}
	if s.deps.SetLegacyView != nil {
		s.deps.SetLegacyView(nil)
	}
	if s.deps.SetDualVectorPoolsReady != nil {
		s.deps.SetDualVectorPoolsReady(false)
	}
	if s.deps.SetRuntimeCapability != nil {
		s.deps.SetRuntimeCapability("vector_read", false)
		s.deps.SetRuntimeCapability("vector_write", false)
	}
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	s.SetVectorHealth(ctx, map[string]any{
		"state": state, "error_code": errorCode, "reason": reason,
		"recovery_stage": recoveryStage, "operation_id": "", "trusted_coverage": 0.0,
	})
	if errorCode == "embedding_fingerprint_unavailable" {
		s.Logger().Info(fmt.Sprintf("向量通道暂不可用，已保持原文件并降级运行: code=%s, error=%s", errorCode, reason))
	} else {
		s.LoggerWarn("向量通道暂不可用，已保持原文件并降级运行: code=%s, error=%s", errorCode, reason)
	}
}

// CopyLegacyVectorsOnce 对齐 _copy_legacy_vectors_once
func (s *VectorRecoveryService) CopyLegacyVectorsOnce(ctx context.Context, batchSize int) (map[string]any, error) {
	journal, err := s.readJournal()
	if err != nil {
		return nil, err
	}
	legacyView := s.legacyView()
	if journal == nil || legacyView == nil {
		return map[string]any{"success": true, "processed": 0, "copied": 0, "done": true}, nil
	}
	paragraphStore := s.paragraphStore()
	graphStore := s.graphStore()
	if paragraphStore == nil || graphStore == nil {
		return map[string]any{"success": false, "processed": 0, "copied": 0, "done": false}, nil
	}
	targets := s.legacyCopyTargets()
	copyState := asMap(journal["copy"])
	copyState = cloneAnyMap(copyState)
	cursor := asString(copyState["cursor"])
	remaining := make([][3]string, 0, len(targets))
	for _, item := range targets {
		if item[0]+":"+item[1] > cursor {
			remaining = append(remaining, item)
		}
	}
	limit := max(1, batchSize)
	batch := remaining
	if len(batch) > limit {
		batch = remaining[:limit]
	}
	if len(batch) == 0 {
		copyState["state"] = "completed"
		finished := float64(time.Now().UnixNano()) / 1e9
		copyState["finished_at"] = finished
		copyState["total"] = len(targets)
		journal["copy"] = copyState
		journal["stage"] = "completed"
		journal["completed_at"] = finished
		if err := s.writeJournal(journal); err != nil {
			return nil, err
		}
		if s.deps.SetLegacyView != nil {
			s.deps.SetLegacyView(nil)
		}
		coverage := spaceFloatAny(asMap(journal["trusted_view"])["coverage"])
		s.SetVectorHealth(ctx, map[string]any{"state": "recovered", "copy_progress": copyState, "trusted_coverage": coverage})
		return map[string]any{"success": true, "processed": 0, "copied": 0, "done": true}, nil
	}
	sourceIDs := make([]string, 0, len(batch))
	for _, item := range batch {
		sourceIDs = append(sourceIDs, item[1])
	}
	sourceVectors, err := legacyView.GetVectors(sourceIDs)
	if err != nil {
		return nil, err
	}
	copied := 0
	copiedRelations := make([]string, 0)
	for _, item := range batch {
		itemType, sourceID, targetID := item[0], item[1], item[2]
		vector, ok := sourceVectors[sourceID]
		if !ok {
			continue
		}
		targetStore := paragraphStore
		if itemType != "paragraph" {
			targetStore = graphStore
		}
		effectiveID := sourceID
		if itemType != "paragraph" {
			effectiveID = targetID
		}
		if !targetStore.Contains(effectiveID) {
			if _, err := targetStore.Add([][]float32{vector}, []string{effectiveID}); err != nil {
				return nil, err
			}
		}
		copied++
		if itemType == "relation" {
			copiedRelations = append(copiedRelations, sourceID)
		}
	}
	if s.deps.SaveVectorStore != nil {
		s.deps.SaveVectorStore(ctx, paragraphStore)
		s.deps.SaveVectorStore(ctx, graphStore)
	}
	if s.deps.RefreshDualVectorReadyManifestFromStores != nil {
		s.deps.RefreshDualVectorReadyManifestFromStores(ctx)
	}
	if store := s.store(); store != nil {
		for _, relationHash := range copiedRelations {
			if _, err := store.SetRelationVectorState(relationHash, "ready", nil, false); err != nil {
				return nil, err
			}
		}
	}
	// 对齐 Python：本批长度等于剩余全量时才完成，禁止用 batch_size 截断后误判完成
	state := "running"
	if len(batch) == len(remaining) {
		state = "completed"
	}
	processed := intOr(copyState["processed"], 0, math.MinInt) + len(batch)
	copiedTotal := intOr(copyState["copied"], 0, math.MinInt) + copied
	last := batch[len(batch)-1]
	copyState["state"] = state
	copyState["cursor"] = last[0] + ":" + last[1]
	copyState["processed"] = processed
	copyState["copied"] = copiedTotal
	copyState["total"] = len(targets)
	if state == "completed" {
		copyState["finished_at"] = float64(time.Now().UnixNano()) / 1e9
	}
	journal["copy"] = copyState
	if state == "completed" {
		journal["stage"] = "completed"
		journal["completed_at"] = copyState["finished_at"]
	} else {
		journal["stage"] = "copying"
	}
	if err := s.writeJournal(journal); err != nil {
		return nil, err
	}
	completed := state == "completed"
	if completed && s.deps.SetLegacyView != nil {
		s.deps.SetLegacyView(nil)
	}
	healthState := "recovering"
	if completed {
		healthState = "recovered"
	}
	s.SetVectorHealth(ctx, map[string]any{"state": healthState, "copy_progress": copyState})
	return map[string]any{"success": true, "processed": len(batch), "copied": copied, "done": completed}, nil
}

func (s *VectorRecoveryService) legacyCopyTargets() [][3]string {
	store := s.store()
	view := s.legacyView()
	if store == nil || view == nil {
		return nil
	}
	rows := make([][3]string, 0)
	type owner struct{ itemType, sourceID string }
	ownerCounts := map[string]int{}
	for _, item := range []struct{ itemType, table string }{{"paragraph", "paragraphs"}, {"entity", "entities"}, {"relation", "relations"}} {
		filter := s.activeRowFilter(store, item.table)
		result, err := store.Query(fmt.Sprintf("SELECT hash FROM %s WHERE %s ORDER BY hash ASC", item.table, filter))
		if err != nil {
			continue
		}
		for _, row := range result {
			hashValue := strings.TrimSpace(asString(row["hash"]))
			if hashValue != "" && view.Contains(hashValue) {
				rows = append(rows, [3]string{item.itemType, hashValue, item.itemType + ":" + hashValue})
				ownerCounts[hashValue]++
			}
		}
	}
	filtered := make([][3]string, 0, len(rows))
	for _, item := range rows {
		if ownerCounts[item[1]] == 1 {
			filtered = append(filtered, item)
		}
	}
	sort.SliceStable(filtered, func(i int, j int) bool {
		if filtered[i][0] != filtered[j][0] {
			return filtered[i][0] < filtered[j][0]
		}
		return filtered[i][1] < filtered[j][1]
	})
	return filtered
}

// CleanupVectorQuarantine 对齐 _cleanup_vector_quarantine
func (s *VectorRecoveryService) CleanupVectorQuarantine(ctx context.Context) (map[string]any, error) {
	root := s.quarantineRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{"success": true, "removed": 0}, nil
		}
		return nil, err
	}
	journal, err := s.readJournal()
	if err != nil {
		return nil, err
	}
	activeOperation := ""
	activeCopyState := ""
	if journal != nil {
		activeOperation = asString(journal["operation_id"])
		activeCopyState = asString(asMap(journal["copy"])["state"])
	}
	type directory struct {
		path    string
		modTime time.Time
	}
	directories := make([]directory, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		directories = append(directories, directory{path: filepath.Join(root, entry.Name()), modTime: info.ModTime()})
	}
	sort.SliceStable(directories, func(i int, j int) bool { return directories[i].modTime.After(directories[j].modTime) })
	removed := 0
	now := time.Now()
	for index, item := range directories {
		if index == 0 {
			continue
		}
		name := filepath.Base(item.path)
		if name == activeOperation && activeCopyState != "completed" && activeCopyState != "skipped" {
			continue
		}
		if now.Sub(item.modTime) < 7*24*time.Hour {
			continue
		}
		if err := os.RemoveAll(item.path); err != nil {
			return nil, err
		}
		removed++
	}
	return map[string]any{"success": true, "removed": removed}, nil
}

// RecoverEmbeddingOnce 对齐 _recover_embedding_once 的公开入口
func (s *VectorRecoveryService) RecoverEmbeddingOnce(ctx context.Context, sampleText string) (map[string]any, error) {
	if s.deps.RecoverEmbedding == nil {
		return map[string]any{"success": false, "error": "embedding 恢复探针未注入"}, nil
	}
	return s.deps.RecoverEmbedding(ctx, sampleText)
}

// ResumeVectorRecoveryIfNeeded 对齐 _resume_vector_recovery_if_needed
// 返回真实错误且只执行一次；失败时保留日志，供下一次启动继续恢复
func (s *VectorRecoveryService) ResumeVectorRecoveryIfNeeded(ctx context.Context) error {
	s.resumeMu.Lock()
	defer s.resumeMu.Unlock()
	if s.resumeDone {
		return nil
	}
	journal, err := s.readJournal()
	if err != nil {
		return fmt.Errorf("读取向量恢复日志失败: %w", err)
	}
	if journal == nil {
		s.resumeDone = true
		return nil
	}
	stage := effectiveRecoveryStage(journal)
	if stage != "completed" {
		if err := s.resumeKnownVectorRecovery(ctx, journal, nil); err != nil {
			return err
		}
		s.resumeDone = true
		return nil
	}
	quarantinePath := asString(journal["quarantine_path"])
	copyState := asString(asMap(journal["copy"])["state"])
	if copyState != "completed" && copyState != "skipped" && quarantinePath != "" {
		if _, err := os.Stat(quarantinePath); err == nil {
			if s.deps.SetLegacyView != nil {
				s.deps.SetLegacyView(s.openTrustedView(quarantinePath))
			}
		}
	}
	report := asMap(journal["trusted_view"])
	state := "degraded"
	if s.legacyView() != nil {
		state = "recovering"
	}
	s.SetVectorHealth(ctx, map[string]any{
		"state": state, "error_code": asString(journal["error_code"]), "reason": asString(journal["reason"]),
		"trusted_coverage": spaceFloatAny(report["coverage"]), "recovery_stage": stage,
		"operation_id": asString(journal["operation_id"]), "copy_progress": asMap(journal["copy"]),
	})
	s.resumeDone = true
	return nil
}

// RecoverKnownVectorFailure 对齐 _recover_known_vector_failure
func (s *VectorRecoveryService) RecoverKnownVectorFailure(ctx context.Context, err error) bool {
	code := vectorIntegrityCode(err)
	if _, ok := knownRecoverableVectorCodes[code]; !ok {
		return false
	}
	journal, readErr := s.readJournal()
	if readErr != nil {
		s.LoggerWarn("读取向量恢复日志失败: %v", readErr)
		return false
	}
	if journal == nil || effectiveRecoveryStage(journal) == "completed" {
		operationID := fmt.Sprintf("%d-%s", time.Now().Unix(), randomHex(12))
		journal = map[string]any{
			"version": 1, "operation_id": operationID, "stage": "prepared",
			"source_path": s.vectorsRoot(), "quarantine_path": filepath.Join(s.quarantineRoot(), operationID),
			"created_at": float64(time.Now().UnixNano()) / 1e9,
		}
		journal["error_code"] = code
		journal["reason"] = err.Error()
		if typed, ok := err.(*storage.VectorStoreIntegrityError); ok {
			for key, value := range typed.ToDict() {
				journal[key] = value
			}
		}
		if writeErr := s.writeJournal(journal); writeErr != nil {
			s.LoggerWarn("写入向量恢复日志失败: %v", writeErr)
			return false
		}
	}
	if resumeErr := s.resumeKnownVectorRecovery(ctx, journal, err); resumeErr != nil {
		s.LoggerWarn("恢复已知向量损坏失败: %v", resumeErr)
		return false
	}
	s.LoggerWarn("已隔离损坏向量并切换到空 V2 双池: operation_id=%s, code=%s", asString(journal["operation_id"]), code)
	return true
}

func (s *VectorRecoveryService) resumeKnownVectorRecovery(ctx context.Context, journal map[string]any, cause error) error {
	stage := effectiveRecoveryStage(journal)
	stageIndex := -1
	for index, item := range vectorRecoveryStages {
		if item == stage {
			stageIndex = index
			break
		}
	}
	if stageIndex < 0 {
		return fmt.Errorf("向量恢复日志阶段无效: %s", stage)
	}
	operationID := asString(journal["operation_id"])
	if operationID == "" {
		return errors.New("向量恢复日志缺少 operation_id")
	}
	sourcePath := asString(journal["source_path"])
	if sourcePath == "" {
		sourcePath = s.vectorsRoot()
	}
	quarantinePath := asString(journal["quarantine_path"])
	if quarantinePath == "" {
		quarantinePath = filepath.Join(s.quarantineRoot(), operationID)
	}
	if !stageAtLeast(stage, "quarantined") {
		// 对齐 Python quarantine_path.parent.mkdir(parents=True, exist_ok=True)
		if err := os.MkdirAll(filepath.Dir(quarantinePath), 0o755); err != nil {
			return fmt.Errorf("创建向量隔离父目录失败: %w", err)
		}
		sourceExists := pathExists(sourcePath)
		quarantineExists := pathExists(quarantinePath)
		if sourceExists && !quarantineExists {
			if err := os.Rename(sourcePath, quarantinePath); err != nil {
				return fmt.Errorf("隔离损坏向量目录失败: %w", err)
			}
		} else if sourceExists && quarantineExists {
			return errors.New("向量恢复源目录和隔离目录同时存在，拒绝覆盖")
		} else if !quarantineExists {
			return errors.New("向量恢复源目录与隔离目录均不存在")
		}
		journal["stage"] = "quarantined"
		if err := s.writeJournal(journal); err != nil {
			return fmt.Errorf("写入向量恢复日志失败: %w", err)
		}
		stage = "quarantined"
	}
	if !stageAtLeast(stage, "metadata_reset") {
		store := s.store()
		if store == nil {
			return errors.New("重置向量派生状态时元数据库不可用")
		}
		reset, err := store.ResetVectorProjectionState()
		if err != nil {
			return fmt.Errorf("重置向量派生状态失败: %w", err)
		}
		result := map[string]any{}
		for key, value := range reset {
			result[key] = value
		}
		journal["metadata_reset"] = result
		journal["stage"] = "metadata_reset"
		if err := s.writeJournal(journal); err != nil {
			return fmt.Errorf("写入向量恢复日志失败: %w", err)
		}
		stage = "metadata_reset"
	}
	if !stageAtLeast(stage, "new_generation_ready") {
		ok, err := s.prepareEmptyDualGeneration(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("准备新的双池向量代失败")
		}
		journal["stage"] = "new_generation_ready"
		if err := s.writeJournal(journal); err != nil {
			return fmt.Errorf("写入向量恢复日志失败: %w", err)
		}
		stage = "new_generation_ready"
	}
	legacyView := s.openTrustedView(quarantinePath)
	if s.deps.SetLegacyView != nil {
		s.deps.SetLegacyView(legacyView)
	}
	report := map[string]any{}
	if legacyView != nil {
		report = legacyView.Report.ToDict()
	}
	if !stageAtLeast(stage, "copying") {
		journal["trusted_view"] = report
		copyPayload := map[string]any{"state": "skipped", "cursor": "", "processed": 0, "copied": 0, "total": 0}
		if legacyView != nil {
			copyPayload["state"] = "pending"
			copyPayload["total"] = intOr(report["trusted_count"], 0, math.MinInt)
		}
		journal["copy"] = copyPayload
		if legacyView != nil {
			journal["stage"] = "copying"
		} else {
			journal["stage"] = "completed"
			journal["completed_at"] = float64(time.Now().UnixNano()) / 1e9
		}
		if err := s.writeJournal(journal); err != nil {
			return fmt.Errorf("写入向量恢复日志失败: %w", err)
		}
		stage = asString(journal["stage"])
	}
	if s.deps.SetRuntimeCapability != nil {
		s.deps.SetRuntimeCapability("vector_write", true)
		s.deps.SetRuntimeCapability("vector_read", true)
	}
	state := "degraded"
	if legacyView != nil {
		state = "recovering"
	}
	errorCode := asString(journal["error_code"])
	reason := asString(journal["reason"])
	if cause != nil {
		errorCode = vectorIntegrityCode(cause)
		reason = cause.Error()
	}
	s.SetVectorHealth(ctx, map[string]any{
		"state": state, "error_code": errorCode, "reason": reason,
		"trusted_coverage": spaceFloatAny(report["coverage"]), "recovery_stage": stage,
		"operation_id": operationID, "copy_progress": asMap(journal["copy"]),
	})
	return nil
}

func (s *VectorRecoveryService) prepareEmptyDualGeneration(ctx context.Context) (bool, error) {
	root := s.vectorsRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return false, fmt.Errorf("创建双池向量目录失败: %w", err)
	}
	if s.deps.MakeVectorStore == nil || s.deps.SaveVectorStore == nil {
		return false, errors.New("向量库工厂未注入，无法准备新的双池代")
	}
	baseStore, err := s.deps.MakeVectorStore(ctx, root)
	if err != nil {
		return false, fmt.Errorf("创建向量库失败: %w", err)
	}
	paragraphStore, err := s.deps.MakeVectorStore(ctx, s.paragraphVectorDir())
	if err != nil {
		return false, fmt.Errorf("创建段落向量库失败: %w", err)
	}
	graphStore, err := s.deps.MakeVectorStore(ctx, s.graphVectorDir())
	if err != nil {
		return false, fmt.Errorf("创建图向量库失败: %w", err)
	}
	if s.deps.SetVectorStore != nil {
		s.deps.SetVectorStore(baseStore)
	}
	if s.deps.SetParagraphVectorStore != nil {
		s.deps.SetParagraphVectorStore(paragraphStore)
	}
	if s.deps.SetGraphVectorStore != nil {
		s.deps.SetGraphVectorStore(graphStore)
	}
	s.deps.SaveVectorStore(ctx, paragraphStore)
	s.deps.SaveVectorStore(ctx, graphStore)
	emptyStats := map[string]map[string]int{
		"paragraphs": {"done": 0, "failed": 0}, "entities": {"done": 0, "failed": 0}, "relations": {"done": 0, "failed": 0},
	}
	emptyMigration := map[string]map[string]int{
		"paragraphs": {"copied": 0, "encoded": 0, "missing": 0},
		"entities":   {"copied": 0, "encoded": 0, "missing": 0},
		"relations":  {"copied": 0, "encoded": 0, "missing": 0},
	}
	if s.deps.WriteDualVectorReadyManifest != nil {
		s.deps.WriteDualVectorReadyManifest(ctx, emptyStats, emptyMigration, "integrity_recovery")
	}
	if s.deps.SetDualVectorPoolsReady != nil {
		s.deps.SetDualVectorPoolsReady(true)
	}
	return true, nil
}

func (s *VectorRecoveryService) openTrustedView(quarantinePath string) *storage.ReadOnlyVectorStoreView {
	dimension := 0
	if s.deps.EmbeddingDimension != nil {
		dimension = s.deps.EmbeddingDimension()
	}
	fingerprint := map[string]any{}
	if s.deps.CurrentEmbeddingFingerprint != nil {
		fingerprint = s.deps.CurrentEmbeddingFingerprint()
	}
	view, err := storage.OpenTrustedV1(quarantinePath, dimension, fingerprint)
	if err != nil {
		s.LoggerWarn("旧向量不满足只读复用条件，将保持 sparse/graph 检索: code=%s, error=%v", vectorIntegrityCode(err), err)
		return nil
	}
	return view
}

func (s *VectorRecoveryService) store() *storage.Store {
	if s.deps.Metadata == nil {
		return nil
	}
	return s.deps.Metadata()
}

func (s *VectorRecoveryService) legacyView() *storage.ReadOnlyVectorStoreView {
	if s.deps.LegacyView == nil {
		return nil
	}
	return s.deps.LegacyView()
}

func (s *VectorRecoveryService) paragraphStore() *storage.VectorStore {
	if s.deps.ParagraphVectorStore == nil {
		return nil
	}
	return s.deps.ParagraphVectorStore()
}

func (s *VectorRecoveryService) graphStore() *storage.VectorStore {
	if s.deps.GraphVectorStore == nil {
		return nil
	}
	return s.deps.GraphVectorStore()
}

func (s *VectorRecoveryService) vectorsRoot() string {
	if s.deps.VectorsRoot != nil {
		return s.deps.VectorsRoot()
	}
	return filepath.Join(s.DataDir(), "vectors")
}

func (s *VectorRecoveryService) paragraphVectorDir() string {
	if s.deps.ParagraphVectorDir != nil {
		return s.deps.ParagraphVectorDir()
	}
	return filepath.Join(s.vectorsRoot(), "paragraph")
}

func (s *VectorRecoveryService) graphVectorDir() string {
	if s.deps.GraphVectorDir != nil {
		return s.deps.GraphVectorDir()
	}
	return filepath.Join(s.vectorsRoot(), "graph")
}

func randomHex(length int) string {
	const alphabet = "0123456789abcdef"
	now := time.Now().UnixNano()
	out := make([]byte, length)
	for index := range out {
		now = now*6364136223846793005 + 1442695040888963407
		out[index] = alphabet[byte(now>>33)%byte(len(alphabet))]
	}
	return string(out)
}
