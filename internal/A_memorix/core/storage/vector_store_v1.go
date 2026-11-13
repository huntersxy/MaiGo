package storage

// A_memorix V1 向量元数据升级与一次性对账
// 对齐 Python 的 _migrate_vector_metadata_v1_unlocked 与
// _reconcile_vector_metadata_v1_unlocked
//
// 与 Python 的差异：Python 用 numpy.memmap 直接按 float16 读取 vectors.bin；
// Go 版本按元数据记录的 vector_dtype 解码，并在证据 npz 里统一写 float16
// （Python 侧读取该 npz 时行为不变）

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

// jsonStrictStringList 严格解析非空字符串列表，对齐 Python 的 list[str] 校验
func jsonStrictStringList(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok || text == "" {
			return nil, false
		}
		out = append(out, text)
	}
	return out, true
}

// fileSHA256 计算文件 SHA-256 十六进制摘要
func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// sortedStrings 返回排序副本，空输入返回空切片而不是 nil
func sortedStrings(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}

// migrateVectorMetadataV1Locked 严格升级 V1 元数据，或按活动资源做一次性对账
func (s *VectorStore) migrateVectorMetadataV1Locked(
	meta map[string]any,
	vectorBytes int64,
	idBytes int64,
	expectedEmbeddingFingerprint map[string]any,
	validHashes []string,
	evidenceRoot string,
) (map[string]any, error) {
	rawKnownValue, hasKnown := meta["known_hashes"]
	if !hasKnown {
		rawKnownValue, hasKnown = meta["ids"]
	}
	rawKnown := []string{}
	if hasKnown {
		parsed, ok := jsonStrictStringList(rawKnownValue)
		if !ok {
			return nil, NewVectorStoreIntegrityError(
				"V1 向量元数据的 known_hashes 无效，拒绝升级",
				VectorStoreIntegrityOptions{ErrorCode: "v1_metadata_invalid", PairAligned: boolPointer(true)},
			)
		}
		rawKnown = parsed
	}
	expectedIDs := make(map[int64]int, len(rawKnown))
	for _, hashValue := range rawKnown {
		expectedIDs[s.generateID(hashValue)]++
	}
	if validHashes == nil && len(expectedIDs) != len(rawKnown) {
		return nil, NewVectorStoreIntegrityError(
			"V1 向量元数据存在重复哈希或 int64 ID 冲突，拒绝升级",
			VectorStoreIntegrityOptions{ErrorCode: "v1_metadata_id_collision", PairAligned: boolPointer(true)},
		)
	}

	var metadataDimension any = s.dimension
	if raw, ok := meta["dimension"]; ok {
		metadataDimension = raw
	}
	dimensionStatus := "mismatched"
	if parsed, ok := jsonInteger(metadataDimension); ok && int(parsed) == s.dimension {
		dimensionStatus = "matched"
	}
	fingerprintStatus := vectorFingerprintStatus(
		asJSONObject(meta["embedding_fingerprint"]),
		expectedEmbeddingFingerprint,
	)
	if dimensionStatus != "matched" {
		return nil, NewVectorStoreIntegrityError(
			fmt.Sprintf("V1 向量维度与当前实例不一致: %s != %d", pyFormatValue(metadataDimension), s.dimension),
			VectorStoreIntegrityOptions{
				ErrorCode:         "v1_dimension_mismatch",
				PairAligned:       boolPointer(true),
				DimensionStatus:   dimensionStatus,
				FingerprintStatus: fingerprintStatus,
				Details: map[string]any{
					"stored_dimension":   metadataDimension,
					"expected_dimension": s.dimension,
				},
			},
		)
	}
	if expectedEmbeddingFingerprint != nil && fingerprintStatus != "matched" {
		errorCode := "v1_fingerprint_mismatch"
		if fingerprintStatus == "missing" {
			errorCode = "v1_fingerprint_missing"
		}
		return nil, NewVectorStoreIntegrityError("V1 向量 embedding 指纹无法可信验证", VectorStoreIntegrityOptions{
			ErrorCode:         errorCode,
			PairAligned:       boolPointer(true),
			DimensionStatus:   dimensionStatus,
			FingerprintStatus: fingerprintStatus,
		})
	}

	deletedIDs := map[int64]struct{}{}
	if raw, ok := meta["deleted_ids"]; ok {
		if raw == nil {
			return nil, v1TombstoneInvalidError(dimensionStatus, fingerprintStatus)
		}
		parsed, err := jsonInt64Slice(raw)
		if err != nil {
			return nil, v1TombstoneInvalidError(dimensionStatus, fingerprintStatus)
		}
		deletedIDs = int64SetFromSlice(parsed)
	}
	if validHashes == nil {
		orphaned := false
		for intID := range deletedIDs {
			if expectedIDs[intID] == 0 {
				orphaned = true
				break
			}
		}
		if orphaned {
			return nil, NewVectorStoreIntegrityError(
				"V1 向量元数据存在无对应向量的 tombstone，拒绝升级",
				VectorStoreIntegrityOptions{
					ErrorCode:         "v1_tombstone_orphaned",
					PairAligned:       boolPointer(true),
					DimensionStatus:   dimensionStatus,
					FingerprintStatus: fingerprintStatus,
				},
			)
		}
	}

	if validHashes != nil {
		if evidenceRoot == "" {
			return nil, fmt.Errorf("V1 对账必须提供只读证据目录")
		}
		return s.reconcileVectorMetadataV1Locked(
			rawKnown,
			deletedIDs,
			validHashes,
			evidenceRoot,
			vectorBytes,
			idBytes,
			expectedEmbeddingFingerprint,
		)
	}

	actualIDs, err := s.diskIDMultisetLocked()
	if err != nil {
		return nil, err
	}
	if !multisetEqual(expectedIDs, actualIDs) {
		missingCount := multisetDifference(expectedIDs, actualIDs)
		unexpectedCount := multisetDifference(actualIDs, expectedIDs)
		return nil, NewVectorStoreIntegrityError(
			fmt.Sprintf("V1 向量元数据与成对文件不一致，拒绝升级: missing=%d, unexpected=%d", missingCount, unexpectedCount),
			VectorStoreIntegrityOptions{
				ErrorCode:         "v1_id_set_mismatch",
				MissingCount:      missingCount,
				UnexpectedCount:   unexpectedCount,
				PairAligned:       boolPointer(true),
				DimensionStatus:   dimensionStatus,
				FingerprintStatus: fingerprintStatus,
				Details:           map[string]any{"data_dir": s.dataDir},
			},
		)
	}

	s.knownHashes = stringSetFromSlice(rawKnown)
	s.deletedIDs = cloneInt64Set(deletedIDs)
	s.invalidateIDMap()
	trained := false
	if rawTrained, ok := meta["is_trained"].(bool); ok {
		trained = rawTrained
	}
	s.trained = trained
	s.vectorNorm = "l2"
	if trained {
		// Python 会尝试直接读取 vectors.index；Go 没有索引文件，按成对文件重建
		if err := s.rebuildLoadedIndexLocked(); err != nil {
			return nil, err
		}
	}

	migrated := make(map[string]any, len(meta)+4)
	for key, value := range meta {
		migrated[key] = value
	}
	deletedList := make([]int64, 0, len(deletedIDs))
	for intID := range deletedIDs {
		deletedList = append(deletedList, intID)
	}
	sort.Slice(deletedList, func(i, j int) bool { return deletedList[i] < deletedList[j] })
	migrated["known_hashes"] = append([]string{}, rawKnown...)
	migrated["deleted_ids"] = deletedList
	migrated["dimension"] = s.dimension
	migrated["is_trained"] = s.trained
	migrated["vector_norm"] = "l2"
	migrated["schema_version"] = 2
	migrated["binary_commit"] = map[string]any{
		"transaction_id": "metadata-v1-migration-" + newTransactionID(),
		"vector_bytes":   vectorBytes,
		"id_bytes":       idBytes,
	}
	if err := writeJSONObject(s.metadataPath(), migrated); err != nil {
		return nil, err
	}
	return migrated, nil
}

