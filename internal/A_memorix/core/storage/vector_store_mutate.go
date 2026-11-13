package storage

// A_memorix 向量软删除与恢复
// 对齐 Python 的 delete / restore / _check_rebuild_needed

import (
	"fmt"
	"math"
)

// Delete 软删除向量，对齐 VectorStore.delete
// 只加入 tombstone 并从内存索引移除，不重写 append-only 文件
func (s *VectorStore) Delete(ids []string) (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if err := s.raiseIfCleanupCheckpointBrokenLocked(); err != nil {
		return 0, err
	}
	count := 0
	for _, hashValue := range ids {
		if _, known := s.knownHashes[hashValue]; !known {
			continue
		}
		intID := s.generateID(hashValue)
		if _, deleted := s.deletedIDs[intID]; deleted {
			continue
		}
		s.deletedIDs[intID] = struct{}{}
		s.removeEntryLocked(intID)
		count++
	}
	s.totalDeleted += count
	if err := s.checkRebuildNeededLocked(); err != nil {
		return count, err
	}
	return count, nil
}

// removeEntryLocked 从内存索引移除单个 ID，保持插入顺序
func (s *VectorStore) removeEntryLocked(intID int64) {
	delete(s.entries, intID)
	for index, value := range s.entryOrder {
		if value == intID {
			s.entryOrder = append(s.entryOrder[:index], s.entryOrder[index+1:]...)
			return
		}
	}
}

// appendEntryLocked 写回单个向量并记录插入顺序，重复 ID 先移除
func (s *VectorStore) appendEntryLocked(intID int64, vector []float32) {
	s.removeEntryLocked(intID)
	s.entries[intID] = vector
	s.entryOrder = append(s.entryOrder, intID)
}

// checkRebuildNeededLocked 判断是否需要触发压缩，对齐 _check_rebuild_needed
func (s *VectorStore) checkRebuildNeededLocked() error {
	if s.binCount == 0 {
		return nil
	}
	ratio := float64(len(s.deletedIDs)) / float64(s.binCount)
	if ratio > 0.3 && len(s.deletedIDs) > 1000 {
		if s.cleanupCheckpoint != nil {
			s.cleanupCompactionDeferred = true
			return nil
		}
		return s.rebuildIndexLocked()
	}
	return nil
}

// Restore 恢复 tombstone 向量，对齐 VectorStore.restore
// 只从已有成对文件读回数据，不向 append-only 文件写入重复 ID
func (s *VectorStore) Restore(ids []string) (int, error) {
	for _, hashValue := range ids {
		if hashValue == "" {
			return 0, fmt.Errorf("Vector IDs must be non-empty strings")
		}
	}
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, hashValue := range ids {
		if _, duplicate := seen[hashValue]; duplicate {
			continue
		}
		seen[hashValue] = struct{}{}
		unique = append(unique, hashValue)
	}
	if len(unique) == 0 {
		return 0, nil
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	if err := s.raiseIfCleanupCheckpointBrokenLocked(); err != nil {
		return 0, err
	}
	if err := s.flushWriteBufferLocked(); err != nil {
		return 0, err
	}

	intToStr := make(map[int64]string, len(unique))
	pending := make(map[int64]struct{}, len(unique))
	for _, hashValue := range unique {
		intID := s.generateID(hashValue)
		if _, known := s.knownHashes[hashValue]; !known {
			continue
		}
		if _, deleted := s.deletedIDs[intID]; !deleted {
			continue
		}
		if colliding, exists := intToStr[intID]; exists && colliding != hashValue {
			return 0, fmt.Errorf("向量 ID 冲突: %s / %s", colliding, hashValue)
		}
		intToStr[intID] = hashValue
		pending[intID] = struct{}{}
	}
	if len(intToStr) == 0 {
		return 0, nil
	}

	persisted, err := s.readPersistedVectorsLocked(pending)
	if err != nil {
		return 0, err
	}
	if len(persisted) == 0 {
		return 0, nil
	}

	restoredIDs := make([]int64, 0, len(persisted))
	for intID := range persisted {
		restoredIDs = append(restoredIDs, intID)
	}
	added := make([]int64, 0, len(restoredIDs))
	for _, intID := range restoredIDs {
		vector := persisted[intID]
		if len(vector) != s.dimension {
			return 0, fmt.Errorf("向量数据读取不完整: int_id=%d", intID)
		}
		for _, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return 0, fmt.Errorf("向量数据包含非有限值: int_id=%d", intID)
			}
		}
		normalizeVector(vector)
		s.appendEntryLocked(intID, vector)
		added = append(added, intID)
	}
	for _, intID := range added {
		delete(s.deletedIDs, intID)
	}
	return len(added), nil
}
