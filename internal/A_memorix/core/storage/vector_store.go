package storage

// A_memorix 向量存储核心类型，对齐 Python core/storage/vector_store.py。
//
// 磁盘格式与 Python 的差异：Go 新写入的向量按 float32 落盘并在元数据记
// vector_dtype=float32，缺少该字段的历史元数据按 Python 的 float16 读取，因此
// 可直接接管 Python 目录；不生成 FAISS 的 vectors.index，检索用精确内积暴力扫描。
// vectors_ids.bin（大端 int64）与日志、元数据格式与 Python 一致。
//
// 所有向量写入前做 L2 归一化，索引度量固定为内积，等价余弦相似度。

import (
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	memorixutils "maibot-go/internal/A_memorix/core/utils"
)

const (
	// DefaultVectorMinTrain 默认训练触发阈值
	DefaultVectorMinTrain = 40
	// VectorTrainSize 强制训练样本量
	VectorTrainSize = 10000
	// VectorReservoirCapacity 储水池采样上限
	VectorReservoirCapacity = 10000
	// VectorReservoirSampleScope 只对流式处理前 50k 数据做储水池采样
	VectorReservoirSampleScope = 50000
)

// 磁盘向量元素编码，写在 vectors_metadata.json 的 vector_dtype 字段
const (
	VectorDtypeFloat32 = "float32"
	VectorDtypeFloat16 = "float16"
)

// 向量索引度量，与 Python faiss.METRIC_INNER_PRODUCT 对应
const VectorMetricInnerProduct = "inner_product"

// GenerateVectorID 生成稳定的 int64 向量 ID，对齐 VectorStore._generate_id
// 取 sha1(key) 前 8 字节按大端解释，再与 0x7FFFFFFFFFFFFFFF 相与
func GenerateVectorID(key string) int64 {
	digest := sha1.Sum([]byte(key))
	value := binary.BigEndian.Uint64(digest[:8])
	return int64(value & 0x7FFFFFFFFFFFFFFF)
}

// VectorStoreIntegrityError 携带机器可读诊断信息的向量一致性异常
// 字段与 Python 属性一一对应，ToDict 的键与 Python to_dict 一致
type VectorStoreIntegrityError struct {
	Message           string
	ErrorCode         string
	MissingCount      int
	UnexpectedCount   int
	PairAligned       *bool
	DimensionStatus   string
	FingerprintStatus string
	Details           map[string]any
}

// VectorStoreIntegrityOptions 构造 VectorStoreIntegrityError 的可选参数
type VectorStoreIntegrityOptions struct {
	ErrorCode         string
	MissingCount      int
	UnexpectedCount   int
	PairAligned       *bool
	DimensionStatus   string
	FingerprintStatus string
	Details           map[string]any
}

// NewVectorStoreIntegrityError 构造一致性异常并补齐 Python 侧的默认值
func NewVectorStoreIntegrityError(message string, opts VectorStoreIntegrityOptions) *VectorStoreIntegrityError {
	errorCode := opts.ErrorCode
	if errorCode == "" {
		errorCode = "vector_integrity_unknown"
	}
	dimensionStatus := opts.DimensionStatus
	if dimensionStatus == "" {
		dimensionStatus = "unknown"
	}
	fingerprintStatus := opts.FingerprintStatus
	if fingerprintStatus == "" {
		fingerprintStatus = "unknown"
	}
	details := make(map[string]any, len(opts.Details))
	for key, value := range opts.Details {
		details[key] = value
	}
	return &VectorStoreIntegrityError{
		Message:           message,
		ErrorCode:         errorCode,
		MissingCount:      max(0, opts.MissingCount),
		UnexpectedCount:   max(0, opts.UnexpectedCount),
		PairAligned:       opts.PairAligned,
		DimensionStatus:   dimensionStatus,
		FingerprintStatus: fingerprintStatus,
		Details:           details,
	}
}

// Error 实现 error 接口，等价 Python 的 str(exception)
func (e *VectorStoreIntegrityError) Error() string {
	return e.Message
}

// ToDict 返回机器可读诊断，对齐 Python to_dict
func (e *VectorStoreIntegrityError) ToDict() map[string]any {
	details := make(map[string]any, len(e.Details))
	for key, value := range e.Details {
		details[key] = value
	}
	var pairAligned any
	if e.PairAligned != nil {
		pairAligned = *e.PairAligned
	}
	return map[string]any{
		"error_code":         e.ErrorCode,
		"reason":             e.Error(),
		"missing_count":      e.MissingCount,
		"unexpected_count":   e.UnexpectedCount,
		"pair_aligned":       pairAligned,
		"dimension_status":   e.DimensionStatus,
		"fingerprint_status": e.FingerprintStatus,
		"details":            details,
	}
}

