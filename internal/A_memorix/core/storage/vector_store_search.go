package storage

// A_memorix 向量写入、检索与读取
// 对齐 Python 的 add / _flush_write_buffer / search / get_vectors / iter_vectors_by_ids / warmup_index

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"go.uber.org/zap"
)

// Add 写入向量，对齐 VectorStore.add 的校验顺序与返回值
// 向量必须已归一化前的原始值，内部会做 L2 归一化后写入
func (s *VectorStore) Add(vectors [][]float32, ids []string) (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.addLocked(vectors, ids)
}

// addLocked 是 Add 的持锁实现，供旧格式迁移复用
func (s *VectorStore) addLocked(vectors [][]float32, ids []string) (int, error) {
	if err := s.raiseIfCleanupCheckpointBrokenLocked(); err != nil {
		return 0, err
	}
	if err := validateVectorBatch(vectors, ids, s.dimension); err != nil {
		return 0, err
	}

	normalized := make([][]float32, len(vectors))
	for index, vector := range vectors {
		row := append([]float32(nil), vector...)
		normalizeVector(row)
		normalized[index] = row
	}

	tombstoned := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, known := s.knownHashes[id]; !known {
			continue
		}
		if _, deleted := s.deletedIDs[s.generateID(id)]; deleted {
			tombstoned = append(tombstoned, id)
		}
	}
	if len(tombstoned) != 0 {
		return 0, fmt.Errorf("%d 个向量 ID 已被删除，请先调用 restore() 恢复", len(tombstoned))
	}

	pendingVectors := make([][]float32, 0, len(ids))
	pendingIDs := make([]int64, 0, len(ids))
	pendingHashes := make([]string, 0, len(ids))
	for index, id := range ids {
		if _, known := s.knownHashes[id]; known {
			continue
		}
		pendingVectors = append(pendingVectors, normalized[index])
		pendingIDs = append(pendingIDs, s.generateID(id))
		pendingHashes = append(pendingHashes, id)
	}
	if len(pendingHashes) == 0 {
		return 0, nil
	}

	s.writeBufferVecs = append(s.writeBufferVecs, pendingVectors...)
	s.writeBufferIDs = append(s.writeBufferIDs, pendingIDs...)
	for _, hashValue := range pendingHashes {
		s.knownHashes[hashValue] = struct{}{}
	}
	s.invalidateIDMap()

	if len(s.writeBufferIDs) >= s.bufferSize {
		if err := s.flushWriteBufferLocked(); err != nil {
			return 0, err
		}
	}

	if !s.trained {
		// 未训练阶段由 flush 统一写入回退索引，这里只更新储水池
		s.updateReservoirLocked(pendingVectors)
		if len(s.reservoir) >= VectorTrainSize {
			zap.L().Info("训练样本达到上限，开始训练...")
			s.trainAndReplayLocked()
		}
	}

	s.totalAdded += len(pendingIDs)
	return len(pendingIDs), nil
}

// validateVectorBatch 按 Python add 的顺序校验形状、ID、有限性与范数
func validateVectorBatch(vectors [][]float32, ids []string, dimension int) error {
	if len(vectors) == 0 {
		return fmt.Errorf("vectors must have shape (N, D), got (0,)")
	}
	width := len(vectors[0])
	for _, vector := range vectors {
		if len(vector) != width {
			return fmt.Errorf("vectors must have shape (N, D), got (%d,)", len(vectors))
		}
	}
	if len(vectors) != len(ids) {
		return fmt.Errorf("Vector/ID count mismatch: %d vs %d", len(vectors), len(ids))
	}
	if width != dimension {
		return fmt.Errorf("Dimension mismatch: %d vs %d", width, dimension)
	}
	for _, id := range ids {
		if id == "" {
			return fmt.Errorf("Vector IDs must be non-empty strings")
		}
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("Vector IDs must be unique within one add batch")
		}
		seen[id] = struct{}{}
	}
	for _, vector := range vectors {
		for _, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return fmt.Errorf("vectors contain non-finite values")
			}
		}
	}
	for _, vector := range vectors {
		if vectorNormValue(vector) <= 1e-12 {
			return fmt.Errorf("vectors contain zero-norm rows")
		}
	}
	return nil
}

