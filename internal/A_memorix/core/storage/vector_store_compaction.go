package storage

// A_memorix 向量压缩（compaction）事务与崩溃恢复
// 对齐 Python 的 rebuild_index / _rebuild_index_locked /
// _recover_interrupted_compaction_unlocked / _restore_compaction_backup_unlocked /
// _reload_runtime_after_compaction_rollback_unlocked
//
// 与 Python 的差异：Python 压缩后重写 FAISS 索引文件，Go 版本没有索引文件，
// 压缩完成后直接从成对文件重建内存索引（内容等价）

import (
	"fmt"
	"os"

	"go.uber.org/zap"
)

// RebuildIndex 手工压缩向量文件，对齐 VectorStore.rebuild_index
func (s *VectorStore) RebuildIndex() error {
	s.lock.Lock()
	defer s.lock.Unlock()
	if err := s.raiseIfCleanupCheckpointBrokenLocked(); err != nil {
		return err
	}
	if s.cleanupCheckpoint != nil {
		return fmt.Errorf("cleanup checkpoint 活动期间禁止手工压缩向量文件")
	}
	return s.rebuildIndexLocked()
}

// rebuildIndexLocked 重写成对文件剔除 tombstone 行，对齐 _rebuild_index_locked
func (s *VectorStore) rebuildIndexLocked() error {
	zap.L().Info("Compacting vector store...")
	if err := s.recoverInterruptedCompactionLocked(); err != nil {
		return err
	}
	if err := s.flushWriteBufferLocked(); err != nil {
		return err
	}
	if err := s.saveLocked("", nil); err != nil {
		return err
	}

	baseVectorBytes, baseIDBytes, err := s.vectorPairSizesLocked()
	if err != nil {
		return err
	}
	tmpBinPath := s.binPath() + ".tmp"
	tmpIDsPath := s.idsBinPath() + ".tmp"
	newCount, err := s.writeCompactionTmpPairLocked(tmpBinPath, tmpIDsPath)
	if err != nil {
		return err
	}
	if !s.vectorPairMatches(tmpBinPath, tmpIDsPath, newCount, s.dimension) {
		return fmt.Errorf("向量压缩临时文件不一致，已拒绝切换")
	}

	// 先把当前成对文件复制为只读备份，后续崩溃恢复以此为准
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

	tmpVectorBytes, err := fileSizeOrZero(tmpBinPath)
	if err != nil {
		return err
	}
	tmpIDBytes, err := fileSizeOrZero(tmpIDsPath)
	if err != nil {
		return err
	}
	journal := map[string]any{
		"version":             int64(2),
		"transaction_id":      newTransactionID(),
		"dimension":           int64(s.dimension),
		"base_vector_bytes":   baseVectorBytes,
		"base_id_bytes":       baseIDBytes,
		"target_vector_bytes": tmpVectorBytes,
		"target_id_bytes":     tmpIDBytes,
	}
	if err := writeJSONObject(s.compactionJournalPath(), journal); err != nil {
		return err
	}
	if err := s.replaceCompactionTmpPairLocked(tmpBinPath, tmpIDsPath); err != nil {
		return err
	}
	if !s.vectorPairMatches(s.binPath(), s.idsBinPath(), newCount, s.dimension) {
		if recoverErr := s.recoverInterruptedCompactionLocked(); recoverErr != nil {
			return recoverErr
		}
		return fmt.Errorf("向量压缩文件切换失败，已恢复原始文件")
	}

	s.binCount = int(newCount)
	s.invalidateIDOffsets()
	s.entries = map[int64][]float32{}
	s.entryOrder = nil
	s.trained = false

	// 压缩已剔除 tombstone 行，对应哈希一并从成员集移除
	removed := false
	for hashValue := range s.knownHashes {
		if _, deleted := s.deletedIDs[s.generateID(hashValue)]; deleted {
			delete(s.knownHashes, hashValue)
			removed = true
		}
	}
	s.deletedIDs = map[int64]struct{}{}
	if removed {
		s.invalidateIDMap()
	}
	if err := s.forceTrainSmallDataLocked(); err != nil {
		return err
	}
	return s.saveLocked("", nil)
}

