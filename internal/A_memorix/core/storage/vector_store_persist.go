package storage

// A_memorix 向量元数据提交、加载与旧版本元数据升级
// 对齐 Python 的 save / load / _validate_or_migrate_vector_metadata_unlocked
//
// 与 Python 的差异：Python 在 is_trained 时额外序列化 FAISS 索引 vectors.index，
// Go 版本不写该文件，加载时直接从成对文件重建内存索引（结果等价）

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"go.uber.org/zap"
)

// resolvePath 返回绝对路径，用于判断调用方是否指向同一数据目录
func resolvePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(absolute)
}

// Save 提交元数据，对齐 VectorStore.save
func (s *VectorStore) Save(dataDir string, embeddingFingerprint map[string]any) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.saveLocked(dataDir, embeddingFingerprint)
}

// saveLocked 是 Save 的持锁实现，供压缩与 checkpoint 回滚复用
func (s *VectorStore) saveLocked(dataDir string, embeddingFingerprint map[string]any) error {
	if err := s.raiseIfCleanupCheckpointBrokenLocked(); err != nil {
		return err
	}
	target := dataDir
	if target == "" {
		target = s.dataDir
	}
	if target == "" {
		return fmt.Errorf("No data_dir")
	}
	if s.dataDir == "" || resolvePath(target) != resolvePath(s.dataDir) {
		return fmt.Errorf("VectorStore 只能向初始化时绑定的 data_dir 提交")
	}

	if err := s.flushWriteBufferLocked(); err != nil {
		return err
	}
	vectorBytes, idBytes, err := s.vectorPairSizesLocked()
	if err != nil {
		return err
	}
	appendJournal, err := s.readAppendJournalLocked()
	if err != nil {
		return err
	}
	compactionJournal, err := s.readCompactionJournalLocked()
	if err != nil {
		return err
	}
	if appendJournal != nil && compactionJournal != nil {
		return fmt.Errorf("向量追加与压缩事务不能同时存在")
	}
	if appendJournal != nil {
		targetVectorBytes, _ := jsonInteger(appendJournal["target_vector_bytes"])
		targetIDBytes, _ := jsonInteger(appendJournal["target_id_bytes"])
		if vectorBytes != targetVectorBytes || idBytes != targetIDBytes {
			return fmt.Errorf("向量追加文件未达到日志目标长度，拒绝提交")
		}
	}
	if compactionJournal != nil {
		targetVectorBytes, _ := jsonInteger(compactionJournal["target_vector_bytes"])
		targetIDBytes, _ := jsonInteger(compactionJournal["target_id_bytes"])
		if vectorBytes != targetVectorBytes || idBytes != targetIDBytes {
			return fmt.Errorf("向量压缩文件未达到日志目标长度，拒绝提交")
		}
	}

	var binaryTransactionID any
	if appendJournal != nil {
		binaryTransactionID = appendJournal["transaction_id"]
	} else if compactionJournal != nil {
		binaryTransactionID = compactionJournal["transaction_id"]
	}

	var previousFingerprint map[string]any
	if embeddingFingerprint == nil {
		if _, exists, statErr := statSize(s.metadataPath()); statErr != nil {
			return statErr
		} else if exists {
			previousMeta, readErr := readJSONObject(s.metadataPath())
			if readErr != nil {
				zap.L().Warn("读取旧向量元数据失败，跳过 embedding 指纹继承", zap.Error(readErr))
			} else {
				if raw := asJSONObject(previousMeta["embedding_fingerprint"]); len(raw) != 0 {
					previousFingerprint = raw
				}
			}
		}
	}

	knownHashes := make([]string, 0, len(s.knownHashes))
	for hashValue := range s.knownHashes {
		knownHashes = append(knownHashes, hashValue)
	}
	sort.Strings(knownHashes)
	deletedIDs := make([]int64, 0, len(s.deletedIDs))
	for intID := range s.deletedIDs {
		deletedIDs = append(deletedIDs, intID)
	}
	sort.Slice(deletedIDs, func(i, j int) bool { return deletedIDs[i] < deletedIDs[j] })

	meta := map[string]any{
		"dimension":         s.dimension,
		"quantization_type": string(s.quantizationType),
		"is_trained":        s.trained,
		"vector_norm":       s.vectorNorm,
		"deleted_ids":       deletedIDs,
		"known_hashes":      knownHashes,
		"schema_version":    2,
		"vector_dtype":      s.vectorDtype,
		"binary_commit": map[string]any{
			"transaction_id": binaryTransactionID,
			"vector_bytes":   vectorBytes,
			"id_bytes":       idBytes,
		},
	}
	if len(embeddingFingerprint) != 0 {
		meta["embedding_fingerprint"] = embeddingFingerprint
	} else if len(previousFingerprint) != 0 {
		meta["embedding_fingerprint"] = previousFingerprint
	}

	if err := writeJSONObject(s.metadataPath(), meta); err != nil {
		return err
	}
	if appendJournal != nil {
		if err := s.finalizeAppendCommitLocked(); err != nil {
			return err
		}
	}
	if compactionJournal != nil {
		if err := s.finalizeCompactionCommitLocked(); err != nil {
			return err
		}
	}
	zap.L().Debug("VectorStore saved.")
	return nil
}

