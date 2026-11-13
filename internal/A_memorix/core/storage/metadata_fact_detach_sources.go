package storage

// 按实际来源撤销事实证据，对齐 Python metadata_fact.detach_fact_evidence_for_sources

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// DetachFactEvidenceForSources 撤销指定 observation 来源；其他来源仍支持事实时不撤回 claim
func (s *Store) DetachFactEvidenceForSources(observationIDs []string, reason string, detachedAt *float64) (map[string]any, error) {
	ids := factTrimmedDedupe(observationIDs)
	empty := map[string]any{
		"paragraph_hashes": []string{}, "claims": []map[string]any{}, "evidence": []map[string]any{},
		"detached_evidence_count": 0, "retracted_claim_ids": []string{},
	}
	if len(ids) == 0 {
		return empty, nil
	}
	c, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	placeholders := factPlaceholders(len(ids))
	rows, err := factQueryMaps(c, fmt.Sprintf(
		"SELECT * FROM fact_evidence WHERE evidence_type='source' AND evidence_id IN (%s)", placeholders,
	), factStringList(ids)...)
	if err != nil {
		return nil, err
	}
	evidence := factEvidenceRows(rows)
	claimIDs := make([]string, 0, len(evidence))
	seenClaims := map[string]struct{}{}
	for _, item := range evidence {
		claimID := factStrip(factPyStrOrEmpty(item["claim_id"]))
		if claimID == "" {
			continue
		}
		if _, exists := seenClaims[claimID]; exists {
			continue
		}
		seenClaims[claimID] = struct{}{}
		claimIDs = append(claimIDs, claimID)
	}
	claims := make([]map[string]any, 0, len(claimIDs))
	for _, claimID := range claimIDs {
		claim, err := s.GetFactClaim(claimID)
		if err != nil {
			return nil, err
		}
		if claim != nil {
			claims = append(claims, claim)
		}
	}
	for _, claimID := range claimIDs {
		remaining, err := factQueryMaps(c, fmt.Sprintf(`
			SELECT 1 FROM fact_evidence
			WHERE claim_id=? AND evidence_type='source' AND stance='support'
			  AND evidence_id NOT IN (%s) LIMIT 1
		`, placeholders), append([]any{claimID}, factStringList(ids)...)...)
		if err != nil {
			return nil, err
		}
		if len(remaining) > 0 {
			continue
		}
		anchors, err := factQueryMaps(c, `
			SELECT * FROM fact_evidence
			WHERE claim_id=? AND evidence_type='paragraph'
			  AND json_extract(metadata_json, '$.source_managed')=1
		`, claimID)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, factEvidenceRows(anchors)...)
	}
	snapshot := map[string]any{"paragraph_hashes": []string{}, "claims": claims, "evidence": evidence}
	return s.detachFactEvidenceSnapshot(snapshot, reason, c, detachedAt)
}

// detachFactEvidenceSnapshot 对齐 _detach_fact_evidence_snapshot：删除证据并撤回已无 support 的 claim
func (s *Store) detachFactEvidenceSnapshot(snapshot map[string]any, reason string, c *Conn, detachedAt *float64) (map[string]any, error) {
	evidence := sliceOfMaps(snapshot["evidence"])
	if len(evidence) == 0 {
		snapshot["detached_evidence_count"] = 0
		snapshot["retracted_claim_ids"] = []string{}
		return snapshot, nil
	}
	now := nowTimestamp()
	if detachedAt != nil {
		now = *detachedAt
	}
	removedSupport := map[string][]string{}
	supportOrder := make([]string, 0, len(evidence))
	for _, item := range evidence {
		claimID := strings.TrimSpace(factPyStrOrEmpty(item["claim_id"]))
		evidenceType := strings.TrimSpace(factPyStrOrEmpty(item["evidence_type"]))
		evidenceID := strings.TrimSpace(factPyStrOrEmpty(item["evidence_id"]))
		stance := strings.TrimSpace(factPyStrOrEmpty(item["stance"]))
		if claimID == "" || evidenceType == "" || evidenceID == "" {
			continue
		}
		if _, err := c.Exec(`
			DELETE FROM fact_evidence
			WHERE claim_id = ? AND evidence_type = ? AND evidence_id = ? AND stance = ?
		`, claimID, evidenceType, evidenceID, stance); err != nil {
			return snapshot, err
		}
		if stance == "support" {
			if _, exists := removedSupport[claimID]; !exists {
				supportOrder = append(supportOrder, claimID)
			}
			removedSupport[claimID] = append(removedSupport[claimID], evidenceID)
		}
	}
	retracted := make([]string, 0, len(supportOrder))
	for _, claimID := range supportOrder {
		var remaining int64
		if err := c.QueryRow(`SELECT COUNT(*) FROM fact_evidence WHERE claim_id = ? AND stance = 'support'`, claimID).Scan(&remaining); err != nil {
			return snapshot, err
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
			return snapshot, err
		}
		if status.String != "active" && status.String != "conflicted" {
			continue
		}
		if _, err := c.Exec(`
			UPDATE fact_claims
			SET status = 'retracted', valid_to = COALESCE(valid_to, ?), updated_at = ?
			WHERE claim_id = ?
		`, now, now, claimID); err != nil {
			return snapshot, err
		}
		removedIDs := append([]string(nil), removedSupport[claimID]...)
		sort.Strings(removedIDs)
		evidenceID := ""
		if len(removedIDs) > 0 {
			evidenceID = removedIDs[0]
		}
		claimToken := claimID
		if _, err := factAppendTransition(c, &claimToken, nil, "detach_evidence", reason, "source", evidenceID, now); err != nil {
			return snapshot, err
		}
		if scopeType.String == "person" && scopeID.String != "" {
			if _, err := c.Exec("UPDATE person_profile_snapshots SET expires_at=0 WHERE person_id=?", scopeID.String); err != nil {
				return snapshot, err
			}
		}
		retracted = append(retracted, claimID)
	}
	if len(retracted) > 0 {
		paragraphIDs := make([]string, 0, len(evidence))
		seen := map[string]struct{}{}
		for _, item := range evidence {
			if factPyStrOrEmpty(item["evidence_type"]) != "source" {
				continue
			}
			metadata := decodeMetadata(item["metadata_json"])
			paragraphID := strings.TrimSpace(factPyStrOrEmpty(metadata["paragraph_hash"]))
			if paragraphID == "" {
				continue
			}
			if _, exists := seen[paragraphID]; exists {
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
				return snapshot, err
			}
			if strings.TrimSpace(source.String) == "" {
				continue
			}
			s.EnqueueEpisodeSourceRebuild(strings.TrimSpace(source.String), "fact_evidence_retracted", EpisodeEnqueueOptions{Conn: c})
		}
	}
	if err := c.Commit(); err != nil {
		return snapshot, err
	}
	snapshot["detached_evidence_count"] = len(evidence)
	snapshot["retracted_claim_ids"] = retracted
	return snapshot, nil
}

func sliceOfMaps(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if entry, ok := item.(map[string]any); ok {
				out = append(out, entry)
			}
		}
		return out
	default:
		return nil
	}
}
