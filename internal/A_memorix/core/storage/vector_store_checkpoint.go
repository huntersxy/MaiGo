package storage

// A_memorix 向量清理 checkpoint 与清空逻辑
// 对齐 Python 的 begin/commit/rollback_cleanup_checkpoint 与 clear
//
// 与 Python 一致：使用可重入锁，checkpoint 期间同一 goroutine 仍可继续调用
// add/delete/save 等方法；其它 goroutine 会阻塞到 commit/rollback
// begin 成功后锁一直持有到 commit/rollback，调用方必须成对使用

import (
	"fmt"
	"os"

	"go.uber.org/zap"
)

// cleanupCheckpointState 保存 checkpoint 开始时的可恢复快照
type cleanupCheckpointState struct {
	token        string
	vectorBytes  int64
	idBytes      int64
	pairExisted  bool
	metadata     map[string]any
	knownHashes  map[string]struct{}
	deletedIDs   map[int64]struct{}
	reservoir    [][]float32
	seenCount    int
	totalAdded   int
	totalDeleted int
}

// BeginCleanupCheckpoint 开始清理 checkpoint，返回用于提交或回滚的 token
// 对齐 begin_cleanup_checkpoint：成功后一直持锁到 commit/rollback
func (s *VectorStore) BeginCleanupCheckpoint() (string, error) {
	s.lock.Lock()
	defer func() {
		if !s.checkpointLockHeld {
			s.lock.Unlock()
		}
	}()
	if err := s.raiseIfCleanupCheckpointBrokenLocked(); err != nil {
		return "", err
	}
	if s.cleanupCheckpoint != nil {
		return "", fmt.Errorf("同一向量池不允许嵌套 cleanup checkpoint")
	}
	if len(s.writeBufferVecs) != 0 || len(s.writeBufferIDs) != 0 {
		return "", fmt.Errorf("cleanup checkpoint 前必须先提交向量写缓冲区")
	}
	for _, path := range []string{s.appendJournalPath(), s.compactionJournalPath()} {
		if _, exists, err := statSize(path); err != nil {
			return "", err
		} else if exists {
			return "", fmt.Errorf("cleanup checkpoint 前存在未完成的向量文件事务")
		}
	}
	if _, exists, err := statSize(s.metadataPath()); err != nil {
		return "", err
	} else if !exists {
		return "", fmt.Errorf("cleanup checkpoint 缺少已提交的向量元数据")
	}

	vectorBytes, idBytes, err := s.vectorPairSizesLocked()
	if err != nil {
		return "", err
	}
	meta, err := readJSONObject(s.metadataPath())
	if err != nil {
		return "", err
	}
	validated, err := s.validateOrMigrateVectorMetadataLocked(meta, vectorBytes, idBytes, nil, nil, "")
	if err != nil {
		return "", err
	}
	baselineKnown, err := jsonStringSlice(validated["known_hashes"])
	if err != nil {
		return "", fmt.Errorf("cleanup checkpoint 的内存成员集与已提交元数据不一致")
	}
	baselineDeleted, err := jsonInt64Slice(validated["deleted_ids"])
	if err != nil {
		return "", fmt.Errorf("cleanup checkpoint 的内存成员集与已提交元数据不一致")
	}
	if !sameStringSet(stringSetFromSlice(baselineKnown), s.knownHashes) ||
		!sameInt64Set(int64SetFromSlice(baselineDeleted), s.deletedIDs) {
		return "", fmt.Errorf("cleanup checkpoint 的内存成员集与已提交元数据不一致")
	}

	_, binExists, err := statSize(s.binPath())
	if err != nil {
		return "", err
	}
	snapshot := &cleanupCheckpointState{
		token:        newTransactionID(),
		vectorBytes:  vectorBytes,
		idBytes:      idBytes,
		pairExisted:  binExists,
		metadata:     validated,
		knownHashes:  cloneStringSet(s.knownHashes),
		deletedIDs:   cloneInt64Set(s.deletedIDs),
		reservoir:    cloneVectorMatrix(s.reservoir),
		seenCount:    s.seenCountForReservoir,
		totalAdded:   s.totalAdded,
		totalDeleted: s.totalDeleted,
	}
	s.cleanupCheckpoint = snapshot
	s.cleanupCompactionDeferred = false
	s.checkpointLockHeld = true
	return snapshot.token, nil
}