// pairRow 描述从成对文件读出的一行
type pairRow struct {
	id     int64
	vector []float32
}

// readPairRowsLocked 流式读取成对文件，keep 返回 false 时跳过该行
// 读出的向量已做 L2 归一化
func (s *VectorStore) readPairRowsLocked(keep func(intID int64, vector []float32) bool) ([]pairRow, error) {
	binExists := true
	if _, exists, err := statSize(s.binPath()); err != nil {
		return nil, err
	} else if !exists {
		binExists = false
	}
	idsExists := true
	if _, exists, err := statSize(s.idsBinPath()); err != nil {
		return nil, err
	} else if !exists {
		idsExists = false
	}
	if !binExists || !idsExists {
		return nil, nil
	}

	vectorFile, err := os.Open(s.binPath())
	if err != nil {
		return nil, err
	}
	defer func() { _ = vectorFile.Close() }()
	idsFile, err := os.Open(s.idsBinPath())
	if err != nil {
		return nil, err
	}
	defer func() { _ = idsFile.Close() }()

	itemSize := s.vectorItemSizeLocked()
	const chunkRows = 10000
	vectorBuffer := make([]byte, int64(chunkRows)*itemSize)
	idBuffer := make([]byte, chunkRows*8)
	rows := make([]pairRow, 0, 1024)
	for {
		vectorRead, vectorErr := vectorFile.Read(vectorBuffer)
		if vectorRead == 0 {
			if vectorErr != nil && !errors.Is(vectorErr, io.EOF) {
				return nil, vectorErr
			}
			break
		}
		idRead, idErr := idsFile.Read(idBuffer)
		if idRead == 0 && idErr != nil {
			return nil, idErr
		}
		count := vectorRead / int(itemSize)
		if idRead/8 < count {
			count = idRead / 8
		}
		for index := 0; index < count; index++ {
			intID := int64(decodeBigEndianInt64(idBuffer[index*8:]))
			vector := decodeVectorRow(vectorBuffer[index*int(itemSize):], s.dimension, s.vectorDtype)
			normalizeVector(vector)
			if keep != nil && !keep(intID, vector) {
				continue
			}
			rows = append(rows, pairRow{id: intID, vector: vector})
		}
		if vectorRead < len(vectorBuffer) {
			break
		}
	}
	return rows, nil
}

// decodeBigEndianInt64 把大端 8 字节解释为 int64
func decodeBigEndianInt64(raw []byte) int64 {
	var value uint64
	for index := 0; index < 8; index++ {
		value = value<<8 | uint64(raw[index])
	}
	return int64(value)
}

// Search 检索向量，对齐 VectorStore.search
// 返回按分数降序排列的字符串 ID 与内积分数（L2 归一化后即余弦相似度）
func (s *VectorStore) Search(query []float32, k int, filterDeleted bool, allowedIDs []string) ([]string, []float64, error) {
	if len(query) != s.dimension {
		if len(query) == 0 {
			return nil, nil, fmt.Errorf("query embedding must have shape (D,) or (1, D), got (0,)")
		}
		return nil, nil, fmt.Errorf("query embedding dimension mismatch: expected=%d got=%d", s.dimension, len(query))
	}
	for _, value := range query {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, nil, fmt.Errorf("query embedding contains non-finite values")
		}
	}

	var allowed map[string]struct{}
	if allowedIDs != nil {
		allowed = make(map[string]struct{}, len(allowedIDs))
		for _, value := range allowedIDs {
			allowed[value] = struct{}{}
		}
		if len(allowed) == 0 {
			return []string{}, []float64{}, nil
		}
	}

	queryLocal := append([]float32(nil), query...)
	normalizeVector(queryLocal)

	s.lock.Lock()
	defer s.lock.Unlock()
	if err := s.raiseIfCleanupCheckpointBrokenLocked(); err != nil {
		return nil, nil, err
	}
	if err := s.flushWriteBufferLocked(); err != nil {
		return nil, nil, err
	}
	if len(s.entries) == 0 {
		zap.L().Warn("Indices are empty. No data to search.")
		return []string{}, []float64{}, nil
	}
	requestedK := max(1, k)
	idToStr := s.intToStrMapLocked()

	type candidate struct {
		hashValue string
		score     float64
	}
	candidates := make([]candidate, 0, len(s.entryOrder))
	seen := make(map[int64]struct{}, len(s.entryOrder))
	for _, intID := range s.entryOrder {
		if _, duplicate := seen[intID]; duplicate {
			continue
		}
		vector, ok := s.entries[intID]
		if !ok {
			continue
		}
		seen[intID] = struct{}{}
		if filterDeleted {
			if _, deleted := s.deletedIDs[intID]; deleted {
				continue
			}
		}
		hashValue, ok := idToStr[intID]
		if !ok {
			continue
		}
		if allowed != nil {
			if _, ok := allowed[hashValue]; !ok {
				continue
			}
		}
		candidates = append(candidates, candidate{hashValue: hashValue, score: float64(dotProduct(queryLocal, vector))})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	if len(candidates) > requestedK {
		candidates = candidates[:requestedK]
	}
	if len(candidates) == 0 {
		return []string{}, []float64{}, nil
	}
	ids := make([]string, 0, len(candidates))
	scores := make([]float64, 0, len(candidates))
	for _, item := range candidates {
		ids = append(ids, item.hashValue)
		scores = append(scores, item.score)
	}
	return ids, scores, nil
}