// writeCompactionTmpPairLocked 流式写出剔除 tombstone 的临时成对文件
// 向量行按原始字节拷贝，避免浮点往返改变归一化结果
func (s *VectorStore) writeCompactionTmpPairLocked(tmpBinPath string, tmpIDsPath string) (int64, error) {
	vectorBytes, idBytes, err := s.vectorPairSizesLocked()
	if err != nil {
		return 0, err
	}
	itemSize := s.vectorItemSizeLocked()
	totalRows := vectorBytes / itemSize
	if idBytes/8 != totalRows {
		return 0, fmt.Errorf("向量数据文件与 ID 文件记录数不一致")
	}

	inputVectors, err := os.Open(s.binPath())
	if err != nil {
		return 0, err
	}
	defer func() { _ = inputVectors.Close() }()
	inputIDs, err := os.Open(s.idsBinPath())
	if err != nil {
		return 0, err
	}
	defer func() { _ = inputIDs.Close() }()

	outputVectors, err := os.OpenFile(tmpBinPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o666)
	if err != nil {
		return 0, err
	}
	defer func() { _ = outputVectors.Close() }()
	outputIDs, err := os.OpenFile(tmpIDsPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o666)
	if err != nil {
		return 0, err
	}
	defer func() { _ = outputIDs.Close() }()

	const chunkRows = 10000
	vectorBuffer := make([]byte, chunkRows*int(itemSize))
	idBuffer := make([]byte, chunkRows*8)
	var written int64
	for processed := int64(0); processed < totalRows; {
		batch := int64(chunkRows)
		if totalRows-processed < batch {
			batch = totalRows - processed
		}
		vectorChunk := vectorBuffer[:int(batch)*int(itemSize)]
		idChunk := idBuffer[:int(batch)*8]
		if _, err := readFullAt(inputVectors, vectorChunk, processed*itemSize); err != nil {
			return 0, err
		}
		if _, err := readFullAt(inputIDs, idChunk, processed*8); err != nil {
			return 0, err
		}
		for index := 0; index < int(batch); index++ {
			intID := decodeBigEndianInt64(idChunk[index*8:])
			if _, deleted := s.deletedIDs[intID]; deleted {
				continue
			}
			if _, err := outputVectors.Write(vectorChunk[index*int(itemSize) : (index+1)*int(itemSize)]); err != nil {
				return 0, err
			}
			if _, err := outputIDs.Write(idChunk[index*8 : (index+1)*8]); err != nil {
				return 0, err
			}
			written++
		}
		processed += batch
	}
	if err := outputVectors.Sync(); err != nil {
		return 0, err
	}
	if err := outputIDs.Sync(); err != nil {
		return 0, err
	}
	return written, nil
}

// readFullAt 从指定偏移读满缓冲区
func readFullAt(file *os.File, buffer []byte, offset int64) (int, error) {
	read := 0
	for read < len(buffer) {
		count, err := file.ReadAt(buffer[read:], offset+int64(read))
		read += count
		if err != nil {
			if read == len(buffer) {
				return read, nil
			}
			return read, fmt.Errorf("向量成对文件读取不完整: offset=%d, want=%d, got=%d", offset, len(buffer), read)
		}
	}
	return read, nil
}

