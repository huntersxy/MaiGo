package storage

// A_memorix 向量成对文件（vectors.bin / vectors_ids.bin）的读写与事务恢复
// 对齐 Python 的 pair 校验、追加日志、压缩日志与尾部回滚
//
// 与 Python 的差异：Python 固定按 float16（每元素 2 字节）计算行宽，
// Go 版本按元数据里的 vector_dtype 选择 2 或 4 字节，其余事务协议完全一致

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

// statSize 返回文件长度与是否存在
func statSize(path string) (int64, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return info.Size(), true, nil
}

// fileSizeOrZero 返回文件长度，不存在视为 0
func fileSizeOrZero(path string) (int64, error) {
	size, _, err := statSize(path)
	return size, err
}

// storedVectorDtype 从元数据里解析向量编码，缺省按 Python 的 float16 处理
func storedVectorDtype(meta map[string]any) string {
	if meta == nil {
		return VectorDtypeFloat16
	}
	value, ok := meta["vector_dtype"].(string)
	if !ok || value == "" {
		return VectorDtypeFloat16
	}
	return strings.ToLower(value)
}

// vectorItemSizeLocked 返回当前实例的向量行宽
func (s *VectorStore) vectorItemSizeLocked() int64 {
	return int64(vectorItemSize(s.dimension, s.vectorDtype))
}

// vectorPairSizesLocked 读取成对文件长度，对齐 _vector_pair_sizes_unlocked
func (s *VectorStore) vectorPairSizesLocked() (int64, int64, error) {
	binSize, binExists, err := statSize(s.binPath())
	if err != nil {
		return 0, 0, err
	}
	idsSize, idsExists, err := statSize(s.idsBinPath())
	if err != nil {
		return 0, 0, err
	}
	if binExists != idsExists {
		return 0, 0, NewVectorStoreIntegrityError("向量数据文件与 ID 文件必须成对存在", VectorStoreIntegrityOptions{
			ErrorCode:   "vector_pair_missing",
			PairAligned: boolPointer(false),
		})
	}
	if !binExists {
		return 0, 0, nil
	}
	itemSize := s.vectorItemSizeLocked()
	if binSize%itemSize != 0 || idsSize%8 != 0 {
		return 0, 0, NewVectorStoreIntegrityError("向量数据文件或 ID 文件存在不完整记录", VectorStoreIntegrityOptions{
			ErrorCode:   "vector_pair_truncated",
			PairAligned: boolPointer(false),
			Details: map[string]any{
				"vector_bytes": binSize,
				"id_bytes":     idsSize,
			},
		})
	}
	if binSize/itemSize != idsSize/8 {
		return 0, 0, NewVectorStoreIntegrityError("向量数据文件与 ID 文件记录数不一致", VectorStoreIntegrityOptions{
			ErrorCode:       "vector_pair_count_mismatch",
			PairAligned:     boolPointer(false),
			DimensionStatus: "matched",
			Details: map[string]any{
				"vector_records": binSize / itemSize,
				"id_records":     idsSize / 8,
			},
		})
	}
	return binSize, idsSize, nil
}

// clearPairStateLocked 按已提交元数据维度校验待清空 pair，对齐 _clear_pair_state_unlocked
func (s *VectorStore) clearPairStateLocked() (int64, int64, int, error) {
	binSize, binExists, err := statSize(s.binPath())
	if err != nil {
		return 0, 0, 0, err
	}
	idsSize, idsExists, err := statSize(s.idsBinPath())
	if err != nil {
		return 0, 0, 0, err
	}
	if binExists != idsExists {
		return 0, 0, 0, fmt.Errorf("向量数据文件与 ID 文件必须成对存在")
	}
	if !binExists {
		return 0, 0, s.dimension, nil
	}
	if _, metaExists, metaErr := statSize(s.metadataPath()); metaErr != nil {
		return 0, 0, 0, metaErr
	} else if !metaExists {
		currentVectorBytes, currentIDBytes, pairErr := s.vectorPairSizesLocked()
		if pairErr != nil {
			return 0, 0, 0, pairErr
		}
		return currentVectorBytes, currentIDBytes, s.dimension, nil
	}

	meta, err := readJSONObject(s.metadataPath())
	if err != nil {
		return 0, 0, 0, err
	}
	storedDimension, ok := jsonInteger(meta["dimension"])
	if !ok || storedDimension <= 0 {
		return 0, 0, 0, fmt.Errorf("向量元数据的 dimension 无效，拒绝清空")
	}
	itemSize := int64(vectorItemSize(int(storedDimension), storedVectorDtype(meta)))
	if binSize%itemSize != 0 || idsSize%8 != 0 || binSize/itemSize != idsSize/8 {
		return 0, 0, 0, fmt.Errorf("待清空向量 pair 与已提交维度不一致")
	}
	schemaVersion, schemaOK := jsonInteger(meta["schema_version"])
	if !schemaOK {
		schemaVersion = 1
	}
	if schemaVersion == 2 {
		commit := asJSONObject(meta["binary_commit"])
		if commit == nil {
			return 0, 0, 0, fmt.Errorf("V2 向量元数据缺少 binary_commit，拒绝清空")
		}
		commitVectorBytes, _ := jsonInteger(commit["vector_bytes"])
		commitIDBytes, _ := jsonInteger(commit["id_bytes"])
		if commitVectorBytes != binSize || commitIDBytes != idsSize {
			return 0, 0, 0, fmt.Errorf("待清空向量 pair 与已提交长度不一致")
		}
	}
	return binSize, idsSize, int(storedDimension), nil
}

