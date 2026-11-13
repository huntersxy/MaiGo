package storage

// A_memorix Episode 来源段落读取与事实投影
// 对齐 Python metadata_episode.get_live_paragraphs_by_source。
// 依赖的 project_fact_paragraph / stale marks / relation status 在本文件以 episode*
// 私有辅助实现；公开端口见 metadata_fact_projection.go / metadata_feedback_marks.go。

import (
	"fmt"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// GetLiveParagraphsBySource 获取指定 source 下所有 live paragraphs
// 对齐 Python get_live_paragraphs_by_source；exclude_stale=True 时剔除引用了失效关系的段落
func (s *Store) GetLiveParagraphsBySource(source string, excludeStale bool, conn *Conn) ([]map[string]any, error) {
	token := normalizeEpisodeSource(source)
	if token == "" {
		return []map[string]any{}, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query(`SELECT *
		FROM paragraphs
		WHERE TRIM(COALESCE(source, '')) = ?
		  AND (is_deleted IS NULL OR is_deleted = 0)
		  AND COALESCE(json_extract(metadata, '$.dedup_phase'), 'complete') = 'complete'
		ORDER BY created_at ASC, hash ASC`, token)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}

	live := make([]map[string]any, 0, len(items))
	for _, item := range items {
		projected, kept, err := s.episodeProjectFactParagraph(c, s.rowToDict(item))
		if err != nil {
			return nil, err
		}
		if !kept {
			continue
		}
		live = append(live, projected)
	}
	if !excludeStale {
		return live, nil
	}

	paragraphHashes := make([]string, 0, len(live))
	for _, row := range live {
		hashValue := strings.TrimSpace(stringFromRow(row, "hash"))
		if hashValue != "" {
			paragraphHashes = append(paragraphHashes, hashValue)
		}
	}
	marksByParagraph, err := episodeStaleRelationMarksBatch(c, paragraphHashes)
	if err != nil {
		return nil, err
	}
	relationHashes := make([]string, 0, len(marksByParagraph))
	seenRelations := make(map[string]struct{}, len(marksByParagraph))
	for _, marks := range marksByParagraph {
		for _, relationHash := range marks {
			if relationHash == "" {
				continue
			}
			if _, ok := seenRelations[relationHash]; ok {
				continue
			}
			seenRelations[relationHash] = struct{}{}
			relationHashes = append(relationHashes, relationHash)
		}
	}
	statusMap, err := episodeRelationStatusBatch(c, relationHashes)
	if err != nil {
		return nil, err
	}

	filtered := make([]map[string]any, 0, len(live))
	for _, row := range live {
		paragraphHash := strings.TrimSpace(stringFromRow(row, "hash"))
		stale := false
		for _, relationHash := range marksByParagraph[paragraphHash] {
			if relationHash == "" {
				continue
			}
			inactive, ok := statusMap[relationHash]
			if !ok || inactive {
				stale = true
				break
			}
		}
		if stale {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered, nil
}

// episodeProjectFactParagraph 对齐 Python project_fact_paragraph：仅对去重管理的新段落投影有效事实
// 返回 (投影结果, 是否保留, error)；第二个返回值为 false 表示该段落应被丢弃
func (s *Store) episodeProjectFactParagraph(c *Conn, paragraph map[string]any) (map[string]any, bool, error) {
	if paragraph == nil {
		return nil, false, nil
	}
	metadata, _ := paragraph["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	if dedupPhase, ok := metadata["dedup_phase"].(string); ok && dedupPhase == "pending" {
		return nil, false, nil
	}
	if !episodeTruthy(metadata["dedup_managed"]) {
		return paragraph, true, nil
	}

	now := nowTimestamp()
	rows, err := c.Query(`SELECT c.claim_id, c.value_text
		FROM fact_claims c
		JOIN fact_evidence e ON e.claim_id = c.claim_id
		WHERE e.evidence_type = 'paragraph' AND e.evidence_id = ?
		  AND c.status = 'active'
		  AND (c.valid_from IS NULL OR c.valid_from <= ?)
		  AND (c.valid_to IS NULL OR c.valid_to > ?)`,
		stringFromRow(paragraph, "hash"), now, now)
	if err != nil {
		return nil, false, err
	}
	claimRows, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, false, err
	}
	active := make(map[string]struct{}, len(claimRows))
	claimIDs := make([]string, 0, len(claimRows))
	for _, row := range claimRows {
		active[episodeNormalizedToken(row["value_text"])] = struct{}{}
		claimIDs = append(claimIDs, episodePyStr(row["claim_id"]))
	}

	texts := episodeIterList(metadata["dedup_fact_texts"])
	if _, ok := metadata["dedup_fact_texts"]; !ok {
		texts = []any{paragraph["content"]}
	}
	kept := make([]string, 0, len(texts))
	for _, text := range texts {
		if _, ok := active[episodeNormalizedToken(text)]; ok {
			kept = append(kept, episodePyStr(text))
		}
	}
	if len(kept) == 0 {
		return nil, false, nil
	}
	projected := make(map[string]any, len(paragraph)+2)
	for key, value := range paragraph {
		projected[key] = value
	}
	projected["content"] = strings.Join(kept, "\n")
	projected["active_fact_claim_ids"] = claimIDs
	return projected, true, nil
}

// episodeNormalizedToken 对齐 Python _normalized_token：NFKC + 空白归一 + casefold
// strings.Fields 对 0x1c-0x1f 的处理弱于 Python str.split()，此差异仅影响极端控制字符
func episodeNormalizedToken(value any) string {
	text := norm.NFKC.String(episodeStrOrEmpty(value))
	return cases.Fold().String(strings.Join(strings.Fields(text), " "))
}

// episodeStaleRelationMarksBatch 对齐 Python get_paragraph_stale_relation_marks_batch
// 仅返回本文件需要的 relation_hash（Python 返回完整 mark 字典）
func episodeStaleRelationMarksBatch(c *Conn, paragraphHashes []string) (map[string][]string, error) {
	normalized := normalizeHashSequence(paragraphHashes)
	grouped := make(map[string][]string, len(normalized))
	if len(normalized) == 0 {
		return grouped, nil
	}
	for _, token := range normalized {
		grouped[token] = []string{}
	}
	query := fmt.Sprintf(`SELECT paragraph_hash, relation_hash
		FROM paragraph_stale_relation_marks
		WHERE paragraph_hash IN (%s)
		ORDER BY updated_at DESC, paragraph_hash ASC, relation_hash ASC`, sqlPlaceholders(len(normalized)))
	rows, err := c.Query(query, stringArgs(normalized)...)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	for _, row := range items {
		paragraphHash := strings.TrimSpace(stringFromRow(row, "paragraph_hash"))
		relationHash := strings.TrimSpace(stringFromRow(row, "relation_hash"))
		if paragraphHash == "" {
			continue
		}
		grouped[paragraphHash] = append(grouped[paragraphHash], relationHash)
	}
	return grouped, nil
}

// episodeRelationStatusBatch 对齐 Python get_relation_status_batch 的 is_inactive 读取部分
// 返回 relation hash -> 是否失效；缺失键表示关系不存在
func episodeRelationStatusBatch(c *Conn, relationHashes []string) (map[string]bool, error) {
	normalized := normalizeHashSequence(relationHashes)
	status := make(map[string]bool, len(normalized))
	if len(normalized) == 0 {
		return status, nil
	}
	query := fmt.Sprintf("SELECT hash, is_inactive FROM relations WHERE hash IN (%s)",
		sqlPlaceholders(len(normalized)))
	rows, err := c.Query(query, stringArgs(normalized)...)
	if err != nil {
		return nil, err
	}
	items, err := scanRowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	for _, row := range items {
		status[stringFromRow(row, "hash")] = episodeTruthy(row["is_inactive"])
	}
	return status, nil
}