// Load 从数据目录加载，对齐 VectorStore.load
func (s *VectorStore) Load(dataDir string, expectedEmbeddingFingerprint map[string]any, v1ValidHashes []string, v1EvidenceRoot string) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.loadLocked(dataDir, expectedEmbeddingFingerprint, v1ValidHashes, v1EvidenceRoot)
}

// loadLocked 是 Load 的持锁实现，供 checkpoint 回滚复用
func (s *VectorStore) loadLocked(dataDir string, expectedEmbeddingFingerprint map[string]any, v1ValidHashes []string, v1EvidenceRoot string) error {
	if err := s.raiseIfCleanupCheckpointBrokenLocked(); err != nil {
		return err
	}
	target := dataDir
	if target == "" {
		target = s.dataDir
	}
	if s.dataDir == "" || resolvePath(target) != resolvePath(s.dataDir) {
		return fmt.Errorf("VectorStore 只能从初始化时绑定的 data_dir 加载")
	}

	// 先按元数据确定行宽，否则 float16 / float32 成对文件无法正确判长
	metaExists := false
	if _, exists, err := statSize(s.metadataPath()); err != nil {
		return err
	} else if exists {
		metaExists = true
		peeked, readErr := readJSONObject(s.metadataPath())
		if readErr != nil {
			return readErr
		}
		s.vectorDtype = storedVectorDtype(peeked)
	}

	if err := s.recoverInterruptedAppendLocked(); err != nil {
		return err
	}
	if err := s.recoverInterruptedCompactionLocked(); err != nil {
		return err
	}

	var preloadedMeta map[string]any
	if metaExists {
		meta, readErr := readJSONObject(s.metadataPath())
		if readErr != nil {
			return readErr
		}
		preloadedMeta = meta
	}
	if preloadedMeta != nil {
		storedDimension := int64(s.dimension)
		if raw, ok := preloadedMeta["dimension"]; ok {
			parsed, valid := jsonInteger(raw)
			if !valid {
				storedDimension = -1
			} else {
				storedDimension = parsed
			}
		}
		if storedDimension != int64(s.dimension) {
			schemaVersion, schemaOK := jsonInteger(preloadedMeta["schema_version"])
			errorCode := "v1_dimension_mismatch"
			if schemaOK && schemaVersion == 2 {
				errorCode = "v2_dimension_mismatch"
			}
			return NewVectorStoreIntegrityError(
				fmt.Sprintf("向量维度与当前实例不一致: %s != %d", pyFormatValue(preloadedMeta["dimension"]), s.dimension),
				VectorStoreIntegrityOptions{
					ErrorCode:         errorCode,
					DimensionStatus:   "mismatched",
					FingerprintStatus: vectorFingerprintStatus(asJSONObject(preloadedMeta["embedding_fingerprint"]), expectedEmbeddingFingerprint),
					Details: map[string]any{
						"stored_dimension":   preloadedMeta["dimension"],
						"expected_dimension": s.dimension,
					},
				},
			)
		}
	}

	vectorBytes, idBytes, err := s.vectorPairSizesLocked()
	if err != nil {
		return err
	}
	s.binCount = int(vectorBytes / s.vectorItemSizeLocked())

	npyPath := filepath.Join(s.dataDir, "vectors.npy")
	_, npyExists, npyErr := statSize(npyPath)
	if npyErr != nil {
		return npyErr
	}
	_, binExists, binErr := statSize(s.binPath())
	if binErr != nil {
		return binErr
	}
	if npyExists && !binExists {
		return fmt.Errorf("检测到 legacy vectors.npy，vNext 不再支持运行时自动迁移。 请先执行 scripts/release_vnext_migrate.py migrate。")
	}

	if !metaExists {
		if vectorBytes != 0 {
			return fmt.Errorf("检测到未受元数据管理的向量二进制文件")
		}
		zap.L().Warn("No metadata found, initialized empty.")
		return nil
	}

	meta, err := s.validateOrMigrateVectorMetadataLocked(
		preloadedMeta,
		vectorBytes,
		idBytes,
		expectedEmbeddingFingerprint,
		v1ValidHashes,
		v1EvidenceRoot,
	)
	if err != nil {
		return err
	}

	if stored, ok := meta["vector_norm"].(string); !ok || stored != "l2" {
		zap.L().Warn("Index IDMap2 version mismatch (L2 Norm), forcing rebuild...")
		legacyKnown, legacyErr := jsonStringSlice(meta["ids"], meta["known_hashes"])
		if legacyErr != nil && meta["ids"] != nil {
			return legacyErr
		}
		s.knownHashes = map[string]struct{}{}
		for _, hashValue := range legacyKnown {
			s.knownHashes[hashValue] = struct{}{}
		}
		deleted, deletedErr := jsonInt64Slice(meta["deleted_ids"])
		if deletedErr != nil {
			return deletedErr
		}
		s.deletedIDs = map[int64]struct{}{}
		for _, intID := range deleted {
			s.deletedIDs[intID] = struct{}{}
		}
		s.invalidateIDMap()
		return s.rebuildLoadedIndexLocked()
	}

	if rawTrained, ok := meta["is_trained"].(bool); ok {
		s.trained = rawTrained
	} else {
		s.trained = false
	}
	if stored, ok := meta["vector_norm"].(string); ok && stored != "" {
		s.vectorNorm = stored
	}
	deleted, err := jsonInt64Slice(meta["deleted_ids"])
	if err != nil {
		return err
	}
	s.deletedIDs = map[int64]struct{}{}
	for _, intID := range deleted {
		s.deletedIDs[intID] = struct{}{}
	}
	known, err := jsonStringSlice(meta["known_hashes"])
	if err != nil {
		return err
	}
	s.knownHashes = map[string]struct{}{}
	for _, hashValue := range known {
		s.knownHashes[hashValue] = struct{}{}
	}
	s.invalidateIDMap()
	s.invalidateIDOffsets()

	if s.trained {
		// Python 会读取 vectors.index；Go 版本没有索引文件，直接从成对文件重建
		if err := s.rebuildLoadedIndexLocked(); err != nil {
			return err
		}
	}
	return nil
}