// v1TombstoneInvalidError 构造 tombstone 非法异常
func v1TombstoneInvalidError(dimensionStatus string, fingerprintStatus string) error {
	return NewVectorStoreIntegrityError(
		"V1 向量元数据的 deleted_ids 无效，拒绝升级",
		VectorStoreIntegrityOptions{
			ErrorCode:         "v1_tombstone_invalid",
			PairAligned:       boolPointer(true),
			DimensionStatus:   dimensionStatus,
			FingerprintStatus: fingerprintStatus,
		},
	)
}

// excludedVectorRow 描述一条被排除的磁盘行
type excludedVectorRow struct {
	row      int
	intID    int64
	reasons  []string
	declared []string
	active   []string
}

// reconcileVectorMetadataV1Locked 按活动哈希重写成对文件并保留排除证据
// 对齐 _reconcile_vector_metadata_v1_unlocked
func (s *VectorStore) reconcileVectorMetadataV1Locked(
	rawKnown []string,
	deletedIDs map[int64]struct{},
	validHashes []string,
	evidenceRoot string,
	vectorBytes int64,
	idBytes int64,
	expectedEmbeddingFingerprint map[string]any,
) (map[string]any, error) {
	rawDiskIDs, err := readVectorIDFile(s.idsBinPath())
	if err != nil {
		return nil, err
	}
	diskCounts := make(map[int64]int, len(rawDiskIDs))
	for _, intID := range rawDiskIDs {
		diskCounts[intID]++
	}

	validSet := make(map[string]struct{}, len(validHashes))
	for _, hashValue := range validHashes {
		if hashValue != "" {
			validSet[hashValue] = struct{}{}
		}
	}
	sortedValid := make([]string, 0, len(validSet))
	for hashValue := range validSet {
		sortedValid = append(sortedValid, hashValue)
	}
	sort.Strings(sortedValid)
	validCandidates := map[int64][]string{}
	for _, hashValue := range sortedValid {
		intID := s.generateID(hashValue)
		validCandidates[intID] = append(validCandidates[intID], hashValue)
	}
	declaredCandidates := map[int64][]string{}
	for _, hashValue := range rawKnown {
		intID := s.generateID(hashValue)
		declaredCandidates[intID] = append(declaredCandidates[intID], hashValue)
	}

	acceptedRows := make([]int, 0, len(rawDiskIDs))
	acceptedHashes := make([]string, 0, len(rawDiskIDs))
	preserved := make([]string, 0, len(rawDiskIDs))
	recoveredTail := make([]string, 0)
	excluded := make([]excludedVectorRow, 0)
	for rowIndex, intID := range rawDiskIDs {
		validOwners := validCandidates[intID]
		declaredOwners := declaredCandidates[intID]
		reasons := make([]string, 0, 6)
		if diskCounts[intID] > 1 {
			reasons = append(reasons, "duplicate_disk_id")
		}
		if _, deleted := deletedIDs[intID]; deleted {
			reasons = append(reasons, "tombstoned")
		}
		if len(validOwners) > 1 {
			reasons = append(reasons, "active_id_collision")
		}
		if len(declaredOwners) > 1 {
			reasons = append(reasons, "metadata_id_collision")
		}
		if len(validOwners) == 0 {
			reasons = append(reasons, "unresolved_orphan")
		} else if len(declaredOwners) != 0 && declaredOwners[0] != validOwners[0] {
			reasons = append(reasons, "metadata_active_collision")
		}
		if len(reasons) != 0 {
			excluded = append(excluded, excludedVectorRow{
				row:      rowIndex,
				intID:    intID,
				reasons:  reasons,
				declared: append([]string{}, declaredOwners...),
				active:   append([]string{}, validOwners...),
			})
			continue
		}
		activeHash := validOwners[0]
		acceptedRows = append(acceptedRows, rowIndex)
		acceptedHashes = append(acceptedHashes, activeHash)
		if len(declaredOwners) != 0 {
			preserved = append(preserved, activeHash)
		} else {
			recoveredTail = append(recoveredTail, activeHash)
		}
	}

	acceptedHashSet := make(map[string]struct{}, len(acceptedHashes))
	for _, hashValue := range acceptedHashes {
		acceptedHashSet[hashValue] = struct{}{}
	}
	missing := make([]string, 0, len(sortedValid))
	for _, hashValue := range sortedValid {
		if _, ok := acceptedHashSet[hashValue]; !ok {
			missing = append(missing, hashValue)
		}
	}

	operationID := fmt.Sprintf("%d-%s", time.Now().UnixNano(), newTransactionID()[:12])
	evidenceDir := filepath.Join(evidenceRoot, operationID)
	if _, err := os.Stat(evidenceDir); err == nil {
		return nil, fmt.Errorf("证据目录已存在: %s", evidenceDir)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(evidenceDir, 0o777); err != nil {
		return nil, err
	}

	excludedVectorRows := make([]int64, 0, len(excluded))
	excludedIntIDs := make([]int64, 0, len(excluded))
	excludedValues := make([]float32, 0, len(excluded)*s.dimension)
	if len(excluded) != 0 {
		itemSize := s.vectorItemSizeLocked()
		file, err := os.Open(s.binPath())
		if err != nil {
			return nil, err
		}
		raw := make([]byte, itemSize)
		for _, item := range excluded {
			if _, err := file.ReadAt(raw, int64(item.row)*itemSize); err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("向量数据读取不完整: row=%d", item.row)
			}
			excludedVectorRows = append(excludedVectorRows, int64(item.row))
			excludedIntIDs = append(excludedIntIDs, item.intID)
			excludedValues = append(excludedValues, decodeVectorRow(raw, s.dimension, s.vectorDtype)...)
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	}

	evidencePath := filepath.Join(evidenceDir, "excluded_vectors.npz")
	evidenceTmpPath := evidencePath + ".tmp"
	if err := writeNpzFile(evidenceTmpPath, []npzMember{
		{Name: "row_indices", Descr: "<i8", Shape: []int{len(excludedVectorRows)}, Payload: npyInt64Payload(excludedVectorRows)},
		{Name: "int64_ids", Descr: "<i8", Shape: []int{len(excludedIntIDs)}, Payload: npyInt64Payload(excludedIntIDs)},
		{Name: "vectors", Descr: "<f2", Shape: []int{len(excluded), s.dimension}, Payload: npyFloat16Payload(excludedValues)},
	}); err != nil {
		return nil, err
	}
	if err := os.Rename(evidenceTmpPath, evidencePath); err != nil {
		return nil, err
	}

	excludedReports := make([]any, 0, len(excluded))
	for _, item := range excluded {
		excludedReports = append(excludedReports, map[string]any{
			"row":             item.row,
			"int64_id":        item.intID,
			"reasons":         item.reasons,
			"declared_hashes": item.declared,
			"active_hashes":   item.active,
		})
	}
	binChecksum, err := fileSHA256(s.binPath())
	if err != nil {
		return nil, err
	}
	idsChecksum, err := fileSHA256(s.idsBinPath())
	if err != nil {
		return nil, err
	}
	report := map[string]any{
		"version":      1,
		"operation_id": operationID,
		"status":       "prepared",
		"store_path":   s.dataDir,
		"dimension":    s.dimension,
		"dtype":        "float16",
		"source_checksums": map[string]any{
			"vectors.bin":     binChecksum,
			"vectors_ids.bin": idsChecksum,
		},
		"evidence_file":                     "excluded_vectors.npz",
		"preserved":                         sortedStrings(preserved),
		"recovered_tail":                    sortedStrings(recoveredTail),
		"missing_requires_explicit_rebuild": missing,
		"excluded":                          excludedReports,
		"counts": map[string]any{
			"source_rows":   len(rawDiskIDs),
			"accepted_rows": len(acceptedRows),
			"excluded_rows": len(excluded),
			"missing":       len(missing),
		},
	}
	reportPath := filepath.Join(evidenceDir, "report.json")
	if err := writeJSONObject(reportPath, report); err != nil {
		return nil, err
	}

	tmpBinPath := s.binPath() + ".tmp"
	tmpIDsPath := s.idsBinPath() + ".tmp"
	if err := removeFileIfExists(tmpBinPath); err != nil {
		return nil, err
	}
	if err := removeFileIfExists(tmpIDsPath); err != nil {
		return nil, err
	}
	acceptedSet := make(map[int]struct{}, len(acceptedRows))
	for _, row := range acceptedRows {
		acceptedSet[row] = struct{}{}
	}
	if _, err := s.writeAcceptedRowsLocked(tmpBinPath, tmpIDsPath, rawDiskIDs, acceptedSet); err != nil {
		return nil, err
	}
	acceptedCount := int64(len(acceptedHashes))
	if !s.vectorPairMatches(tmpBinPath, tmpIDsPath, acceptedCount, s.dimension) {
		return nil, fmt.Errorf("V1 对账临时 pair 校验失败")
	}
	if err := removeFileIfExists(s.binBackupPath()); err != nil {
		return nil, err
	}
	if err := removeFileIfExists(s.idsBackupPath()); err != nil {
		return nil, err
	}
	if err := copyAndSync(s.binPath(), s.binBackupPath()); err != nil {
		return nil, err
	}
	if err := copyAndSync(s.idsBinPath(), s.idsBackupPath()); err != nil {
		return nil, err
	}
	tmpBinSize, err := fileSizeOrZero(tmpBinPath)
	if err != nil {
		return nil, err
	}
	tmpIDsSize, err := fileSizeOrZero(tmpIDsPath)
	if err != nil {
		return nil, err
	}
	journal := map[string]any{
		"version":             int64(2),
		"purpose":             "v1_reconciliation",
		"transaction_id":      "v1-reconciliation-" + newTransactionID(),
		"dimension":           int64(s.dimension),
		"base_vector_bytes":   vectorBytes,
		"base_id_bytes":       idBytes,
		"target_vector_bytes": tmpBinSize,
		"target_id_bytes":     tmpIDsSize,
	}
	if err := writeJSONObject(s.compactionJournalPath(), journal); err != nil {
		return nil, err
	}

	switchErr := func() error {
		if err := s.replaceCompactionTmpPairLocked(tmpBinPath, tmpIDsPath); err != nil {
			return err
		}
		if !s.vectorPairMatches(s.binPath(), s.idsBinPath(), acceptedCount, s.dimension) {
			return fmt.Errorf("V1 对账 pair 切换后校验失败")
		}
		s.knownHashes = stringSetFromSlice(acceptedHashes)
		s.deletedIDs = map[int64]struct{}{}
		s.binCount = int(acceptedCount)
		s.invalidateIDMap()
		s.invalidateIDOffsets()
		s.entries = map[int64][]float32{}
		s.entryOrder = nil
		s.trained = false
		if acceptedCount > 0 {
			if err := s.forceTrainSmallDataLocked(); err != nil {
				return err
			}
		}
		if err := s.saveLocked("", expectedEmbeddingFingerprint); err != nil {
			return err
		}
		if !s.trained {
			if err := removeFileIfExists(s.indexPath()); err != nil {
				return err
			}
		}
		return nil
	}()
	if switchErr != nil {
		if _, exists, statErr := statSize(s.compactionJournalPath()); statErr == nil && exists {
			_ = s.recoverInterruptedCompactionLocked()
		}
		return nil, switchErr
	}

	report["status"] = "completed"
	report["completed_at"] = float64(time.Now().UnixNano()) / 1e9
	if err := writeJSONObject(reportPath, report); err != nil {
		zap.L().Error("V1 对账已提交，但更新证据报告失败", zap.Error(err))
	}
	return readJSONObject(s.metadataPath())
}

// trimEmptyStrings 去掉空白项并保持顺序，供对账参数清理使用
func trimEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}
