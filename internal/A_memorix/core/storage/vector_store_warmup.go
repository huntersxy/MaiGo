package storage

// A_memorix 向量储水池采样、训练与索引预热
// 对齐 Python 的 _update_reservoir / _train_and_replay / warmup_index

import (
	"math/rand"
	"time"

	"go.uber.org/zap"
)

// updateReservoirLocked 维护储水池采样，对齐 _update_reservoir
func (s *VectorStore) updateReservoirLocked(vectors [][]float32) {
	for _, vector := range vectors {
		s.seenCountForReservoir++
		if len(s.reservoir) < VectorReservoirCapacity {
			s.reservoir = append(s.reservoir, append([]float32(nil), vector...))
			continue
		}
		if s.seenCountForReservoir <= VectorReservoirSampleScope {
			position := randomIntn(s.seenCountForReservoir)
			if position < VectorReservoirCapacity {
				s.reservoir[position] = append([]float32(nil), vector...)
			}
		}
	}
}

// randomIntn 返回 [0, limit) 的伪随机数，limit 必须为正
func randomIntn(limit int) int {
	if limit <= 1 {
		return 0
	}
	return int(rand.Int63n(int64(limit)))
}

// trainAndReplayLocked 训练并回放磁盘向量，对齐 _train_and_replay_unlocked
// Go 版本没有量化索引，训练等价于「切换到精确索引 + 按磁盘回放」
func (s *VectorStore) trainAndReplayLocked() {
	if len(s.reservoir) == 0 {
		zap.L().Warn("No training data available.")
		return
	}
	s.trained = true
	s.reservoir = nil
	if err := s.replayVectorsLocked(); err != nil {
		zap.L().Error("Replay failed, keeping fallback index as backup", zap.Error(err))
	}
}

// replayVectorsLocked 按磁盘内容重建内存索引，跳过 tombstone
func (s *VectorStore) replayVectorsLocked() error {
	rows, err := s.readPairRowsLocked(func(int64, []float32) bool { return true })
	if err != nil {
		return err
	}
	entries := make(map[int64][]float32, len(rows))
	order := make([]int64, 0, len(rows))
	for _, row := range rows {
		if _, deleted := s.deletedIDs[row.id]; deleted {
			continue
		}
		entries[row.id] = row.vector
		order = append(order, row.id)
	}
	s.entries = entries
	s.entryOrder = order
	return nil
}

// bootstrapFallbackLocked 从磁盘自举内存索引，对齐 _bootstrap_fallback_from_disk_unlocked
func (s *VectorStore) bootstrapFallbackLocked() error {
	rows, err := s.readPairRowsLocked(func(int64, []float32) bool { return true })
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, deleted := s.deletedIDs[row.id]; deleted {
			continue
		}
		if _, exists := s.entries[row.id]; exists {
			continue
		}
		s.entries[row.id] = row.vector
		s.entryOrder = append(s.entryOrder, row.id)
	}
	return nil
}

// forceTrainSmallDataLocked 用小数据集强制训练，对齐 _force_train_small_data_unlocked
func (s *VectorStore) forceTrainSmallDataLocked() error {
	s.reservoir = nil
	rows, err := s.readPairRowsLocked(func(int64, []float32) bool { return true })
	if err != nil {
		return err
	}
	for _, row := range rows {
		if len(s.reservoir) >= VectorTrainSize {
			break
		}
		s.reservoir = append(s.reservoir, row.vector)
	}
	s.trainAndReplayLocked()
	return nil
}

// WarmupIndex 预热索引，对齐 warmup_index 的返回结构
func (s *VectorStore) WarmupIndex(forceTrain bool) map[string]any {
	started := time.Now()
	s.lock.Lock()
	err := s.warmupLocked(forceTrain)
	duration := float64(time.Since(started).Microseconds()) / 1000.0
	summary := map[string]any{
		"ok":              err == nil,
		"trained":         s.trained,
		"index_ntotal":    s.indexNtotal(),
		"fallback_ntotal": s.fallbackNtotal(),
		"bin_count":       s.binCount,
		"duration_ms":     duration,
		"error":           nil,
	}
	if err != nil {
		summary["error"] = err.Error()
	}
	s.lock.Unlock()
	return summary
}

// warmupLocked 执行预热流程，调用方需持有锁
func (s *VectorStore) warmupLocked(forceTrain bool) error {
	if err := s.raiseIfCleanupCheckpointBrokenLocked(); err != nil {
		return err
	}
	if err := s.flushWriteBufferLocked(); err != nil {
		return err
	}
	binSize, err := fileSizeOrZero(s.binPath())
	if err != nil {
		return err
	}
	s.binCount = int(binSize / s.vectorItemSizeLocked())

	needsBootstrap := s.binCount > 0 && s.fallbackNtotal() == 0 && (!s.trained || s.indexNtotal() == 0)
	if needsBootstrap {
		if err := s.bootstrapFallbackLocked(); err != nil {
			return err
		}
	}
	minTrain := max(1, s.minTrainThreshold)
	needsTrain := forceTrain && s.binCount >= minTrain && !s.trained
	if needsTrain {
		if err := s.forceTrainSmallDataLocked(); err != nil {
			return err
		}
	}
	return nil
}
