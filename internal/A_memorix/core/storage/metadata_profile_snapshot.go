package storage

// A_memorix 人物画像快照，逐条对齐 Python core/storage/metadata_profile.py 的快照部分
// 快照按 person_id 递增 profile_version；证据指纹未变时只延长有效期，不新增版本

import (
	"database/sql"
	"errors"
	"fmt"
)

// snapshotSelectColumns 与 Python 查询列顺序逐字一致
const snapshotSelectColumns = `
			SELECT
				snapshot_id, person_id, profile_version, profile_text,
				aliases_json, relation_edges_json, vector_evidence_json, evidence_ids_json,
				fact_claim_ids_json, evidence_fingerprint, updated_at, expires_at, source_note
			FROM person_profile_snapshots`

// scanPersonProfileSnapshot 对齐 Python get_latest_person_profile_snapshot 的返回字段
func scanPersonProfileSnapshot(scanner personProfileScanner) (map[string]any, error) {
	var (
		snapshotID          int64
		personID            string
		profileVersion      int64
		profileText         sql.NullString
		aliasesJSON         sql.NullString
		relationEdgesJSON   sql.NullString
		vectorEvidenceJSON  sql.NullString
		evidenceIDsJSON     sql.NullString
		factClaimIDsJSON    sql.NullString
		evidenceFingerprint sql.NullString
		updatedAt           sql.NullFloat64
		expiresAt           sql.NullFloat64
		sourceNote          sql.NullString
	)
	if err := scanner.Scan(
		&snapshotID, &personID, &profileVersion, &profileText,
		&aliasesJSON, &relationEdgesJSON, &vectorEvidenceJSON, &evidenceIDsJSON,
		&factClaimIDsJSON, &evidenceFingerprint, &updatedAt, &expiresAt, &sourceNote,
	); err != nil {
		return nil, err
	}
	return map[string]any{
		"snapshot_id":          snapshotID,
		"person_id":            personID,
		"profile_version":      int(profileVersion),
		"profile_text":         profileText.String,
		"aliases":              personProfileJSONList(aliasesJSON.String),
		"relation_edges":       personProfileJSONList(relationEdgesJSON.String),
		"vector_evidence":      personProfileJSONList(vectorEvidenceJSON.String),
		"evidence_ids":         personProfileJSONList(evidenceIDsJSON.String),
		"fact_claim_ids":       personProfileJSONList(factClaimIDsJSON.String),
		"evidence_fingerprint": evidenceFingerprint.String,
		"updated_at":           personProfileFloatValue(updatedAt),
		"expires_at":           personProfileFloatValue(expiresAt),
		"source_note":          sourceNote.String,
	}, nil
}