// GetVectors 按字符串 ID 读取已持久化向量，对齐 get_vectors
func (s *VectorStore) GetVectors(ids []string) (map[string][]float32, error) {
	result := map[string][]float32{}
	err := s.IterVectorsByIDs(ids, 1024, func(batch map[string][]float32) error {
		for key, value := range batch {
			result[key] = value
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// IterVectorsByIDs 分批读取持久化向量，对齐 iter_vectors_by_ids
func (s *VectorStore) IterVectorsByIDs(ids []string, batchSize int, fn func(batch map[string][]float32) error) error {
	s.lock.Lock()
	if err := s.raiseIfCleanupCheckpointBrokenLocked(); err != nil {
		s.lock.Unlock()
		return err
	}
	requested := make([]string, 0, len(ids))
	for _, item := range ids {
		trimmed := strings.TrimFunc(item, pyIsSpace)
		if trimmed != "" {
			requested = append(requested, trimmed)
		}
	}
	if len(requested) == 0 {
		s.lock.Unlock()
		return nil
	}
	safeBatchSize := batchSize
	if safeBatchSize <= 0 {
		safeBatchSize = 1024
	}
	if err := s.flushWriteBufferLocked(); err != nil {
		s.lock.Unlock()
		return err
	}
	intToStr := make(map[int64]string, len(requested))
	order := make([]int64, 0, len(requested))
	for _, hashValue := range requested {
		if _, known := s.knownHashes[hashValue]; !known {
			continue
		}
		intID := s.generateID(hashValue)
		if _, deleted := s.deletedIDs[intID]; deleted {
			continue
		}
		if _, duplicate := intToStr[intID]; duplicate {
			continue
		}
		intToStr[intID] = hashValue
		order = append(order, intID)
	}
	if len(intToStr) == 0 {
		s.lock.Unlock()
		return nil
	}
	pending := make(map[int64]struct{}, len(intToStr))
	for _, intID := range order {
		pending[intID] = struct{}{}
	}
	vectors, err := s.readPersistedVectorsLocked(pending)
	s.lock.Unlock()
	if err != nil {
		return err
	}

	batch := map[string][]float32{}
	for _, intID := range order {
		vector, ok := vectors[intID]
		if !ok {
			continue
		}
		normalizeVector(vector)
		batch[intToStr[intID]] = vector
		if len(batch) >= safeBatchSize {
			if err := fn(batch); err != nil {
				return err
			}
			batch = map[string][]float32{}
		}
	}
	if len(batch) != 0 {
		if err := fn(batch); err != nil {
			return err
		}
	}
	return nil
}

// ensureIDOffsetsLocked 建立磁盘 ID 到行号的唯一映射，对齐 _ensure_id_offsets_unlocked
func (s *VectorStore) ensureIDOffsetsLocked() error {
	if s.dataDir == "" {
		return fmt.Errorf("No data_dir")
	}
	_, binExists, err := statSize(s.binPath())
	if err != nil {
		return err
	}
	_, idsExists, err := statSize(s.idsBinPath())
	if err != nil {
		return err
	}
	if !binExists && !idsExists {
		s.idOffsets = map[int64]int64{}
		s.idOffsetsCount = 0
		return nil
	}
	if !s.vectorPairMatches(s.binPath(), s.idsBinPath(), -1, s.dimension) {
		return fmt.Errorf("向量数据文件与 ID 文件不一致，拒绝恢复")
	}
	idsSize, err := fileSizeOrZero(s.idsBinPath())
	if err != nil {
		return err
	}
	count := int(idsSize / 8)
	if s.idOffsetsCount == count {
		return nil
	}
	rawIDs, err := readVectorIDFile(s.idsBinPath())
	if err != nil {
		return err
	}
	offsets := make(map[int64]int64, len(rawIDs))
	for offset, intID := range rawIDs {
		if _, duplicate := offsets[intID]; duplicate {
			return fmt.Errorf("向量磁盘文件存在重复 int64 ID: %d", intID)
		}
		offsets[intID] = int64(offset)
	}
	s.idOffsets = offsets
	s.idOffsetsCount = len(rawIDs)
	return nil
}

// readPersistedVectorsLocked 读取指定 ID 的持久化向量（含 tombstone），对齐 _read_persisted_vectors_unlocked
func (s *VectorStore) readPersistedVectorsLocked(intIDs map[int64]struct{}) (map[int64][]float32, error) {
	result := map[int64][]float32{}
	if len(intIDs) == 0 {
		return result, nil
	}
	if err := s.ensureIDOffsetsLocked(); err != nil {
		return nil, err
	}
	if len(s.idOffsets) == 0 {
		return result, nil
	}
	itemSize := s.vectorItemSizeLocked()
	file, err := os.Open(s.binPath())
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	raw := make([]byte, itemSize)
	for intID := range intIDs {
		offset, ok := s.idOffsets[intID]
		if !ok {
			continue
		}
		if _, err := file.ReadAt(raw, offset*itemSize); err != nil {
			return nil, fmt.Errorf("向量数据读取不完整: int_id=%d", intID)
		}
		result[intID] = decodeVectorRow(raw, s.dimension, s.vectorDtype)
	}
	return result, nil
}

// IsTombstoned 判断字符串 ID 是否已被软删除，对齐 is_tombstoned
func (s *VectorStore) IsTombstoned(hashValue string) (bool, error) {
	if hashValue == "" {
		return false, fmt.Errorf("Vector ID must be a non-empty string")
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	if _, known := s.knownHashes[hashValue]; !known {
		return false, nil
	}
	_, deleted := s.deletedIDs[s.generateID(hashValue)]
	return deleted, nil
}

// HasData 判断元数据文件是否存在，对齐 has_data
func (s *VectorStore) HasData() bool {
	s.lock.Lock()
	defer s.lock.Unlock()
	_, exists, err := statSize(s.metadataPath())
	return err == nil && exists
}

// NumVectors 返回活动向量数量，对齐 num_vectors
func (s *VectorStore) NumVectors() int {
	s.lock.Lock()
	defer s.lock.Unlock()
	return len(s.knownHashes) - len(s.deletedIDs)
}

// Contains 判断字符串 ID 是否可用，对齐 __contains__
func (s *VectorStore) Contains(hashValue string) bool {
	s.lock.Lock()
	defer s.lock.Unlock()
	if _, known := s.knownHashes[hashValue]; !known {
		return false
	}
	_, deleted := s.deletedIDs[s.generateID(hashValue)]
	return !deleted
}

// NeedsTraining 判断是否达到训练阈值，对齐 needs_training
func (s *VectorStore) NeedsTraining(minSamples int) bool {
	s.lock.Lock()
	defer s.lock.Unlock()
	// 对齐 Python：min_train_threshold 是下限，运行期阈值只能抬高门槛
	threshold := max(1, s.minTrainThreshold, minSamples)
	active := len(s.knownHashes) - len(s.deletedIDs)
	return !s.trained && active >= threshold
}