// pySortedStringList 近似 Python 的 sorted(list) 文本，用于对齐错误文案
func pySortedStringList(values []string) string {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	parts := make([]string, 0, len(sorted))
	for _, value := range sorted {
		parts = append(parts, "'"+value+"'")
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// syncAppend 以追加模式写入并 fsync，对齐 _sync_append
func syncAppend(path string, payload []byte) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	written, err := file.Write(payload)
	if err != nil {
		return err
	}
	if written != len(payload) {
		return fmt.Errorf("文件追加不完整: %s, %d/%d", path, written, len(payload))
	}
	return file.Sync()
}

// truncateAndSync 截断文件并 fsync，对齐 _truncate_and_sync
func truncateAndSync(path string, size int64) error {
	if _, exists, err := statSize(path); err != nil {
		return err
	} else if !exists {
		if size != 0 {
			return fmt.Errorf("待回滚文件不存在: %s", path)
		}
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o666)
		if createErr != nil {
			return createErr
		}
		_ = file.Close()
	}
	currentSize, err := fileSizeOrZero(path)
	if err != nil {
		return err
	}
	if currentSize < size {
		return fmt.Errorf("待回滚文件小于已提交长度: %s, %d < %d", path, currentSize, size)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err := file.Truncate(size); err != nil {
		return err
	}
	return file.Sync()
}

// rollbackAppendTailLocked 把成对文件回滚到指定长度，对齐 _rollback_append_tail_unlocked
func (s *VectorStore) rollbackAppendTailLocked(journal map[string]any, vectorBytes int64, idBytes int64) error {
	targetVectorBytes, _ := jsonInteger(journal["target_vector_bytes"])
	targetIDBytes, _ := jsonInteger(journal["target_id_bytes"])
	actualVectorBytes, err := fileSizeOrZero(s.binPath())
	if err != nil {
		return err
	}
	actualIDBytes, err := fileSizeOrZero(s.idsBinPath())
	if err != nil {
		return err
	}
	if actualVectorBytes > targetVectorBytes || actualIDBytes > targetIDBytes {
		return fmt.Errorf("向量追加文件超出日志目标长度，拒绝截断")
	}
	if actualVectorBytes < vectorBytes || actualIDBytes < idBytes {
		return fmt.Errorf("向量追加文件小于已提交基线，拒绝截断")
	}
	if err := truncateAndSync(s.binPath(), vectorBytes); err != nil {
		return err
	}
	if err := truncateAndSync(s.idsBinPath(), idBytes); err != nil {
		return err
	}
	s.invalidateIDOffsets()

	baseVectorBytes, _ := jsonInteger(journal["base_vector_bytes"])
	baseIDBytes, _ := jsonInteger(journal["base_id_bytes"])
	if vectorBytes == baseVectorBytes && idBytes == baseIDBytes {
		return removeFileIfExists(s.appendJournalPath())
	}
	journal["target_vector_bytes"] = vectorBytes
	journal["target_id_bytes"] = idBytes
	return writeJSONObject(s.appendJournalPath(), journal)
}

// removeFileIfExists 删除文件，不存在视为成功
func removeFileIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// recoverInterruptedAppendLocked 以元数据提交点恢复被中断的追加，对齐 _recover_interrupted_append_unlocked
func (s *VectorStore) recoverInterruptedAppendLocked() error {
	journal, err := s.readAppendJournalLocked()
	if err != nil {
		return err
	}
	if journal == nil {
		return nil
	}
	meta := map[string]any{}
	if _, exists, err := statSize(s.metadataPath()); err != nil {
		return err
	} else if exists {
		meta, err = readJSONObject(s.metadataPath())
		if err != nil {
			return err
		}
	}
	actualVectorBytes, err := fileSizeOrZero(s.binPath())
	if err != nil {
		return err
	}
	actualIDBytes, err := fileSizeOrZero(s.idsBinPath())
	if err != nil {
		return err
	}
	targetVectorBytes, _ := jsonInteger(journal["target_vector_bytes"])
	targetIDBytes, _ := jsonInteger(journal["target_id_bytes"])
	if metadataCommitsAppend(meta, journal) {
		if actualVectorBytes != targetVectorBytes || actualIDBytes != targetIDBytes {
			return fmt.Errorf("向量追加已提交，但成对文件未达到提交长度")
		}
		return removeFileIfExists(s.appendJournalPath())
	}
	baseVectorBytes, _ := jsonInteger(journal["base_vector_bytes"])
	baseIDBytes, _ := jsonInteger(journal["base_id_bytes"])
	if err := s.rollbackAppendTailLocked(journal, baseVectorBytes, baseIDBytes); err != nil {
		return err
	}
	// save() 可能已替换索引，而元数据尚未提交，删除它以强制 load() 重建
	return removeFileIfExists(s.indexPath())
}

// finalizeAppendCommitLocked 删除已提交的追加日志，对齐 _finalize_append_commit_unlocked
func (s *VectorStore) finalizeAppendCommitLocked() error {
	return removeFileIfExists(s.appendJournalPath())
}

// flushWriteBufferLocked 把写缓冲追加到成对文件，对齐 _flush_write_buffer_unlocked
func (s *VectorStore) flushWriteBufferLocked() error {
	if len(s.writeBufferIDs) == 0 {
		return nil
	}
	if len(s.writeBufferVecs) != len(s.writeBufferIDs) {
		return fmt.Errorf("向量写缓冲区长度不一致: %d vs %d", len(s.writeBufferVecs), len(s.writeBufferIDs))
	}
	// Python 在 data_dir 为 None 时无法解析落盘路径，这里显式失败避免写到当前目录
	if s.dataDir == "" {
		return fmt.Errorf("No data_dir")
	}

	vectors := append([][]float32(nil), s.writeBufferVecs...)
	ids := append([]int64(nil), s.writeBufferIDs...)
	vectorBytes := encodeVectorRows(vectors, s.vectorDtype)
	idsBytes := encodeInt64List(ids)

	journal, err := s.readAppendJournalLocked()
	if err != nil {
		return err
	}
	var currentVectorBytes, currentIDBytes int64
	if journal == nil {
		currentVectorBytes, currentIDBytes, err = s.vectorPairSizesLocked()
		if err != nil {
			return err
		}
		journal = map[string]any{
			"version":             int64(1),
			"transaction_id":      newTransactionID(),
			"base_vector_bytes":   currentVectorBytes,
			"base_id_bytes":       currentIDBytes,
			"target_vector_bytes": currentVectorBytes,
			"target_id_bytes":     currentIDBytes,
		}
	} else {
		currentVectorBytes, err = fileSizeOrZero(s.binPath())
		if err != nil {
			return err
		}
		currentIDBytes, err = fileSizeOrZero(s.idsBinPath())
		if err != nil {
			return err
		}
		targetVectorBytes, _ := jsonInteger(journal["target_vector_bytes"])
		targetIDBytes, _ := jsonInteger(journal["target_id_bytes"])
		if currentVectorBytes != targetVectorBytes || currentIDBytes != targetIDBytes {
			return fmt.Errorf("上一次向量追加未完成，请重启并执行 load() 恢复")
		}
		meta := map[string]any{}
		if _, exists, err := statSize(s.metadataPath()); err != nil {
			return err
		} else if exists {
			meta, err = readJSONObject(s.metadataPath())
			if err != nil {
				return err
			}
		}
		if metadataCommitsAppend(meta, journal) {
			// 元数据已提交上一笔追加，不能继续沿用旧基线
			if err := removeFileIfExists(s.appendJournalPath()); err != nil {
				return err
			}
			journal = map[string]any{
				"version":             int64(1),
				"transaction_id":      newTransactionID(),
				"base_vector_bytes":   currentVectorBytes,
				"base_id_bytes":       currentIDBytes,
				"target_vector_bytes": currentVectorBytes,
				"target_id_bytes":     currentIDBytes,
			}
		}
	}

	journal["target_vector_bytes"] = currentVectorBytes + int64(len(vectorBytes))
	journal["target_id_bytes"] = currentIDBytes + int64(len(idsBytes))
	if err := s.writeAppendJournalLocked(journal); err != nil {
		return err
	}

	if err := s.appendPairLocked(vectorBytes, idsBytes, vectors, ids, journal, currentVectorBytes, currentIDBytes); err != nil {
		return err
	}

	s.binCount += len(ids)
	s.invalidateIDOffsets()
	s.writeBufferVecs = nil
	s.writeBufferIDs = nil
	return nil
}

// appendPairLocked 追加成对文件并同步内存索引，失败时回滚到基线
func (s *VectorStore) appendPairLocked(vectorBytes []byte, idsBytes []byte, vectors [][]float32, ids []int64, journal map[string]any, baseVectorBytes int64, baseIDBytes int64) error {
	if err := syncAppend(s.binPath(), vectorBytes); err != nil {
		return err
	}
	if err := syncAppend(s.idsBinPath(), idsBytes); err != nil {
		// ids 追加失败时按日志目标长度回滚 bin 尾部
		_ = s.rollbackAppendTailLocked(journal, baseVectorBytes, baseIDBytes)
		return err
	}
	for index, intID := range ids {
		s.entries[intID] = append([]float32(nil), vectors[index]...)
		s.entryOrder = append(s.entryOrder, intID)
	}
	return nil
}

// copyAndSync 复制文件并 fsync，对齐 _copy_and_sync
func copyAndSync(source string, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	defer func() { _ = output.Close() }()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	return nil
}

// vectorPairMatches 校验成对文件长度与条数，对齐 _vector_pair_matches
func (s *VectorStore) vectorPairMatches(binPath string, idsPath string, expectedCount int64, dimension int) bool {
	binSize, binExists, err := statSize(binPath)
	if err != nil || !binExists {
		return false
	}
	idsSize, idsExists, err := statSize(idsPath)
	if err != nil || !idsExists {
		return false
	}
	itemSize := int64(vectorItemSize(dimension, s.vectorDtype))
	if binSize%itemSize != 0 || idsSize%8 != 0 {
		return false
	}
	vectorCount := binSize / itemSize
	idCount := idsSize / 8
	if vectorCount != idCount {
		return false
	}
	return expectedCount < 0 || vectorCount == expectedCount
}

// readVectorIDChunk 从 ID 文件读取一段 int64
func readVectorIDChunk(file *os.File, offset int64, count int) ([]int64, error) {
	buffer := make([]byte, count*8)
	read, err := file.ReadAt(buffer, offset)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if read != len(buffer) {
		return nil, fmt.Errorf("向量 ID 读取不完整: offset=%d, want=%d, got=%d", offset, len(buffer), read)
	}
	return decodeInt64List(buffer), nil
}

// decodeInt64List 解析大端 int64 列表
func decodeInt64List(raw []byte) []int64 {
	out := make([]int64, 0, len(raw)/8)
	for offset := 0; offset+8 <= len(raw); offset += 8 {
		out = append(out, int64(binary.BigEndian.Uint64(raw[offset:offset+8])))
	}
	return out
}

// fsyncDir 尽力同步目录项，平台不支持时忽略
func fsyncDir(path string) {
	directory, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		zap.L().Debug("同步目录失败", zap.String("path", path), zap.Error(err))
	}
}

// jsonNumber 构造 json.Number，便于写回整数值
func jsonNumber(value int64) json.Number {
	return json.Number(fmt.Sprintf("%d", value))
}

// newTransactionID 生成 32 位十六进制事务 ID，等价 uuid.uuid4().hex
func newTransactionID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		// 极端情况下退化为时间戳，保证事务 ID 仍然唯一可比较
		binary.BigEndian.PutUint64(buffer, uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint64(buffer[8:], uint64(os.Getpid()))
	}
	return hex.EncodeToString(buffer)
}
