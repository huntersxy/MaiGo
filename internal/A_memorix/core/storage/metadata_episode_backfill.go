package storage

// A_memorix 段落向量回填任务队列
// 对齐 Python core/storage/metadata_episode.py 的 paragraph_vector_backfill 系列方法

import (
	"fmt"
	"strings"
)

// episodeBackfillChunkSize 对齐 Python mark_* 系列的分批大小
const episodeBackfillChunkSize = 500

// EnqueueParagraphVectorBackfill 登记段落向量回填任务，对齐 Python enqueue_paragraph_vector_backfill
func (s *Store) EnqueueParagraphVectorBackfill(paragraphHash string, createdAt *float64, errorText string, conn *Conn) error {
	token := strings.TrimSpace(paragraphHash)
	if token == "" {
		return nil
	}
	now := nowTimestamp()
	createdTs := now
	if createdAt != nil {
		createdTs = *createdAt
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return err
	}
	if _, err := c.Exec(`INSERT INTO paragraph_vector_backfill (
			paragraph_hash, status, retry_count, last_error, created_at, updated_at
		) VALUES (?, 'pending', 0, ?, ?, ?)
		ON CONFLICT(paragraph_hash) DO UPDATE SET
			status = CASE
				WHEN paragraph_vector_backfill.status = 'done' THEN 'done'
				ELSE 'pending'
			END,
			last_error = CASE
				WHEN paragraph_vector_backfill.status = 'done' THEN paragraph_vector_backfill.last_error
				ELSE excluded.last_error
			END,
			created_at = COALESCE(paragraph_vector_backfill.created_at, excluded.created_at),
			updated_at = excluded.updated_at`,
		token, optionalString(strings.TrimSpace(errorText)), createdTs, now,
	); err != nil {
		return err
	}
	return c.Commit()
}

// FetchParagraphVectorBackfillBatch 获取段落向量回填批次，对齐 Python fetch_paragraph_vector_backfill_batch
// limit 为 0 时取 64，maxRetry 为 0 时取 5
func (s *Store) FetchParagraphVectorBackfillBatch(limit int, maxRetry int, conn *Conn) ([]map[string]any, error) {
	safeLimit := limit
	if safeLimit == 0 {
		safeLimit = 64
	}
	if safeLimit < 1 {
		safeLimit = 1
	}
	safeRetry := maxRetry
	if safeRetry == 0 {
		safeRetry = 5
	}
	if safeRetry < 0 {
		safeRetry = 0
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(`SELECT paragraph_hash, status, retry_count, last_error, created_at, updated_at
		FROM paragraph_vector_backfill
		WHERE status = 'pending'
		   OR (status = 'failed' AND retry_count < ?)
		ORDER BY updated_at ASC
		LIMIT ?`, safeRetry, safeLimit)
	if err != nil {
		return nil, err
	}
	return scanRowsToMaps(rows)
}

// MarkParagraphVectorBackfillRunning 批量标记段落回填任务为 running
// 对齐 Python mark_paragraph_vector_backfill_running
func (s *Store) MarkParagraphVectorBackfillRunning(hashes []string, conn *Conn) error {
	return s.updateParagraphVectorBackfill(hashes, conn,
		"status = 'running', updated_at = ?",
		" AND status IN ('pending', 'failed')",
	)
}

// MarkParagraphVectorBackfillDone 批量标记段落回填任务为 done
// 对齐 Python mark_paragraph_vector_backfill_done
func (s *Store) MarkParagraphVectorBackfillDone(hashes []string, conn *Conn) error {
	return s.updateParagraphVectorBackfill(hashes, conn,
		"status = 'done', last_error = NULL, updated_at = ?",
		"",
	)
}

// updateParagraphVectorBackfill 按 500 条分批更新回填状态，对齐 Python 分批循环
func (s *Store) updateParagraphVectorBackfill(hashes []string, conn *Conn, setSQL string, extraCondition string) error {
	normalized := make([]string, 0, len(hashes))
	for _, item := range hashes {
		token := strings.TrimSpace(item)
		if token == "" {
			continue
		}
		normalized = append(normalized, token)
	}
	uniq := dedupeStrings(normalized)
	if len(uniq) == 0 {
		return nil
	}
	now := nowTimestamp()
	c, err := s.ResolveConn(conn)
	if err != nil {
		return err
	}
	for _, batch := range iterSQLBatches(uniq, episodeBackfillChunkSize) {
		args := make([]any, 0, len(batch)+1)
		args = append(args, now)
		args = append(args, stringArgs(batch)...)
		query := fmt.Sprintf(`UPDATE paragraph_vector_backfill
			SET %s
			WHERE paragraph_hash IN (%s)%s`, setSQL, sqlPlaceholders(len(batch)), extraCondition)
		if _, err := c.Exec(query, args...); err != nil {
			return err
		}
	}
	return c.Commit()
}

// MarkParagraphVectorBackfillFailed 标记单个段落回填任务失败并累加重试
// 对齐 Python mark_paragraph_vector_backfill_failed
func (s *Store) MarkParagraphVectorBackfillFailed(paragraphHash string, errorText string, conn *Conn) error {
	token := strings.TrimSpace(paragraphHash)
	if token == "" {
		return nil
	}
	now := nowTimestamp()
	c, err := s.ResolveConn(conn)
	if err != nil {
		return err
	}
	if _, err := c.Exec(`UPDATE paragraph_vector_backfill
		SET status = 'failed',
			retry_count = COALESCE(retry_count, 0) + 1,
			last_error = ?,
			updated_at = ?
		WHERE paragraph_hash = ?`, errorText, now, token); err != nil {
		return err
	}
	return c.Commit()
}

// GetParagraphVectorBackfillStatusCounts 统计段落回填任务状态
// 对齐 Python get_paragraph_vector_backfill_status_counts
func (s *Store) GetParagraphVectorBackfillStatusCounts(conn *Conn) (map[string]int, error) {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(`SELECT status, COUNT(*) AS count
		FROM paragraph_vector_backfill
		GROUP BY status`)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{"pending": 0, "running": 0, "failed": 0, "done": 0}
	for _, item := range items {
		status := strings.ToLower(strings.TrimSpace(stringFromRow(item, "status")))
		if _, ok := counts[status]; !ok {
			continue
		}
		counts[status] = episodeRowInt(item, "count")
	}
	return counts, nil
}