// recoverInterruptedCompactionLocked 恢复被中断的压缩事务
// 对齐 _recover_interrupted_compaction_unlocked
func (s *VectorStore) recoverInterruptedCompactionLocked() error {
	journal, err := s.readCompactionJournalLocked()
	if err != nil {
		return err
	}
	if journal == nil {
		return nil
	}
	s.invalidateIDOffsets()
	meta, err := s.readMetadataOrEmptyLocked()
	if err != nil {
		return err
	}
	targetVectorBytes, _ := jsonInteger(journal["target_vector_bytes"])
	targetIDBytes, _ := jsonInteger(journal["target_id_bytes"])
	vectorBytes, idBytes, err := s.vectorPairSizesLocked()
	if err != nil {
		return err
	}
	if metadataCommitsAppend(meta, journal) {
		if vectorBytes != targetVectorBytes || idBytes != targetIDBytes {
			return fmt.Errorf("向量压缩已提交，但成对文件未达到提交长度")
		}
		return s.finalizeCompactionCommitLocked()
	}

	if err := s.restoreCompactionBackupLocked(journal); err != nil {
		return err
	}
	// Python 侧旧索引已失效，删除它以强制重建
	if err := removeFileIfExists(s.indexPath()); err != nil {
		return err
	}

	purpose, _ := journal["purpose"].(string)
	switch {
	case purpose == "v1_reconciliation":
		s.resetEmptyRuntimeLocked()
	case s.journalDimensionMatchesLocked(journal):
		if err := s.reloadRuntimeAfterCompactionRollbackLocked(meta); err != nil {
			return err
		}
	default:
		storedDimension, _ := jsonInteger(journal["dimension"])
		zap.L().Warn(
			"向量压缩日志维度与当前实例不一致，跳过运行时重建",
			zap.Int64("stored_dimension", storedDimension),
			zap.Int("dimension", s.dimension),
		)
	}
	return s.finalizeCompactionCommitLocked()
}

// journalDimensionMatchesLocked 判断压缩日志维度是否与当前实例一致
func (s *VectorStore) journalDimensionMatchesLocked(journal map[string]any) bool {
	raw, ok := journal["dimension"]
	if !ok {
		return true
	}
	storedDimension, valid := jsonInteger(raw)
	return valid && int(storedDimension) == s.dimension
}

// restoreCompactionBackupLocked 从只读备份恢复成对文件
// 对齐 _restore_compaction_backup_unlocked
func (s *VectorStore) restoreCompactionBackupLocked(journal map[string]any) error {
	_, binBackupExists, err := statSize(s.binBackupPath())
	if err != nil {
		return err
	}
	_, idsBackupExists, err := statSize(s.idsBackupPath())
	if err != nil {
		return err
	}
	if !binBackupExists || !idsBackupExists {
		return fmt.Errorf("向量压缩被中断，且找不到成对备份")
	}
	baseVectorBytes, _ := jsonInteger(journal["base_vector_bytes"])
	baseIDBytes, _ := jsonInteger(journal["base_id_bytes"])
	backupVectorBytes, err := fileSizeOrZero(s.binBackupPath())
	if err != nil {
		return err
	}
	backupIDBytes, err := fileSizeOrZero(s.idsBackupPath())
	if err != nil {
		return err
	}
	itemSize := s.vectorItemSizeLocked()
	expectedRows := baseVectorBytes / itemSize
	if baseVectorBytes%itemSize != 0 ||
		baseIDBytes%8 != 0 ||
		expectedRows != baseIDBytes/8 ||
		backupVectorBytes != baseVectorBytes ||
		backupIDBytes != baseIDBytes {
		return fmt.Errorf("向量压缩备份与日志基线不一致")
	}

	restored := false
	for attempt := 0; attempt < 2; attempt++ {
		if err := copyAndSync(s.binBackupPath(), s.binRecoveryTmpPath()); err != nil {
			return err
		}
		if err := copyAndSync(s.idsBackupPath(), s.idsRecoveryTmpPath()); err != nil {
			return err
		}
		if !s.vectorPairMatches(s.binRecoveryTmpPath(), s.idsRecoveryTmpPath(), expectedRows, s.dimension) {
			return fmt.Errorf("向量压缩恢复临时文件不一致")
		}
		if err := s.replaceCompactionRecoveryPairLocked(); err != nil {
			return err
		}
		if s.vectorPairMatches(s.binPath(), s.idsBinPath(), expectedRows, s.dimension) {
			restored = true
			break
		}
	}
	if !restored {
		return fmt.Errorf("向量压缩备份恢复后校验失败")
	}
	s.invalidateIDOffsets()
	return nil
}

// replaceCompactionTmpPairLocked 用压缩临时文件替换成对文件，对齐 os.replace(tmp, pair)
func (s *VectorStore) replaceCompactionTmpPairLocked(tmpBinPath string, tmpIDsPath string) error {
	if err := os.Rename(tmpBinPath, s.binPath()); err != nil {
		return err
	}
	if err := os.Rename(tmpIDsPath, s.idsBinPath()); err != nil {
		return err
	}
	return nil
}