// rebuildLoadedIndexLocked 从成对文件重建内存索引，对齐 _rebuild_loaded_index_unlocked
func (s *VectorStore) rebuildLoadedIndexLocked() error {
	s.entries = map[int64][]float32{}
	s.entryOrder = nil
	if s.binCount > 0 {
		if err := s.forceTrainSmallDataLocked(); err != nil {
			return err
		}
	}
	matches, err := s.loadedIndexMatchesMetadataLocked()
	if err != nil {
		return err
	}
	if !matches {
		expected, expectedErr := s.expectedActiveIDMultisetLocked()
		if expectedErr != nil {
			return expectedErr
		}
		actual := s.indexedIDMultisetLocked()
		return fmt.Errorf(
			"向量索引按成对文件重建后仍与元数据不一致: missing=%d, unexpected=%d",
			multisetDifference(expected, actual),
			multisetDifference(actual, expected),
		)
	}
	return nil
}

// resetEmptyRuntimeLocked 清空运行时状态，对齐 _reset_empty_runtime_unlocked
func (s *VectorStore) resetEmptyRuntimeLocked() {
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
	s.totalAdded = 0
	s.totalDeleted = 0
	s.binCount = 0
}

// validateOrMigrateVectorMetadataLocked 校验 V2 元数据或升级 V1，对齐 _validate_or_migrate_vector_metadata_unlocked
func (s *VectorStore) validateOrMigrateVectorMetadataLocked(
	meta map[string]any,
	vectorBytes int64,
	idBytes int64,
	expectedEmbeddingFingerprint map[string]any,
	v1ValidHashes []string,
	v1EvidenceRoot string,
) (map[string]any, error) {
	rawSchema, hasSchema := meta["schema_version"]
	var schemaVersion int64 = 1
	var schemaValid bool = true
	if hasSchema {
		schemaVersion, schemaValid = jsonInteger(rawSchema)
	}
	if hasSchema && !schemaValid {
		return nil, fmt.Errorf("不支持的向量元数据版本: %s", pyFormatValue(rawSchema))
	}
	if schemaVersion == 1 {
		return s.migrateVectorMetadataV1Locked(
			meta,
			vectorBytes,
			idBytes,
			expectedEmbeddingFingerprint,
			v1ValidHashes,
			v1EvidenceRoot,
		)
	}
	if schemaVersion != 2 {
		return nil, fmt.Errorf("不支持的向量元数据版本: %s", pyFormatValue(rawSchema))
	}

	dimensionStatus := "mismatched"
	if rawDimension, ok := meta["dimension"]; ok {
		if parsed, valid := jsonInteger(rawDimension); valid && parsed == int64(s.dimension) {
			dimensionStatus = "matched"
		}
	}
	fingerprintStatus := vectorFingerprintStatus(asJSONObject(meta["embedding_fingerprint"]), expectedEmbeddingFingerprint)
	if dimensionStatus != "matched" {
		return nil, NewVectorStoreIntegrityError(
			fmt.Sprintf("V2 向量维度与当前实例不一致: %s != %d", pyFormatValue(meta["dimension"]), s.dimension),
			VectorStoreIntegrityOptions{
				ErrorCode:         "v2_dimension_mismatch",
				PairAligned:       boolPointer(true),
				DimensionStatus:   dimensionStatus,
				FingerprintStatus: fingerprintStatus,
				Details: map[string]any{
					"stored_dimension":   meta["dimension"],
					"expected_dimension": s.dimension,
				},
			},
		)
	}
	if expectedEmbeddingFingerprint != nil && fingerprintStatus != "matched" {
		errorCode := "v2_fingerprint_mismatch"
		if fingerprintStatus == "missing" {
			errorCode = "v2_fingerprint_missing"
		}
		return nil, NewVectorStoreIntegrityError("V2 向量 embedding 指纹无法可信验证", VectorStoreIntegrityOptions{
			ErrorCode:         errorCode,
			PairAligned:       boolPointer(true),
			DimensionStatus:   dimensionStatus,
			FingerprintStatus: fingerprintStatus,
		})
	}

	binaryCommit := asJSONObject(meta["binary_commit"])
	if binaryCommit == nil {
		return nil, NewVectorStoreIntegrityError("V2 向量元数据缺少 binary_commit", VectorStoreIntegrityOptions{
			ErrorCode:         "v2_commit_invalid",
			PairAligned:       boolPointer(true),
			DimensionStatus:   dimensionStatus,
			FingerprintStatus: fingerprintStatus,
		})
	}
	if rawTransaction, ok := binaryCommit["transaction_id"]; ok && rawTransaction != nil {
		transactionID, isString := rawTransaction.(string)
		if !isString || transactionID == "" {
			return nil, NewVectorStoreIntegrityError("V2 向量元数据的 transaction_id 无效", VectorStoreIntegrityOptions{
				ErrorCode:         "v2_commit_invalid",
				PairAligned:       boolPointer(true),
				DimensionStatus:   dimensionStatus,
				FingerprintStatus: fingerprintStatus,
			})
		}
	}
	committedVectorBytes, vectorOK := jsonInteger(binaryCommit["vector_bytes"])
	committedIDBytes, idOK := jsonInteger(binaryCommit["id_bytes"])
	if !vectorOK || !idOK || committedVectorBytes != vectorBytes || committedIDBytes != idBytes {
		return nil, NewVectorStoreIntegrityError(
			fmt.Sprintf(
				"V2 向量元数据提交长度与成对文件不一致: metadata=(%s, %s), pair=(%d, %d)",
				pyFormatValue(binaryCommit["vector_bytes"]),
				pyFormatValue(binaryCommit["id_bytes"]),
				vectorBytes,
				idBytes,
			),
			VectorStoreIntegrityOptions{
				ErrorCode:         "v2_commit_mismatch",
				PairAligned:       boolPointer(true),
				DimensionStatus:   dimensionStatus,
				FingerprintStatus: fingerprintStatus,
				Details: map[string]any{
					"committed_vector_bytes": binaryCommit["vector_bytes"],
					"committed_id_bytes":     binaryCommit["id_bytes"],
					"vector_bytes":           vectorBytes,
					"id_bytes":               idBytes,
				},
			},
		)
	}
	return meta, nil
}

// diskIDMultisetLocked 统计磁盘 ID 多重集，对齐 _disk_id_multiset_unlocked
func (s *VectorStore) diskIDMultisetLocked() (map[int64]int, error) {
	rawIDs, err := readVectorIDFile(s.idsBinPath())
	if err != nil {
		return nil, err
	}
	counts := make(map[int64]int, len(rawIDs))
	for _, intID := range rawIDs {
		counts[intID]++
	}
	return counts, nil
}

// metadataIDSet 把元数据里的 ID 集合转换为字符串切片去重
func metadataKnownHashes(meta map[string]any) ([]string, error) {
	if meta["known_hashes"] == nil && meta["ids"] == nil {
		return []string{}, nil
	}
	return jsonStringSlice(meta["known_hashes"], meta["ids"])
}

// trimsToEmpty 判断字符串是否为空或只含空白
func trimsToEmpty(value string) bool {
	return strings.TrimFunc(value, pyIsSpace) == ""
}
