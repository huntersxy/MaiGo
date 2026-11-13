package storage

// A_memorix 向量追加与压缩事务日志的读取与校验
// 对齐 Python 的 append / compaction journal 协议

import (
	"fmt"
)

// journalIntField 读取日志的非负整数字段，失败时返回指定错误文案
func journalIntField(journal map[string]any, field string, message string) (int64, error) {
	value, ok := jsonInteger(journal[field])
	if !ok || value < 0 {
		return 0, fmt.Errorf("%s的 %s 无效", message, field)
	}
	return value, nil
}

// truthyJournalField 判断日志可选字段是否为真
func truthyJournalField(journal map[string]any, field string) bool {
	value, ok := journal[field].(string)
	return ok && value != ""
}

// readAppendJournalLocked 读取追加事务日志，对齐 _read_append_journal_unlocked
func (s *VectorStore) readAppendJournalLocked() (map[string]any, error) {
	if _, exists, err := statSize(s.appendJournalPath()); err != nil {
		return nil, err
	} else if !exists {
		return nil, nil
	}
	journal, err := readJSONObject(s.appendJournalPath())
	if err != nil {
		return nil, err
	}
	required := []string{
		"transaction_id",
		"base_vector_bytes",
		"base_id_bytes",
		"target_vector_bytes",
		"target_id_bytes",
	}
	missing := make([]string, 0, len(required))
	for _, field := range required {
		if _, ok := journal[field]; !ok {
			missing = append(missing, field)
		}
	}
	if len(missing) != 0 {
		return nil, fmt.Errorf("向量追加日志缺少字段: %s", pySortedStringList(missing))
	}
	transactionID, ok := journal["transaction_id"].(string)
	if !ok || transactionID == "" {
		return nil, fmt.Errorf("向量追加日志的 transaction_id 无效")
	}
	baseVectorBytes, err := journalIntField(journal, "base_vector_bytes", "向量追加日志")
	if err != nil {
		return nil, err
	}
	baseIDBytes, err := journalIntField(journal, "base_id_bytes", "向量追加日志")
	if err != nil {
		return nil, err
	}
	targetVectorBytes, err := journalIntField(journal, "target_vector_bytes", "向量追加日志")
	if err != nil {
		return nil, err
	}
	targetIDBytes, err := journalIntField(journal, "target_id_bytes", "向量追加日志")
	if err != nil {
		return nil, err
	}
	itemSize := s.vectorItemSizeLocked()
	if baseVectorBytes%itemSize != 0 ||
		targetVectorBytes%itemSize != 0 ||
		baseIDBytes%8 != 0 ||
		targetIDBytes%8 != 0 ||
		baseVectorBytes/itemSize != baseIDBytes/8 ||
		targetVectorBytes/itemSize != targetIDBytes/8 ||
		targetVectorBytes < baseVectorBytes ||
		targetIDBytes < baseIDBytes {
		return nil, fmt.Errorf("向量追加日志的成对文件长度无效")
	}
	return journal, nil
}

// readCompactionJournalLocked 读取压缩事务日志，对齐 _read_compaction_journal_unlocked
func (s *VectorStore) readCompactionJournalLocked() (map[string]any, error) {
	if _, exists, err := statSize(s.compactionJournalPath()); err != nil {
		return nil, err
	} else if !exists {
		return nil, nil
	}
	journal, err := readJSONObject(s.compactionJournalPath())
	if err != nil {
		return nil, err
	}
	required := []string{
		"transaction_id",
		"base_vector_bytes",
		"base_id_bytes",
		"target_vector_bytes",
		"target_id_bytes",
	}
	missing := make([]string, 0, len(required))
	for _, field := range required {
		if _, ok := journal[field]; !ok {
			missing = append(missing, field)
		}
	}
	if len(missing) != 0 {
		return nil, fmt.Errorf("向量压缩日志缺少字段: %s", pySortedStringList(missing))
	}
	transactionID, ok := journal["transaction_id"].(string)
	if !ok || transactionID == "" {
		return nil, fmt.Errorf("向量压缩日志的 transaction_id 无效")
	}
	baseVectorBytes, err := journalIntField(journal, "base_vector_bytes", "向量压缩日志")
	if err != nil {
		return nil, err
	}
	baseIDBytes, err := journalIntField(journal, "base_id_bytes", "向量压缩日志")
	if err != nil {
		return nil, err
	}
	targetVectorBytes, err := journalIntField(journal, "target_vector_bytes", "向量压缩日志")
	if err != nil {
		return nil, err
	}
	targetIDBytes, err := journalIntField(journal, "target_id_bytes", "向量压缩日志")
	if err != nil {
		return nil, err
	}
	journalDimension := int64(s.dimension)
	if raw, ok := journal["dimension"]; ok {
		parsed, ok := jsonInteger(raw)
		if !ok || parsed <= 0 {
			return nil, fmt.Errorf("向量压缩日志的 dimension 无效")
		}
		journalDimension = parsed
	}
	itemSize := int64(vectorItemSize(int(journalDimension), s.vectorDtype))
	if baseVectorBytes%itemSize != 0 ||
		targetVectorBytes%itemSize != 0 ||
		baseIDBytes%8 != 0 ||
		targetIDBytes%8 != 0 ||
		baseVectorBytes/itemSize != baseIDBytes/8 ||
		targetVectorBytes/itemSize != targetIDBytes/8 {
		return nil, fmt.Errorf("向量压缩日志的成对文件长度无效")
	}
	return journal, nil
}

// metadataCommitsAppend 判断元数据是否已提交该事务，对齐 _metadata_commits_append
func metadataCommitsAppend(meta map[string]any, journal map[string]any) bool {
	commit := asJSONObject(meta["binary_commit"])
	if commit == nil {
		return false
	}
	commitTransaction, _ := commit["transaction_id"].(string)
	journalTransaction, _ := journal["transaction_id"].(string)
	if commitTransaction != journalTransaction {
		return false
	}
	commitVectorBytes, _ := jsonInteger(commit["vector_bytes"])
	commitIDBytes, _ := jsonInteger(commit["id_bytes"])
	targetVectorBytes, _ := jsonInteger(journal["target_vector_bytes"])
	targetIDBytes, _ := jsonInteger(journal["target_id_bytes"])
	return commitVectorBytes == targetVectorBytes && commitIDBytes == targetIDBytes
}

// writeAppendJournalLocked 写入追加事务日志
func (s *VectorStore) writeAppendJournalLocked(journal map[string]any) error {
	return writeJSONObject(s.appendJournalPath(), journal)
}