// CommitCleanupCheckpoint 提交清理 checkpoint 并释放锁，对齐 commit_cleanup_checkpoint
func (s *VectorStore) CommitCleanupCheckpoint(token string) error {
	if !s.checkpointLockHeld || !s.lock.heldByCurrentGoroutine() {
		return fmt.Errorf("当前没有活动的 cleanup checkpoint")
	}
	if err := s.requireCleanupCheckpointLocked(token); err != nil {
		return err
	}
	for _, path := range []string{s.appendJournalPath(), s.compactionJournalPath()} {
		if _, exists, err := statSize(path); err != nil {
			return err
		} else if exists {
			return fmt.Errorf("cleanup checkpoint 提交时仍有未完成的向量文件事务")
		}
	}
	vectorBytes, idBytes, err := s.vectorPairSizesLocked()
	if err != nil {
		return err
	}
	meta, err := readJSONObject(s.metadataPath())
	if err != nil {
		return err
	}
	validated, err := s.validateOrMigrateVectorMetadataLocked(meta, vectorBytes, idBytes, nil, nil, "")
	if err != nil {
		return err
	}
	committedKnown, err := jsonStringSlice(validated["known_hashes"])
	if err != nil {
		return fmt.Errorf("cleanup checkpoint 提交后的 known_hashes 不一致")
	}
	if !sameStringSet(stringSetFromSlice(committedKnown), s.knownHashes) {
		return fmt.Errorf("cleanup checkpoint 提交后的 known_hashes 不一致")
	}
	committedDeleted, err := jsonInt64Slice(validated["deleted_ids"])
	if err != nil {
		return fmt.Errorf("cleanup checkpoint 提交后的 tombstone 不一致")
	}
	if !sameInt64Set(int64SetFromSlice(committedDeleted), s.deletedIDs) {
		return fmt.Errorf("cleanup checkpoint 提交后的 tombstone 不一致")
	}
	s.cleanupCheckpoint = nil
	s.cleanupCompactionDeferred = false
	s.releaseCheckpointLock()
	return nil
}

// RollbackCleanupCheckpoint 回滚清理 checkpoint，对齐 rollback_cleanup_checkpoint
// 回滚失败会锁死当前实例，必须重建 VectorStore
func (s *VectorStore) RollbackCleanupCheckpoint(token string) error {
	if !s.checkpointLockHeld || !s.lock.heldByCurrentGoroutine() {
		return fmt.Errorf("当前没有活动的 cleanup checkpoint")
	}
	defer s.releaseCheckpointLock()
	if err := s.rollbackCleanupCheckpointLocked(token); err != nil {
		s.cleanupCheckpointError = err.Error()
		s.cleanupCheckpoint = nil
		s.cleanupCompactionDeferred = false
		return fmt.Errorf("cleanup checkpoint 回滚失败")
	}
	return nil
}

// requireCleanupCheckpointLocked 校验活动 checkpoint 与 token
func (s *VectorStore) requireCleanupCheckpointLocked(token string) error {
	if s.cleanupCheckpoint == nil {
		return fmt.Errorf("当前没有活动的 cleanup checkpoint")
	}
	if s.cleanupCheckpoint.token != token {
		return fmt.Errorf("cleanup checkpoint token 不匹配")
	}
	return nil
}

// releaseCheckpointLock 释放 checkpoint 期间持有的锁
func (s *VectorStore) releaseCheckpointLock() {
	if !s.checkpointLockHeld {
		return
	}
	s.checkpointLockHeld = false
	s.lock.Unlock()
}