// replaceCompactionRecoveryPairLocked 用恢复临时文件替换成对文件，对齐 os.replace
func (s *VectorStore) replaceCompactionRecoveryPairLocked() error {
	if err := os.Rename(s.binRecoveryTmpPath(), s.binPath()); err != nil {
		return err
	}
	if err := os.Rename(s.idsRecoveryTmpPath(), s.idsBinPath()); err != nil {
		return err
	}
	return nil
}

// finalizeCompactionCommitLocked 删除压缩日志与备份，对齐 _finalize_compaction_transaction_unlocked
func (s *VectorStore) finalizeCompactionCommitLocked() error {
	if err := removeFileIfExists(s.compactionJournalPath()); err != nil {
		return err
	}
	if err := removeFileIfExists(s.binBackupPath()); err != nil {
		return err
	}
	return removeFileIfExists(s.idsBackupPath())
}

// readMetadataOrEmptyLocked 读取元数据，文件缺失返回空对象
func (s *VectorStore) readMetadataOrEmptyLocked() (map[string]any, error) {
	if _, exists, err := statSize(s.metadataPath()); err != nil {
		return nil, err
	} else if !exists {
		return map[string]any{}, nil
	}
	return readJSONObject(s.metadataPath())
}

// reloadRuntimeAfterCompactionRollbackLocked 回滚后按基线元数据重建运行时
// 对齐 _reload_runtime_after_compaction_rollback_unlocked
func (s *VectorStore) reloadRuntimeAfterCompactionRollbackLocked(meta map[string]any) error {
	if len(s.writeBufferVecs) != 0 || len(s.writeBufferIDs) != 0 {
		return fmt.Errorf("向量压缩回滚时存在未提交写缓冲，拒绝覆盖运行时状态")
	}
	if len(meta) == 0 {
		return fmt.Errorf("向量压缩回滚后缺少基线元数据")
	}
	known, err := jsonStringSlice(meta["known_hashes"])
	if err != nil {
		return fmt.Errorf("向量压缩基线元数据的 known_hashes 无效")
	}
	deleted, err := jsonInt64Slice(meta["deleted_ids"])
	if err != nil {
		return fmt.Errorf("向量压缩基线元数据的 deleted_ids 无效")
	}

	vectorBytes, _, err := s.vectorPairSizesLocked()
	if err != nil {
		return err
	}
	binCount := int(vectorBytes / s.vectorItemSizeLocked())
	expected := make(map[int64]int, len(known))
	for _, hashValue := range known {
		expected[s.generateID(hashValue)]++
	}
	disk, err := s.diskIDMultisetLocked()
	if err != nil {
		return err
	}
	if !multisetEqual(expected, disk) {
		return fmt.Errorf(
			"向量压缩基线元数据与恢复 pair 不一致: missing=%d, unexpected=%d",
			multisetDifference(expected, disk),
			multisetDifference(disk, expected),
		)
	}

	s.knownHashes = make(map[string]struct{}, len(known))
	for _, hashValue := range known {
		s.knownHashes[hashValue] = struct{}{}
	}
	s.deletedIDs = make(map[int64]struct{}, len(deleted))
	for _, intID := range deleted {
		s.deletedIDs[intID] = struct{}{}
	}
	s.binCount = binCount
	s.vectorNorm = "l2"
	s.invalidateIDMap()
	s.invalidateIDOffsets()

	trained := false
	if rawTrained, ok := meta["is_trained"].(bool); ok {
		trained = rawTrained
	}
	s.trained = trained
	if trained {
		return s.rebuildLoadedIndexLocked()
	}
	if binCount > 0 {
		if err := s.bootstrapFallbackLocked(); err != nil {
			return err
		}
		if s.fallbackNtotal() != len(s.knownHashes)-len(s.deletedIDs) {
			return fmt.Errorf("向量压缩回滚后 fallback 索引与基线元数据不一致")
		}
	}
	return nil
}