// TrustedVectorViewReport 只读旧向量视图的可信性评估结果
type TrustedVectorViewReport struct {
	TrustedCount      int
	DeclaredCount     int
	DiskCount         int
	MissingCount      int
	UnexpectedCount   int
	Coverage          float64
	PairAligned       bool
	DimensionStatus   string
	FingerprintStatus string
}

// ToDict 返回评估结果字典，对齐 Python to_dict
func (r TrustedVectorViewReport) ToDict() map[string]any {
	return map[string]any{
		"trusted_count":      r.TrustedCount,
		"declared_count":     r.DeclaredCount,
		"disk_count":         r.DiskCount,
		"missing_count":      r.MissingCount,
		"unexpected_count":   r.UnexpectedCount,
		"coverage":           r.Coverage,
		"pair_aligned":       r.PairAligned,
		"dimension_status":   r.DimensionStatus,
		"fingerprint_status": r.FingerprintStatus,
	}
}

// vectorFingerprintStatus 判定已存 embedding 指纹与期望指纹的关系
// 对齐 ReadOnlyVectorStoreView._fingerprint_status
func vectorFingerprintStatus(stored map[string]any, expected map[string]any) string {
	storedHash := fingerprintHash(stored)
	if storedHash == "" {
		return "missing"
	}
	expectedHash := fingerprintHash(expected)
	if expectedHash == "" {
		return "unknown"
	}
	if storedHash == expectedHash {
		return "matched"
	}
	return "mismatched"
}

// fingerprintHash 取出指纹里的非空 hash 字段
func fingerprintHash(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	value, ok := payload["hash"]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimFunc(text, pyIsSpace)
}

// VectorNorm 返回向量归一化方式
func (s *VectorStore) VectorNorm() string { return s.vectorNorm }

// Metric 返回索引度量，L2 归一化后内积等价余弦
func (s *VectorStore) Metric() string { return VectorMetricInnerProduct }

// IsTrained 返回索引是否已训练
func (s *VectorStore) IsTrained() bool { return s.trained }

// QuantizationType 返回量化类型
func (s *VectorStore) QuantizationType() memorixutils.QuantizationType { return s.quantizationType }

// IndexType 返回索引类型
func (s *VectorStore) IndexType() string { return s.indexType }

// Dimension 返回向量维度
func (s *VectorStore) Dimension() int { return s.dimension }

// DataDir 返回绑定的数据目录，未绑定返回空串
func (s *VectorStore) DataDir() string { return s.dataDir }

// BufferSize 返回写缓冲区阈值
func (s *VectorStore) BufferSize() int { return s.bufferSize }

// MinTrainThreshold 返回训练触发阈值
func (s *VectorStore) MinTrainThreshold() int { return s.minTrainThreshold }

// SetMinTrainThreshold 设置训练触发阈值
func (s *VectorStore) SetMinTrainThreshold(value int) { s.minTrainThreshold = value }

// BinCount 返回成对文件中的行数（含 tombstone 行）
func (s *VectorStore) BinCount() int { return s.binCount }

// TotalAdded 返回累计写入条数
func (s *VectorStore) TotalAdded() int { return s.totalAdded }

// TotalDeleted 返回累计删除条数
func (s *VectorStore) TotalDeleted() int { return s.totalDeleted }

// KnownHashes 返回已持久化的成员哈希副本（无序）
func (s *VectorStore) KnownHashes() []string {
	s.lock.Lock()
	defer s.lock.Unlock()
	out := make([]string, 0, len(s.knownHashes))
	for hashValue := range s.knownHashes {
		out = append(out, hashValue)
	}
	sort.Strings(out)
	return out
}