// rollbackCleanupCheckpointLocked 把成对文件、元数据与运行时恢复到快照
func (s *VectorStore) rollbackCleanupCheckpointLocked(token string) error {
	if err := s.requireCleanupCheckpointLocked(token); err != nil {
		return err
	}
	checkpoint := s.cleanupCheckpoint
	compactionJournal, err := s.readCompactionJournalLocked()
	if err != nil {
		return err
	}
	if compactionJournal != nil {
		return fmt.Errorf("cleanup checkpoint 期间出现了不允许的向量压缩事务")
	}

	if err := truncateAndSync(s.binPath(), checkpoint.vectorBytes); err != nil {
		return err
	}
	if err := truncateAndSync(s.idsBinPath(), checkpoint.idBytes); err != nil {
		return err
	}
	if !checkpoint.pairExisted {
		if err := removeFileIfExists(s.binPath()); err != nil {
			return err
		}
		if err := removeFileIfExists(s.idsBinPath()); err != nil {
			return err
		}
	}
	if err := removeFileIfExists(s.appendJournalPath()); err != nil {
		return err
	}
	if err := removeFileIfExists(s.indexPath()); err != nil {
		return err
	}
	if err := writeJSONObject(s.metadataPath(), checkpoint.metadata); err != nil {
		return err
	}

	s.entries = map[int64][]float32{}
	s.entryOrder = nil
	s.knownHashes = map[string]struct{}{}
	s.deletedIDs = map[int64]struct{}{}
	s.invalidateIDMap()
	s.invalidateIDOffsets()
	s.writeBufferVecs = nil
	s.writeBufferIDs = nil
	s.reservoir = nil
	s.seenCountForReservoir = 0
	s.binCount = 0

	if err := s.loadLocked("", nil, nil, ""); err != nil {
		return err
	}
	if err := s.warmupLocked(false); err != nil {
		return fmt.Errorf("cleanup checkpoint 回滚后索引预热失败: %v", err)
	}

	s.reservoir = cloneVectorMatrix(checkpoint.reservoir)
	s.seenCountForReservoir = checkpoint.seenCount
	s.totalAdded = checkpoint.totalAdded
	s.totalDeleted = checkpoint.totalDeleted

	if !sameStringSet(checkpoint.knownHashes, s.knownHashes) {
		return fmt.Errorf("cleanup checkpoint 回滚后的 known_hashes 不一致")
	}
	if !sameInt64Set(checkpoint.deletedIDs, s.deletedIDs) {
		return fmt.Errorf("cleanup checkpoint 回滚后的 tombstone 不一致")
	}
	if len(s.writeBufferVecs) != 0 || len(s.writeBufferIDs) != 0 {
		return fmt.Errorf("cleanup checkpoint 回滚后残留向量写缓冲区")
	}
	vectorBytes, idBytes, err := s.vectorPairSizesLocked()
	if err != nil {
		return err
	}
	if vectorBytes != checkpoint.vectorBytes || idBytes != checkpoint.idBytes {
		return fmt.Errorf("cleanup checkpoint 回滚后的成对文件长度不一致")
	}

	s.cleanupCheckpoint = nil
	s.cleanupCompactionDeferred = false
	return nil
}

// Clear 清空向量存储，对齐 clear
func (s *VectorStore) Clear() error {
	s.lock.Lock()
	defer s.lock.Unlock()
	if err := s.raiseIfCleanupCheckpointBrokenLocked(); err != nil {
		return err
	}
	if s.dataDir == "" {
		return fmt.Errorf("No data_dir")
	}
	if s.cleanupCheckpoint != nil {
		return fmt.Errorf("cleanup checkpoint 活动期间禁止清空向量存储")
	}
	s.writeBufferVecs = nil
	s.writeBufferIDs = nil
	if err := s.recoverInterruptedAppendLocked(); err != nil {
		return err
	}
	if err := s.recoverInterruptedCompactionLocked(); err != nil {
		return err
	}

	vectorBytes, idBytes, storedDimension, err := s.clearPairStateLocked()
	if err != nil {
		return err
	}
	if vectorBytes != 0 || idBytes != 0 {
		if err := s.clearViaCompactionLocked(vectorBytes, idBytes, storedDimension); err != nil {
			s.writeBufferVecs = nil
			s.writeBufferIDs = nil
			_ = s.recoverInterruptedCompactionLocked()
			return err
		}
		s.resetEmptyRuntimeLocked()
		if err := s.saveLocked("", nil); err != nil {
			return err
		}
	} else {
		s.resetEmptyRuntimeLocked()
	}
	if err := removeFileIfExists(s.metadataPath()); err != nil {
		return err
	}
	if err := removeFileIfExists(s.indexPath()); err != nil {
		return err
	}
	zap.L().Info("VectorStore cleared.")
	return nil
}

