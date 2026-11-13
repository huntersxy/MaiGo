package storage

// A_memorix 事实证据快照与恢复，对齐 Python core/storage/metadata_fact.py

import (
	"fmt"
	"sort"
)

// SnapshotFactEvidenceForParagraphs 读取段落证据及其 claim 状态
// 对齐 snapshot_fact_evidence_for_paragraphs；conn 为空时使用存储连接
func (s *Store) SnapshotFactEvidenceForParagraphs(paragraphHashes []string, conn *Conn) (map[string]any, error) {
	hashes := factTrimmedDedupe(paragraphHashes)
	if len(hashes) == 0 {
		return map[string]any{
			"paragraph_hashes": []string{},
			"claims":           []map[string]any{},
			"evidence":         []map[string]any{},
		}, nil
	}
	c, err := s.ResolveConn(conn)
	if err != nil {
		return nil, err
	}
	placeholders := factPlaceholders(len(hashes))
	args := append(append([]any{}, factStringList(hashes)...), factStringList(hashes)...)
	rows, err := factQueryMaps(c, fmt.Sprintf(`
		SELECT *
		FROM fact_evidence
		WHERE (evidence_type = 'paragraph' AND evidence_id IN (%s))
		   OR (evidence_type = 'source' AND json_extract(metadata_json, '$.paragraph_hash') IN (%s))
		ORDER BY claim_id ASC, evidence_id ASC, stance ASC
	`, placeholders, placeholders), args...)
	if err != nil {
		return nil, err
	}
	evidence := factEvidenceRows(rows)
	claimIDs := make([]string, 0, len(evidence))
	seen := make(map[string]struct{}, len(evidence))
	for _, item := range evidence {
		claimID := factStrip(factPyStrOrEmpty(item["claim_id"]))
		if claimID == "" {
			continue
		}
		if _, ok := seen[claimID]; ok {
			continue
		}
		seen[claimID] = struct{}{}
		claimIDs = append(claimIDs, claimID)
	}
	claims := []map[string]any{}
	if len(claimIDs) > 0 {
		rows, err = factQueryMaps(c, fmt.Sprintf(`
			SELECT * FROM fact_claims
			WHERE claim_id IN (%s)
			ORDER BY claim_id ASC
		`, factPlaceholders(len(claimIDs))), factStringList(claimIDs)...)
		if err != nil {
			return nil, err
		}
		claims = factClaimRows(rows)
	}
	return map[string]any{
		"paragraph_hashes": hashes,
		"claims":           claims,
		"evidence":         evidence,
	}, nil
}

// RestoreFactEvidenceSnapshot 恢复 detach 快照中的 claim 状态与证据
// 对齐 restore_fact_evidence_snapshot；conn 为空时开启独立写事务
func (s *Store) RestoreFactEvidenceSnapshot(snapshot map[string]any, reason string, restoredAt *float64, conn *Conn) (map[string]any, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("fact evidence snapshot 必须是字典")
	}
	if conn != nil {
		return s.restoreFactEvidenceSnapshotOnConn(snapshot, reason, restoredAt, conn)
	}
	var result map[string]any
	err := s.Transaction(true, func(tx *Tx) error {
		value, err := s.restoreFactEvidenceSnapshotOnConn(snapshot, reason, restoredAt, tx.Conn())
		if err != nil {
			return err
		}
		result = value
		return nil
	})
	return result, err
}

