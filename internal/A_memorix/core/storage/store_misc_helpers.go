package storage

// A_memorix 清理与关系状态移植共用的私有助手，并补齐 fact 证据解绑

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// miscCopyMap 复制行字典，避免调用方覆盖底层映射
func miscCopyMap(row map[string]any) map[string]any {
	if row == nil {
		return nil
	}
	out := make(map[string]any, len(row))
	for key, value := range row {
		out[key] = value
	}
	return out
}

// miscBoolValue 对齐 Python bool(value) 的常见标量语义
func miscBoolValue(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case int64:
		return v != 0
	case int:
		return v != 0
	case float64:
		return v != 0
	case []byte:
		return len(v) > 0
	case string:
		return v != ""
	default:
		return true
	}
}

// miscNullableFloat64 把可选浮点转为 SQL 参数
func miscNullableFloat64(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

// miscNullableText 把可选字符串转为 SQL 参数
func miscNullableText(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// miscQueryStrings 执行单列查询并收集文本结果
func miscQueryStrings(c *Conn, query string, args ...any) ([]string, error) {
	rows, err := c.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	values := make([]string, 0, 16)
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value.String)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

// DetachFactEvidenceForParagraphs 删除段落证据并撤回已无 support 的当前 claim
// 对齐 Python metadata_fact.detach_fact_evidence_for_paragraphs：返回含快照与计数的结果
func (s *Store) DetachFactEvidenceForParagraphs(paragraphHashes []string, reason string, detachedAt *float64) (map[string]any, error) {
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.detachFactEvidenceForParagraphsOnConn(paragraphHashes, reason, detachedAt, c)
	if err != nil {
		return snapshot, err
	}
	if err := c.Commit(); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

// DetachFactEvidenceForParagraphsOnConn 在调用方事务内解绑段落事实证据
// 对齐 Python detach_fact_evidence_for_paragraphs 的 conn 参数分支：不自行提交
func (s *Store) DetachFactEvidenceForParagraphsOnConn(paragraphHashes []string, reason string, detachedAt *float64, conn *Conn) (map[string]any, error) {
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	return s.detachFactEvidenceForParagraphsOnConn(paragraphHashes, reason, detachedAt, c)
}

// detachFactEvidenceForParagraphsOnConn 在给定连接上执行解绑，不管理事务提交
func (s *Store) detachFactEvidenceForParagraphsOnConn(paragraphHashes []string, reason string, detachedAt *float64, c *Conn) (map[string]any, error) {
	snapshot, err := s.SnapshotFactEvidenceForParagraphs(paragraphHashes, c)
	if err != nil {
		return nil, err
	}
	removed, retracted, err := s.miscDetachFactEvidenceForParagraphs(paragraphHashes, reason, c, detachedAt)
	if err != nil {
		return snapshot, err
	}
	snapshot["detached_evidence_count"] = removed
	snapshot["retracted_claim_ids"] = retracted
	return snapshot, nil
}

// miscDetachFactEvidenceForParagraphs 删除段落证据并撤回已无 support 的当前 claim
// 对齐 Python metadata_fact.py detach_fact_evidence_for_paragraphs 的段落分支
func (s *Store) miscDetachFactEvidenceForParagraphs(paragraphHashes []string, reason string, c *Conn, detachedAt *float64) (int, []string, error) {
	hashes := normalizeHashSequence(paragraphHashes)
	if len(hashes) == 0 {
		return 0, nil, nil
	}
	placeholders := sqlPlaceholders(len(hashes))
	query := fmt.Sprintf(`
		SELECT *
		FROM fact_evidence
		WHERE (evidence_type = 'paragraph' AND evidence_id IN (%s))
		   OR (evidence_type = 'source' AND json_extract(metadata_json, '$.paragraph_hash') IN (%s))
		`, placeholders, placeholders)
	args := append(stringArgs(hashes), stringArgs(hashes)...)
	rows, err := c.Query(query, args...)
	if err != nil {
		return 0, nil, err
	}
	evidence, err := scanRowsToMaps(rows)
	if err != nil {
		return 0, nil, err
	}
	if len(evidence) == 0 {
		return 0, nil, nil
	}

	now := nowTimestamp()
	if detachedAt != nil {
		now = *detachedAt
	}
	removedSupport := make(map[string][]string)
	supportOrder := make([]string, 0, len(evidence))
	for _, item := range evidence {
		claimID := strings.TrimSpace(stringFromRow(item, "claim_id"))
		evidenceType := strings.TrimSpace(stringFromRow(item, "evidence_type"))
		evidenceID := strings.TrimSpace(stringFromRow(item, "evidence_id"))
		stance := strings.TrimSpace(stringFromRow(item, "stance"))
		if _, err := c.Exec(`
			DELETE FROM fact_evidence
			WHERE claim_id = ? AND evidence_type = ? AND evidence_id = ? AND stance = ?
			`, claimID, evidenceType, evidenceID, stance); err != nil {
			return 0, nil, err
		}
		if stance == "support" {
			if _, ok := removedSupport[claimID]; !ok {
				supportOrder = append(supportOrder, claimID)
			}
			removedSupport[claimID] = append(removedSupport[claimID], evidenceID)
		}
	}

	retracted := make([]string, 0, len(supportOrder))
	for _, claimID := range supportOrder {
		var remaining int64
		if err := c.QueryRow(`
			SELECT COUNT(*)
			FROM fact_evidence
			WHERE claim_id = ? AND stance = 'support'
			`, claimID).Scan(&remaining); err != nil {
			return 0, nil, err
		}
		if remaining > 0 {
			continue
		}
		var status sql.NullString
		var scopeType, scopeID sql.NullString
		err := c.QueryRow("SELECT status, scope_type, scope_id FROM fact_claims WHERE claim_id = ?", claimID).
			Scan(&status, &scopeType, &scopeID)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return 0, nil, err
		}
		if status.String != "active" && status.String != "conflicted" {
			continue
		}
		if _, err := c.Exec(`
			UPDATE fact_claims
			SET status = 'retracted', valid_to = COALESCE(valid_to, ?), updated_at = ?
			WHERE claim_id = ?
			`, now, now, claimID); err != nil {
			return 0, nil, err
		}
		removedIDs := append([]string(nil), removedSupport[claimID]...)
		sort.Strings(removedIDs)
		evidenceID := ""
		if len(removedIDs) > 0 {
			evidenceID = removedIDs[0]
		}
		claimToken := claimID
		if _, err := factAppendTransition(c, &claimToken, nil, "detach_evidence", reason, "paragraph", evidenceID, now); err != nil {
			return 0, nil, err
		}
		if scopeType.String == "person" && scopeID.String != "" {
			if _, err := c.Exec("UPDATE person_profile_snapshots SET expires_at=0 WHERE person_id=?", scopeID.String); err != nil {
				return 0, nil, err
			}
		}
		retracted = append(retracted, claimID)
	}

	if len(retracted) > 0 {
		paragraphIDs := make([]string, 0, len(evidence))
		seen := make(map[string]struct{}, len(evidence))
		for _, item := range evidence {
			if stringFromRow(item, "evidence_type") != "source" {
				continue
			}
			rawMetadata := item["metadata_json"]
			if rawMetadata == nil || stringFromRow(item, "metadata_json") == "" {
				continue
			}
			metadata := decodeMetadata(rawMetadata)
			paragraphID := strings.TrimSpace(stringFromRow(metadata, "paragraph_hash"))
			if paragraphID == "" {
				continue
			}
			if _, ok := seen[paragraphID]; ok {
				continue
			}
			seen[paragraphID] = struct{}{}
			paragraphIDs = append(paragraphIDs, paragraphID)
		}
		for _, paragraphID := range paragraphIDs {
			var source sql.NullString
			err := c.QueryRow("SELECT source FROM paragraphs WHERE hash = ?", paragraphID).Scan(&source)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return 0, nil, err
			}
			if strings.TrimSpace(source.String) == "" {
				continue
			}
			s.EnqueueEpisodeSourceRebuild(strings.TrimSpace(source.String), "fact_evidence_retracted", EpisodeEnqueueOptions{Conn: c})
		}
	}
	return len(evidence), retracted, nil
}