// clearViaCompactionLocked 用空成对文件替换现有文件，走完整压缩事务协议
func (s *VectorStore) clearViaCompactionLocked(vectorBytes int64, idBytes int64, storedDimension int) error {
	tmpBinPath := s.binPath() + ".tmp"
	tmpIDsPath := s.idsBinPath() + ".tmp"
	if err := removeFileIfExists(tmpBinPath); err != nil {
		return err
	}
	if err := removeFileIfExists(tmpIDsPath); err != nil {
		return err
	}
	if err := createEmptyFileSynced(tmpBinPath); err != nil {
		return err
	}
	if err := createEmptyFileSynced(tmpIDsPath); err != nil {
		return err
	}
	if err := removeFileIfExists(s.binBackupPath()); err != nil {
		return err
	}
	if err := removeFileIfExists(s.idsBackupPath()); err != nil {
		return err
	}
	if err := copyAndSync(s.binPath(), s.binBackupPath()); err != nil {
		return err
	}
	if err := copyAndSync(s.idsBinPath(), s.idsBackupPath()); err != nil {
		return err
	}
	journal := map[string]any{
		"version":             int64(2),
		"transaction_id":      newTransactionID(),
		"dimension":           int64(storedDimension),
		"base_vector_bytes":   vectorBytes,
		"base_id_bytes":       idBytes,
		"target_vector_bytes": int64(0),
		"target_id_bytes":     int64(0),
	}
	if err := writeJSONObject(s.compactionJournalPath(), journal); err != nil {
		return err
	}
	if err := s.replaceCompactionTmpPairLocked(tmpBinPath, tmpIDsPath); err != nil {
		return err
	}
	if !s.vectorPairMatches(s.binPath(), s.idsBinPath(), 0, storedDimension) {
		return fmt.Errorf("向量清空后的成对文件不一致")
	}
	return nil
}

// createEmptyFileSynced 创建空文件并 fsync
func createEmptyFileSynced(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// stringSetFromSlice 把字符串切片转成集合
func stringSetFromSlice(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

// int64SetFromSlice 把 int64 切片转成集合
func int64SetFromSlice(values []int64) map[int64]struct{} {
	out := make(map[int64]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

// cloneStringSet 复制字符串集合
func cloneStringSet(source map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(source))
	for key := range source {
		out[key] = struct{}{}
	}
	return out
}

// cloneInt64Set 复制 int64 集合
func cloneInt64Set(source map[int64]struct{}) map[int64]struct{} {
	out := make(map[int64]struct{}, len(source))
	for key := range source {
		out[key] = struct{}{}
	}
	return out
}

// sameStringSet 判断两个字符串集合是否相等
func sameStringSet(left map[string]struct{}, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for key := range left {
		if _, ok := right[key]; !ok {
			return false
		}
	}
	return true
}

// sameInt64Set 判断两个 int64 集合是否相等
func sameInt64Set(left map[int64]struct{}, right map[int64]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for key := range left {
		if _, ok := right[key]; !ok {
			return false
		}
	}
	return true
}

// cloneVectorMatrix 深拷贝向量缓冲区
func cloneVectorMatrix(source [][]float32) [][]float32 {
	if source == nil {
		return nil
	}
	out := make([][]float32, 0, len(source))
	for _, vector := range source {
		out = append(out, append([]float32(nil), vector...))
	}
	return out
}