// restoreFactEvidenceSnapshotOnConn 在给定连接上执行快照恢复
func (s *Store) restoreFactEvidenceSnapshotOnConn(snapshot map[string]any, reason string, restoredAt *float64, c *Conn) (map[string]any, error) {
	evidence := factMapSlice(snapshot["evidence"])
	claims := factMapSlice(snapshot["claims"])
	now := nowTimestamp()
	if restoredAt != nil {
		now = *restoredAt
	}
	evidenceClaimIDs := make(map[string]struct{}, len(evidence))
	for _, item := range evidence {
		claimID := factStrip(factPyStrOrEmpty(item["claim_id"]))
		if claimID != "" {
			evidenceClaimIDs[claimID] = struct{}{}
		}
	}
	claimsByID := make(map[string]map[string]any, len(claims))
	for _, item := range claims {
		claimID := factStrip(factPyStrOrEmpty(item["claim_id"]))
		if _, ok := evidenceClaimIDs[claimID]; ok {
			claimsByID[claimID] = item
		}
	}
	for claimID := range evidenceClaimIDs {
		if _, ok := claimsByID[claimID]; !ok {
			return nil, fmt.Errorf("fact evidence snapshot 缺少证据对应的 claim 快照")
		}
	}

	restoredClaimIDs := make([]string, 0, len(claimsByID))
	touchedClaimIDs := make(map[string]struct{})
	claimIDList := make([]string, 0, len(claimsByID))
	for claimID := range claimsByID {
		claimIDList = append(claimIDList, claimID)
	}
	sort.Strings(claimIDList)
	for _, claimID := range claimIDList {
		claim := claimsByID[claimID]
		previousStatus := factStrip(factPyStrOrEmpty(claim["status"]))
		if _, ok := factStatuses[previousStatus]; !ok {
			return nil, fmt.Errorf("fact evidence snapshot 包含无效 claim 状态")
		}
		var currentStatus string
		if err := c.QueryRow("SELECT status FROM fact_claims WHERE claim_id = ?", claimID).Scan(&currentStatus); err != nil {
			if isNoRows(err) {
				return nil, fmt.Errorf("fact evidence snapshot 对应 claim 不存在: %s", claimID)
			}
			return nil, err
		}
		if currentStatus == previousStatus {
			continue
		}
		var latestTransition string
		if err := c.QueryRow(`
			SELECT transition_type
			FROM fact_transitions
			WHERE old_claim_id = ? OR new_claim_id = ?
			ORDER BY transition_id DESC
			LIMIT 1
		`, claimID, claimID).Scan(&latestTransition); err != nil && !isNoRows(err) {
			return nil, err
		}
		if latestTransition != "detach_evidence" {
			continue
		}
		if _, err := c.Exec(`
			UPDATE fact_claims
			SET status = ?, valid_to = ?, last_confirmed_at = ?, updated_at = ?
			WHERE claim_id = ?
		`, previousStatus, factNormalizeSQLValue(claim["valid_to"]), factNormalizeSQLValue(claim["last_confirmed_at"]), now, claimID); err != nil {
			return nil, err
		}
		restoredClaimIDs = append(restoredClaimIDs, claimID)
		touchedClaimIDs[claimID] = struct{}{}
	}

	restoredEvidenceCount := 0
	for _, item := range evidence {
		claimID := factStrip(factPyStrOrEmpty(item["claim_id"]))
		evidenceType, evidenceID, err := factEvidenceIdentity(factPyStrOrEmpty(item["evidence_type"]), factPyStrOrEmpty(item["evidence_id"]))
		if err != nil {
			return nil, err
		}
		if evidenceType == "" {
			return nil, fmt.Errorf("fact evidence snapshot 包含空证据")
		}
		stance, err := factEnumToken("stance", factPyStrOrEmpty(item["stance"]), factEvidenceStances)
		if err != nil {
			return nil, err
		}
		weight := factFloatByDefault(item["weight"], 1.0)
		if weight == 0 {
			weight = 1.0
		}
		weight = factClamp01(weight)
		observed := now
		if parsed := OptionalFloat(item["observed_at"]); parsed != nil && *parsed != 0 {
			observed = *parsed
		}
		metadata := map[string]any{}
		if value, ok := item["metadata"].(map[string]any); ok {
			metadata = value
		}
		metadataJSON, err := factSortedJSONDumps(metadata)
		if err != nil {
			return nil, err
		}
		cursor, err := c.Exec(`
			INSERT OR IGNORE INTO fact_evidence (
				claim_id, evidence_type, evidence_id, stance,
				weight, observed_at, metadata_json
			) VALUES (?, ?, ?, ?, ?, ?, ?)
		`, claimID, evidenceType, evidenceID, stance, weight, observed, metadataJSON)
		if err != nil {
			return nil, err
		}
		affected, err := cursor.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected > 0 {
			restoredEvidenceCount++
			touchedClaimIDs[claimID] = struct{}{}
		}
	}

	touched := make([]string, 0, len(touchedClaimIDs))
	for claimID := range touchedClaimIDs {
		touched = append(touched, claimID)
	}
	sort.Strings(touched)
	for _, claimID := range touched {
		evidenceIDs := make([]string, 0)
		for _, item := range evidence {
			if factStrip(factPyStrOrEmpty(item["claim_id"])) != claimID {
				continue
			}
			if evidenceID := factStrip(factPyStrOrEmpty(item["evidence_id"])); evidenceID != "" {
				evidenceIDs = append(evidenceIDs, evidenceID)
			}
		}
		sort.Strings(evidenceIDs)
		firstEvidenceID := ""
		if len(evidenceIDs) > 0 {
			firstEvidenceID = evidenceIDs[0]
		}
		if _, err := factAppendTransition(c, &claimID, &claimID, "restore_evidence", reason, "paragraph", firstEvidenceID, now); err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"restored_claim_ids":      restoredClaimIDs,
		"restored_evidence_count": restoredEvidenceCount,
	}, nil
}