// DeletedIDs 返回 tombstone int64 ID 副本（升序）
func (s *VectorStore) DeletedIDs() []int64 {
	s.lock.Lock()
	defer s.lock.Unlock()
	out := make([]int64, 0, len(s.deletedIDs))
	for intID := range s.deletedIDs {
		out = append(out, intID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// BinPath 返回向量二进制路径
func (s *VectorStore) BinPath() string { return s.binPath() }

// IDsBinPath 返回向量 ID 二进制路径
func (s *VectorStore) IDsBinPath() string { return s.idsBinPath() }

// MetadataPath 返回向量元数据路径
func (s *VectorStore) MetadataPath() string { return s.metadataPath() }

// AppendJournalPath 返回追加事务日志路径
func (s *VectorStore) AppendJournalPath() string { return s.appendJournalPath() }

// CompactionJournalPath 返回压缩事务日志路径
func (s *VectorStore) CompactionJournalPath() string { return s.compactionJournalPath() }

// pyIsSpace 对齐 Python str.isspace 的空白判定
func pyIsSpace(r rune) bool {
	if r >= 0x1c && r <= 0x1f {
		return true
	}
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x85, 0xA0:
		return true
	}
	return false
}

// boolPointer 构造布尔指针
func boolPointer(value bool) *bool { return &value }

// VectorStoreConfig 向量存储构造参数，对齐 VectorStore.__init__ 的关键字参数
type VectorStoreConfig struct {
	// Dimension 向量维度，必须为正
	Dimension int
	// QuantizationType 量化类型，留空等价 INT8
	QuantizationType memorixutils.QuantizationType
	// IndexType 索引类型，留空等价 sq8
	IndexType string
	// DataDir 绑定的数据目录，留空表示不落盘（save 会报错）
	DataDir string
	// UseMmap 对齐 Python 的 use_mmap，Go 版本按块读取，保留字段用于配置兼容
	UseMmap bool
	// BufferSize 写缓冲区阈值，<=0 时取 1024
	BufferSize int
}

// VectorStore 向量存储，对齐 Python VectorStore
// 内存索引用 map[int64][]float32 做精确内积，磁盘是 append-only 成对文件
type VectorStore struct {
	dimension         int
	quantizationType  memorixutils.QuantizationType
	indexType         string
	dataDir           string
	useMmap           bool
	bufferSize        int
	minTrainThreshold int

	trained    bool
	vectorNorm string

	knownHashes         map[string]struct{}
	deletedIDs          map[int64]struct{}
	knownHashesRevision int
	cachedMapRevision   int
	cachedMap           map[int64]string
	idOffsets           map[int64]int64
	idOffsetsCount      int

	reservoir             [][]float32
	seenCountForReservoir int

	writeBufferVecs [][]float32
	writeBufferIDs  []int64

	totalAdded   int
	totalDeleted int
	binCount     int

	cleanupCheckpoint         *cleanupCheckpointState
	cleanupCheckpointError    string
	cleanupCompactionDeferred bool
	checkpointLockHeld        bool

	entries     map[int64][]float32
	entryOrder  []int64
	vectorDtype string

	lock *reentrantMutex
}

// NewVectorStore 创建向量存储，对齐 VectorStore.__init__ 的校验与默认值
func NewVectorStore(cfg VectorStoreConfig) (*VectorStore, error) {
	if cfg.Dimension <= 0 {
		return nil, fmt.Errorf("向量维度必须为正整数: %d", cfg.Dimension)
	}
	quantizationType := cfg.QuantizationType
	if quantizationType == "" {
		quantizationType = memorixutils.QuantizationInt8
	}
	if quantizationType != memorixutils.QuantizationInt8 {
		return nil, fmt.Errorf("vNext 仅支持 quantization_type=int8(SQ8)。 请更新配置并执行 scripts/release_vnext_migrate.py migrate。")
	}
	indexType := strings.ToLower(strings.TrimSpace(cfg.IndexType))
	if indexType == "" {
		indexType = "sq8"
	}
	if indexType != "sq8" && indexType != "int8" {
		return nil, fmt.Errorf("vNext 仅支持 index_type=sq8。 请更新配置并执行 scripts/release_vnext_migrate.py migrate。")
	}
	bufferSize := cfg.BufferSize
	if bufferSize <= 0 {
		bufferSize = 1024
	}
	if cfg.DataDir != "" {
		if err := os.MkdirAll(cfg.DataDir, 0o777); err != nil {
			return nil, err
		}
	}
	return &VectorStore{
		dimension:         cfg.Dimension,
		quantizationType:  memorixutils.QuantizationInt8,
		indexType:         "sq8",
		dataDir:           cfg.DataDir,
		useMmap:           cfg.UseMmap,
		bufferSize:        bufferSize,
		minTrainThreshold: DefaultVectorMinTrain,
		vectorNorm:        "l2",
		knownHashes:       map[string]struct{}{},
		deletedIDs:        map[int64]struct{}{},
		cachedMap:         map[int64]string{},
		cachedMapRevision: -1,
		idOffsets:         map[int64]int64{},
		idOffsetsCount:    -1,
		entries:           map[int64][]float32{},
		vectorDtype:       VectorDtypeFloat32,
		lock:              vectorStoreLock(),
	}, nil
}

// binPath 返回向量二进制路径
func (s *VectorStore) binPath() string { return filepath.Join(s.dataDir, "vectors.bin") }

// idsBinPath 返回向量 ID 二进制路径
func (s *VectorStore) idsBinPath() string { return filepath.Join(s.dataDir, "vectors_ids.bin") }

// appendJournalPath 返回追加事务日志路径
func (s *VectorStore) appendJournalPath() string {
	return filepath.Join(s.dataDir, "vectors_append.json")
}

// compactionJournalPath 返回压缩事务日志路径
func (s *VectorStore) compactionJournalPath() string {
	return filepath.Join(s.dataDir, "vectors_compaction.json")
}

// binBackupPath 返回压缩只读备份路径
func (s *VectorStore) binBackupPath() string {
	return filepath.Join(s.dataDir, "vectors.bin.compaction.bak")
}

// idsBackupPath 返回压缩 ID 只读备份路径
func (s *VectorStore) idsBackupPath() string {
	return filepath.Join(s.dataDir, "vectors_ids.bin.compaction.bak")
}

// binRecoveryTmpPath 返回压缩恢复临时文件路径
func (s *VectorStore) binRecoveryTmpPath() string {
	return filepath.Join(s.dataDir, "vectors.bin.compaction.restore.tmp")
}

// idsRecoveryTmpPath 返回压缩恢复 ID 临时文件路径
func (s *VectorStore) idsRecoveryTmpPath() string {
	return filepath.Join(s.dataDir, "vectors_ids.bin.compaction.restore.tmp")
}

// metadataPath 返回向量元数据路径
func (s *VectorStore) metadataPath() string { return filepath.Join(s.dataDir, "vectors_metadata.json") }

// indexPath 返回 Python 侧 FAISS 索引路径，Go 版本只用于清理残留
func (s *VectorStore) indexPath() string { return filepath.Join(s.dataDir, "vectors.index") }

// generateID 生成稳定的 int64 向量 ID
func (s *VectorStore) generateID(key string) int64 { return GenerateVectorID(key) }

// invalidateIDMap 让 int64 -> 字符串反查表失效
func (s *VectorStore) invalidateIDMap() { s.knownHashesRevision++ }

// invalidateIDOffsets 让磁盘行号映射失效
func (s *VectorStore) invalidateIDOffsets() {
	s.idOffsets = map[int64]int64{}
	s.idOffsetsCount = -1
}

// intToStrMapLocked 按需从已知哈希构建易失的反查表，调用方需持有锁
// 版本号可以识别等量替换，避免集合大小不变时命中旧缓存
func (s *VectorStore) intToStrMapLocked() map[int64]string {
	if s.cachedMapRevision != s.knownHashesRevision {
		cached := make(map[int64]string, len(s.knownHashes))
		for hashValue := range s.knownHashes {
			cached[GenerateVectorID(hashValue)] = hashValue
		}
		s.cachedMap = cached
		s.cachedMapRevision = s.knownHashesRevision
	}
	return s.cachedMap
}

// raiseIfCleanupCheckpointBrokenLocked 校验实例是否已因 checkpoint 回滚失败而锁死
func (s *VectorStore) raiseIfCleanupCheckpointBrokenLocked() error {
	if s.cleanupCheckpointError != "" {
		return fmt.Errorf(
			"向量清理 checkpoint 已损坏，当前实例禁止继续读写，必须重建 VectorStore 实例: %s",
			s.cleanupCheckpointError,
		)
	}
	return nil
}

// expectedActiveIDMultisetLocked 统计未 tombstone 的活动 ID 多重集
func (s *VectorStore) expectedActiveIDMultisetLocked() (map[int64]int, error) {
	owners := make(map[int64]string, len(s.knownHashes))
	counts := make(map[int64]int, len(s.knownHashes))
	for hashValue := range s.knownHashes {
		intID := GenerateVectorID(hashValue)
		if _, deleted := s.deletedIDs[intID]; deleted {
			continue
		}
		if owner, ok := owners[intID]; ok && owner != hashValue {
			return nil, fmt.Errorf("向量 int64 ID 冲突: %s / %s", owner, hashValue)
		}
		owners[intID] = hashValue
		counts[intID]++
	}
	return counts, nil
}

// indexedIDMultisetLocked 统计内存索引中的 ID 多重集
func (s *VectorStore) indexedIDMultisetLocked() map[int64]int {
	counts := make(map[int64]int, len(s.entries))
	for intID := range s.entries {
		counts[intID]++
	}
	return counts
}

// loadedIndexMatchesMetadataLocked 校验内存索引与元数据活动集合一致
func (s *VectorStore) loadedIndexMatchesMetadataLocked() (bool, error) {
	expected, err := s.expectedActiveIDMultisetLocked()
	if err != nil {
		return false, err
	}
	actual := s.indexedIDMultisetLocked()
	return multisetEqual(actual, expected), nil
}

// indexNtotal 返回主索引条目数，训练前主索引为空
func (s *VectorStore) indexNtotal() int {
	if s.trained {
		return len(s.entries)
	}
	return 0
}

// fallbackNtotal 返回回退索引条目数，训练后回退索引被释放
func (s *VectorStore) fallbackNtotal() int {
	if s.trained {
		return 0
	}
	return len(s.entries)
}