// GetLatestPersonProfileSnapshot 获取人物最新画像快照，
// 对齐 Python get_latest_person_profile_snapshot
func (s *Store) GetLatestPersonProfileSnapshot(personID string) (map[string]any, error) {
	if personID == "" {
		return nil, nil
	}
	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	snapshot, err := scanPersonProfileSnapshot(c.QueryRow(
		snapshotSelectColumns+" WHERE person_id = ? ORDER BY profile_version DESC LIMIT 1",
		personID,
	))
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

// PersonProfileSnapshotInput 对齐 Python upsert_person_profile_snapshot 的入参
type PersonProfileSnapshotInput struct {
	PersonID            string
	ProfileText         string
	Aliases             []string
	RelationEdges       []map[string]any
	VectorEvidence      []map[string]any
	EvidenceIDs         []string
	FactClaimIDs        []string
	EvidenceFingerprint string
	ExpiresAt           *float64
	SourceNote          string
	UpdatedAt           *float64
}

// UpsertPersonProfileSnapshot 写入人物画像快照（按 person_id 自动递增版本），
// 对齐 Python upsert_person_profile_snapshot
func (s *Store) UpsertPersonProfileSnapshot(input PersonProfileSnapshotInput) (map[string]any, error) {
	if input.PersonID == "" {
		return nil, errors.New("person_id 不能为空")
	}
	aliases := input.Aliases
	if aliases == nil {
		aliases = []string{}
	}
	relationEdges := input.RelationEdges
	if relationEdges == nil {
		relationEdges = []map[string]any{}
	}
	vectorEvidence := input.VectorEvidence
	if vectorEvidence == nil {
		vectorEvidence = []map[string]any{}
	}
	evidenceIDs := input.EvidenceIDs
	if evidenceIDs == nil {
		evidenceIDs = []string{}
	}
	factClaimIDs := input.FactClaimIDs
	if factClaimIDs == nil {
		factClaimIDs = []string{}
	}
	ts := nowTimestamp()
	if input.UpdatedAt != nil {
		ts = *input.UpdatedAt
	}

	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	nextVersion := 1
	var latestVersion sql.NullInt64
	err = c.QueryRow(`
		SELECT profile_version
		FROM person_profile_snapshots
		WHERE person_id = ?
		ORDER BY profile_version DESC
		LIMIT 1
	`, input.PersonID).Scan(&latestVersion)
	switch {
	case err == nil:
		nextVersion = int(latestVersion.Int64) + 1
	case isNoRows(err):
	default:
		return nil, err
	}

	if _, err := c.Exec(`
		INSERT INTO person_profile_snapshots (
			person_id, profile_version, profile_text,
			aliases_json, relation_edges_json, vector_evidence_json, evidence_ids_json,
			fact_claim_ids_json, evidence_fingerprint, updated_at, expires_at, source_note
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		input.PersonID,
		nextVersion,
		input.ProfileText,
		jsonDumps(aliases),
		jsonDumps(relationEdges),
		jsonDumps(vectorEvidence),
		jsonDumps(evidenceIDs),
		jsonDumps(factClaimIDs),
		input.EvidenceFingerprint,
		ts,
		personProfileNullableFloat(input.ExpiresAt),
		input.SourceNote,
	); err != nil {
		return nil, err
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}

	latest, err := s.GetLatestPersonProfileSnapshot(input.PersonID)
	if err != nil {
		return nil, err
	}
	if latest != nil {
		return latest, nil
	}
	return map[string]any{
		"person_id":            input.PersonID,
		"profile_version":      nextVersion,
		"profile_text":         input.ProfileText,
		"aliases":              personProfileAnyList(aliases),
		"relation_edges":       personProfileAnyMapList(relationEdges),
		"vector_evidence":      personProfileAnyMapList(vectorEvidence),
		"evidence_ids":         personProfileAnyList(evidenceIDs),
		"fact_claim_ids":       personProfileAnyList(factClaimIDs),
		"evidence_fingerprint": input.EvidenceFingerprint,
		"updated_at":           ts,
		"expires_at":           personProfileFloatAny(input.ExpiresAt),
		"source_note":          input.SourceNote,
	}, nil
}

// RefreshPersonProfileSnapshotCache 证据未变化时延长快照有效期，不创建新画像版本，
// 对齐 Python refresh_person_profile_snapshot_cache
func (s *Store) RefreshPersonProfileSnapshotCache(
	snapshotID int64,
	expiresAt *float64,
	sourceNote string,
	updatedAt *float64,
) (map[string]any, error) {
	ts := nowTimestamp()
	if updatedAt != nil {
		ts = *updatedAt
	}
	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	result, err := c.Exec(`
		UPDATE person_profile_snapshots
		SET updated_at = ?, expires_at = ?, source_note = ?
		WHERE snapshot_id = ?
	`, ts, personProfileNullableFloat(expiresAt), sourceNote, snapshotID)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		_ = c.Rollback()
		return nil, fmt.Errorf("人物画像快照不存在: snapshot_id=%d", snapshotID)
	}
	if err := c.Commit(); err != nil {
		return nil, err
	}

	var personID string
	if err := c.QueryRow(
		"SELECT person_id FROM person_profile_snapshots WHERE snapshot_id = ?",
		snapshotID,
	).Scan(&personID); err != nil {
		if isNoRows(err) {
			return nil, fmt.Errorf("人物画像快照刷新后读取失败: snapshot_id=%d", snapshotID)
		}
		return nil, err
	}
	latest, err := s.GetLatestPersonProfileSnapshot(personID)
	if err != nil {
		return nil, err
	}
	if latest == nil {
		return nil, fmt.Errorf("人物画像快照刷新后人物记录丢失: snapshot_id=%d", snapshotID)
	}
	return latest, nil
}
