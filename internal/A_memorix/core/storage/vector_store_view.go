package storage

// A_memorix 只读向量视图，对齐 Python core/storage/vector_store.py 的 ReadOnlyVectorStoreView
// 只允许读取旧成对文件的可信交集，所有修改入口都返回只读错误
// 旧 pair 固定按 Python 的 float16 编码解析

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// ReadOnlyVectorStoreView 从已验证的旧成对文件加载可信交集，不允许任何持久化变更
type ReadOnlyVectorStoreView struct {
	DataDir   string
	Dimension int
	Report    TrustedVectorViewReport

	knownHashes map[int64]string
	rowOffsets  map[int64]int64
	entries     map[int64][]float32
	entryOrder  []int64
	vectorBytes int64
	lock        sync.RWMutex
}

// errReadOnlyVectorView 只读视图的修改错误
func errReadOnlyVectorView() error {
	return fmt.Errorf("可信旧向量视图为只读，禁止修改或持久化")
}

// jsonIntegralNumber 判断 JSON 数值是否等于整数，对齐 Python 的 int/float 等价比较并排除 bool
func jsonIntegralNumber(value any) (int64, bool) {
	if parsed, ok := jsonInteger(value); ok {
		return parsed, true
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	asFloat, err := number.Float64()
	if err != nil || math.IsNaN(asFloat) || math.IsInf(asFloat, 0) || asFloat != math.Trunc(asFloat) {
		return 0, false
	}
	if asFloat < math.MinInt64 || asFloat > math.MaxInt64 {
		return 0, false
	}
	return int64(asFloat), true
}

// OpenTrustedV1 打开旧成对文件的只读可信视图，对齐 open_trusted_v1
// 旧 pair 固定按 Python 的 float16 编码解析
func OpenTrustedV1(dataDir string, dimension int, expectedEmbeddingFingerprint map[string]any) (*ReadOnlyVectorStoreView, error) {
	root := dataDir
	metaPath := filepath.Join(root, "vectors_metadata.json")
	binPath := filepath.Join(root, "vectors.bin")
	idsPath := filepath.Join(root, "vectors_ids.bin")
	for _, path := range []string{metaPath, binPath, idsPath} {
		if _, err := os.Stat(path); err != nil {
			return nil, NewVectorStoreIntegrityError("旧向量只读视图缺少元数据或成对文件", VectorStoreIntegrityOptions{
				ErrorCode:   "legacy_view_files_missing",
				PairAligned: boolPointer(false),
			})
		}
	}

	meta, err := readJSONObject(metaPath)
	if err != nil {
		return nil, err
	}
	if rawSchema, exists := meta["schema_version"]; exists {
		if parsed, ok := jsonIntegralNumber(rawSchema); !ok || parsed != 1 {
			return nil, NewVectorStoreIntegrityError(
				fmt.Sprintf("旧向量只读视图仅支持 V1 元数据，当前版本为 %s", pyFormatValue(rawSchema)),
				VectorStoreIntegrityOptions{ErrorCode: "legacy_view_schema_unsupported"},
			)
		}
	}

	storedDimension := int64(dimension)
	dimensionOK := true
	if rawDimension, exists := meta["dimension"]; exists {
		parsed, ok := jsonIntegralNumber(rawDimension)
		if ok {
			storedDimension = parsed
		} else {
			dimensionOK = false
		}
	}
	dimensionStatus := "mismatched"
	if dimensionOK && storedDimension == int64(dimension) {
		dimensionStatus = "matched"
	}
	fingerprintStatus := vectorFingerprintStatus(asJSONObject(meta["embedding_fingerprint"]), expectedEmbeddingFingerprint)
	if dimensionStatus != "matched" || fingerprintStatus != "matched" {
		return nil, NewVectorStoreIntegrityError("旧向量的维度或 embedding 指纹无法可信验证", VectorStoreIntegrityOptions{
			ErrorCode:         "legacy_view_incompatible",
			DimensionStatus:   dimensionStatus,
			FingerprintStatus: fingerprintStatus,
		})
	}

	vectorItemSize := int64(dimension) * 2
	binInfo, err := os.Stat(binPath)
	if err != nil {
		return nil, err
	}
	idsInfo, err := os.Stat(idsPath)
	if err != nil {
		return nil, err
	}
	vectorBytes := binInfo.Size()
	idBytes := idsInfo.Size()
	pairAligned := vectorBytes%vectorItemSize == 0 &&
		idBytes%8 == 0 &&
		vectorBytes/vectorItemSize == idBytes/8
	if !pairAligned {
		return nil, NewVectorStoreIntegrityError("旧向量数据文件与 ID 文件未对齐", VectorStoreIntegrityOptions{
			ErrorCode:         "legacy_view_pair_misaligned",
			PairAligned:       boolPointer(false),
			DimensionStatus:   dimensionStatus,
			FingerprintStatus: fingerprintStatus,
		})
	}

	rawHashes, err := jsonStringSlice(meta["known_hashes"], meta["ids"])
	if err != nil {
		return nil, NewVectorStoreIntegrityError("旧向量元数据的 ID 声明无效", VectorStoreIntegrityOptions{
			ErrorCode:         "legacy_view_metadata_invalid",
			PairAligned:       boolPointer(true),
			DimensionStatus:   dimensionStatus,
			FingerprintStatus: fingerprintStatus,
		})
	}
	rawDeletedIDs, err := jsonInt64Slice(meta["deleted_ids"])
	if err != nil {
		return nil, NewVectorStoreIntegrityError("旧向量元数据的 deleted_ids 无效", VectorStoreIntegrityOptions{
			ErrorCode:         "legacy_view_metadata_invalid",
			PairAligned:       boolPointer(true),
			DimensionStatus:   dimensionStatus,
			FingerprintStatus: fingerprintStatus,
		})
	}
	deletedIDs := make(map[int64]struct{}, len(rawDeletedIDs))
	for _, intID := range rawDeletedIDs {
		deletedIDs[intID] = struct{}{}
	}

	declaredOwners := make(map[int64]string, len(rawHashes))
	ambiguousDeclaredIDs := make(map[int64]struct{})
	for _, hashValue := range rawHashes {
		intID := GenerateVectorID(hashValue)
		if existing, ok := declaredOwners[intID]; ok && existing != hashValue {
			ambiguousDeclaredIDs[intID] = struct{}{}
		}
		declaredOwners[intID] = hashValue
	}

	rawDiskIDs, err := readVectorIDFile(idsPath)
	if err != nil {
		return nil, err
	}
	diskCounts := make(map[int64]int, len(rawDiskIDs))
	for _, intID := range rawDiskIDs {
		diskCounts[intID]++
	}
	rowOffsets := make(map[int64]int64, len(rawDiskIDs))
	for offset, intID := range rawDiskIDs {
		if diskCounts[intID] == 1 {
			rowOffsets[intID] = int64(offset)
		}
	}

	trustedOwners := make(map[int64]string, len(declaredOwners))
	for intID, hashValue := range declaredOwners {
		if _, ambiguous := ambiguousDeclaredIDs[intID]; ambiguous {
			continue
		}
		if _, deleted := deletedIDs[intID]; deleted {
			continue
		}
		if diskCounts[intID] != 1 {
			continue
		}
		trustedOwners[intID] = hashValue
	}
	trustedOffsets := make(map[int64]int64, len(trustedOwners))
	for intID := range trustedOwners {
		trustedOffsets[intID] = rowOffsets[intID]
	}

	expectedIDs := make(map[int64]int, len(rawHashes))
	for _, hashValue := range rawHashes {
		expectedIDs[GenerateVectorID(hashValue)]++
	}
	actualIDs := make(map[int64]int, len(rawDiskIDs))
	for _, intID := range rawDiskIDs {
		actualIDs[intID]++
	}
	missing := multisetDifference(expectedIDs, actualIDs)
	unexpected := multisetDifference(actualIDs, expectedIDs)
	declaredCount := len(rawHashes)
	trustedCount := len(trustedOwners)
	coverage := 0.0
	if declaredCount != 0 {
		coverage = float64(trustedCount) / float64(declaredCount)
	}
	report := TrustedVectorViewReport{
		TrustedCount:      trustedCount,
		DeclaredCount:     declaredCount,
		DiskCount:         len(rawDiskIDs),
		MissingCount:      missing,
		UnexpectedCount:   unexpected,
		Coverage:          coverage,
		PairAligned:       true,
		DimensionStatus:   dimensionStatus,
		FingerprintStatus: fingerprintStatus,
	}
	return NewReadOnlyVectorStoreView(root, dimension, trustedOwners, trustedOffsets, report)
}

// NewReadOnlyVectorStoreView 构造只读视图并从旧 pair 载入可信向量
func NewReadOnlyVectorStoreView(dataDir string, dimension int, knownHashes map[int64]string, rowOffsets map[int64]int64, report TrustedVectorViewReport) (*ReadOnlyVectorStoreView, error) {
	hashes := make(map[int64]string, len(knownHashes))
	for key, value := range knownHashes {
		hashes[key] = value
	}
	offsets := make(map[int64]int64, len(rowOffsets))
	for key, value := range rowOffsets {
		offsets[key] = value
	}
	view := &ReadOnlyVectorStoreView{
		DataDir:     dataDir,
		Dimension:   max(1, dimension),
		Report:      report,
		knownHashes: hashes,
		rowOffsets:  offsets,
		entries:     map[int64][]float32{},
		vectorBytes: int64(max(1, dimension)) * 2,
	}
	if err := view.loadIndex(); err != nil {
		return nil, err
	}
	return view, nil
}

// loadIndex 按行号读取可信向量并归一化，对齐 _load_index
func (v *ReadOnlyVectorStoreView) loadIndex() error {
	if len(v.rowOffsets) == 0 {
		return nil
	}
	file, err := os.Open(filepath.Join(v.DataDir, "vectors.bin"))
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	raw := make([]byte, v.vectorBytes)
	for intID, offset := range v.rowOffsets {
		if _, err := file.ReadAt(raw, offset*v.vectorBytes); err != nil {
			return NewVectorStoreIntegrityError(
				fmt.Sprintf("读取可信旧向量时记录不完整: int_id=%d", intID),
				VectorStoreIntegrityOptions{
					ErrorCode:         "legacy_view_record_truncated",
					PairAligned:       boolPointer(false),
					DimensionStatus:   "matched",
					FingerprintStatus: "matched",
				},
			)
		}
		vector := decodeFloat16Row(raw, v.Dimension)
		normalizeVector(vector)
		v.entries[intID] = vector
		v.entryOrder = append(v.entryOrder, intID)
	}
	return nil
}

// Search 在可信交集上检索，对齐 ReadOnlyVectorStoreView.search
func (v *ReadOnlyVectorStoreView) Search(query []float32, k int, filterDeleted bool, allowedIDs []string) ([]string, []float32, error) {
	_ = filterDeleted
	var allowed map[string]struct{}
	if allowedIDs != nil {
		allowed = make(map[string]struct{}, len(allowedIDs))
		for _, value := range allowedIDs {
			allowed[value] = struct{}{}
		}
		if len(allowed) == 0 {
			return []string{}, []float32{}, nil
		}
	}

	queryLocal := append([]float32(nil), query...)
	if len(queryLocal) != v.Dimension {
		return nil, nil, fmt.Errorf(
			"query embedding dimension mismatch: expected=%d got=%d",
			v.Dimension,
			len(queryLocal),
		)
	}
	for _, value := range queryLocal {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, nil, fmt.Errorf("query embedding contains non-finite values")
		}
	}

	v.lock.RLock()
	defer v.lock.RUnlock()
	if len(v.entries) == 0 {
		return []string{}, []float32{}, nil
	}
	normalizeVector(queryLocal)
	requestedK := max(1, k)
	type scored struct {
		id    int64
		score float32
	}
	candidates := make([]scored, 0, len(v.entryOrder))
	for _, intID := range v.entryOrder {
		vector, ok := v.entries[intID]
		if !ok {
			continue
		}
		hashValue, ok := v.knownHashes[intID]
		if !ok {
			continue
		}
		if allowed != nil {
			if _, ok := allowed[hashValue]; !ok {
				continue
			}
		}
		candidates = append(candidates, scored{id: intID, score: dotProduct(queryLocal, vector)})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	if len(candidates) > requestedK {
		candidates = candidates[:requestedK]
	}
	ids := make([]string, 0, len(candidates))
	scores := make([]float32, 0, len(candidates))
	for _, item := range candidates {
		ids = append(ids, v.knownHashes[item.id])
		scores = append(scores, item.score)
	}
	return ids, scores, nil
}

// IterVectorsByIDs 分批读取可信向量，对齐 iter_vectors_by_ids
func (v *ReadOnlyVectorStoreView) IterVectorsByIDs(ids []string, batchSize int, fn func(batch map[string][]float32) error) error {
	requested := make(map[int64]string, len(ids))
	order := make([]int64, 0, len(ids))
	for _, item := range ids {
		if item == "" {
			continue
		}
		intID := GenerateVectorID(item)
		if _, exists := requested[intID]; !exists {
			order = append(order, intID)
		}
		requested[intID] = item
	}
	if len(order) == 0 {
		return nil
	}
	safeBatchSize := batchSize
	if safeBatchSize <= 0 {
		safeBatchSize = 1024
	}

	file, err := os.Open(filepath.Join(v.DataDir, "vectors.bin"))
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	pending := map[string][]float32{}
	raw := make([]byte, v.vectorBytes)
	for _, intID := range order {
		hashValue := requested[intID]
		offset, ok := v.rowOffsets[intID]
		if !ok || v.knownHashes[intID] != hashValue {
			continue
		}
		if _, err := file.ReadAt(raw, offset*v.vectorBytes); err != nil {
			return NewVectorStoreIntegrityError(
				fmt.Sprintf("读取可信旧向量时记录不完整: int_id=%d", intID),
				VectorStoreIntegrityOptions{
					ErrorCode:   "legacy_view_record_truncated",
					PairAligned: boolPointer(false),
				},
			)
		}
		vector := decodeFloat16Row(raw, v.Dimension)
		norm := vectorNormValue(vector)
		if norm < 1e-12 {
			norm = 1e-12
		}
		for index := range vector {
			vector[index] = float32(float64(vector[index]) / norm)
		}
		pending[hashValue] = vector
		if len(pending) >= safeBatchSize {
			if err := fn(pending); err != nil {
				return err
			}
			pending = map[string][]float32{}
		}
	}
	if len(pending) != 0 {
		if err := fn(pending); err != nil {
			return err
		}
	}
	return nil
}

// GetVectors 读取可信向量，对齐 get_vectors
func (v *ReadOnlyVectorStoreView) GetVectors(ids []string) (map[string][]float32, error) {
	result := map[string][]float32{}
	err := v.IterVectorsByIDs(ids, 1024, func(batch map[string][]float32) error {
		for key, value := range batch {
			result[key] = value
		}
		return nil
	})
	return result, err
}

// HasData 判断视图是否载入任何可信向量
func (v *ReadOnlyVectorStoreView) HasData() bool {
	return len(v.knownHashes) > 0
}

// NumVectors 返回可信向量数量
func (v *ReadOnlyVectorStoreView) NumVectors() int {
	return len(v.knownHashes)
}

// Contains 判断哈希是否在可信交集中
func (v *ReadOnlyVectorStoreView) Contains(hashValue string) bool {
	intID := GenerateVectorID(hashValue)
	stored, ok := v.knownHashes[intID]
	return ok && stored == hashValue
}

// Add 只读视图禁止写入
func (v *ReadOnlyVectorStoreView) Add(vectors [][]float32, ids []string) (int, error) {
	return 0, errReadOnlyVectorView()
}

// Save 只读视图禁止持久化
func (v *ReadOnlyVectorStoreView) Save(dataDir string, embeddingFingerprint map[string]any) error {
	return errReadOnlyVectorView()
}

// Delete 只读视图禁止删除
func (v *ReadOnlyVectorStoreView) Delete(ids []string) (int, error) {
	return 0, errReadOnlyVectorView()
}

// Restore 只读视图禁止恢复
func (v *ReadOnlyVectorStoreView) Restore(ids []string) (int, error) {
	return 0, errReadOnlyVectorView()
}

// Clear 只读视图禁止清空
func (v *ReadOnlyVectorStoreView) Clear() error {
	return errReadOnlyVectorView()
}
