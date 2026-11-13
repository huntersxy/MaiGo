package storage

// A_memorix 摘要检查点，对齐 Python metadata_store.py 的 chat_summary_checkpoints 相关方法

import (
	"errors"
	"time"
)

var (
	// errSummaryBoundToOtherChat 对齐 Python ValueError("摘要标识已绑定到其他聊天流")
	errSummaryBoundToOtherChat = errors.New("摘要标识已绑定到其他聊天流")
	// errInvalidSummaryPhase 对齐 Python ValueError("无效的摘要写入阶段")
	errInvalidSummaryPhase = errors.New("无效的摘要写入阶段")
)

// GetSummaryCheckpoint 读取已完成摘要窗口的幂等记录
func (s *Store) GetSummaryCheckpoint(externalID string) (map[string]any, error) {
	rows, err := s.Query("SELECT * FROM chat_summary_checkpoints WHERE external_id=?", externalID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

// RecordSummaryCheckpoint 原子记录摘要窗口完成
func (s *Store) RecordSummaryCheckpoint(externalID string, chatID string, triggerMessageCount int, phase string, result map[string]any) error {
	return s.Transaction(true, func(tx *Tx) error {
		rows, err := tx.Query("SELECT chat_id FROM chat_summary_checkpoints WHERE external_id=?", externalID)
		if err != nil {
			return err
		}
		existing, err := scanRowsToMaps(rows)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			if stringFromRow(existing[0], "chat_id") != chatID {
				return errSummaryBoundToOtherChat
			}
		}
		if phase != "pending" && phase != "complete" {
			return errInvalidSummaryPhase
		}
		if triggerMessageCount < 0 {
			triggerMessageCount = 0
		}
		var resultJSON any
		if result != nil {
			resultJSON = jsonDumps(result)
		}
		now := float64(time.Now().UnixNano()) / 1e9
		const statement = "INSERT INTO chat_summary_checkpoints " +
			"(external_id, chat_id, trigger_message_count, created_at, phase, result_json) VALUES (?, ?, ?, ?, ?, ?) " +
			"ON CONFLICT(external_id) DO UPDATE SET phase=excluded.phase, " +
			"result_json=COALESCE(excluded.result_json, chat_summary_checkpoints.result_json), " +
			"trigger_message_count=excluded.trigger_message_count"
		_, err = tx.Exec(statement, externalID, chatID, triggerMessageCount, now, phase, resultJSON)
		return err
	})
}

// GetSummaryCheckpointCount 返回该聊天流已完成摘要窗口的最新触发计数
func (s *Store) GetSummaryCheckpointCount(chatID string) (int, error) {
	const query = "SELECT COALESCE(MAX(trigger_message_count), 0) AS count FROM chat_summary_checkpoints " +
		"WHERE chat_id=? AND phase='complete'"
	rows, err := s.Query(query, chatID)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return intFromRow(rows[0], "count"), nil
}

// GetChatSummaryHistory 按聊天流读取摘要段落，对齐 Python get_chat_summary_history
func (s *Store) GetChatSummaryHistory(chatID string) ([]map[string]any, error) {
	const query = `SELECT p.*, MAX(COALESCE(r.created_at, p.created_at)) AS summary_observed_at
		FROM paragraphs p
		LEFT JOIN external_memory_refs r
		  ON r.paragraph_hash = p.hash AND r.source_type = 'chat_summary'
		  AND json_extract(r.metadata_json, '$.chat_id') = ?
		WHERE (p.source = ? OR r.external_id IS NOT NULL)
		  AND (p.is_deleted IS NULL OR p.is_deleted = 0)
		  AND COALESCE(json_extract(p.metadata, '$.dedup_phase'), 'complete') = 'complete'
		GROUP BY p.hash
		ORDER BY summary_observed_at ASC, p.hash ASC`
	rows, err := s.Query(query, chatID, "chat_summary:"+chatID)
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		projected, err := s.ProjectFactParagraph(row)
		if err != nil {
			return nil, err
		}
		if projected == nil {
			continue
		}
		result = append(result, projected)
	}
	return result, nil
}
