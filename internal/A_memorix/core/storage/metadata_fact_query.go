package storage

// A_memorix 事实账本查询，对齐 Python core/storage/metadata_fact.py 的读取方法
// 画像取数只依赖确定性排序与有效区间，不看向量召回分数

import (
	"fmt"
	"strings"
)

// FactClaimQuery 对齐 Python list_fact_claims 的关键字参数
// Statuses 为空表示默认 statuses=["active"]，Limit 为 0 表示默认 200
type FactClaimQuery struct {
	ScopeType   string
	ScopeID     string
	Statuses    []string
	EffectiveAt *float64
	Limit       int
}

// GetFactClaim 读取单条 claim，claim_id 为空或不存在时返回 nil
func (s *Store) GetFactClaim(claimID string) (map[string]any, error) {
	token := factStrip(claimID)
	if token == "" {
		return nil, nil
	}
	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	return factLoadClaim(c, token)
}

// ListFactClaims 按作用域与状态列出 claim
func (s *Store) ListFactClaims(query FactClaimQuery) ([]map[string]any, error) {
	normalizedScopeType, err := factEnumToken("scope_type", query.ScopeType, factScopeTypes)
	if err != nil {
		return nil, err
	}
	normalizedScopeID, err := factRequiredToken("scope_id", query.ScopeID)
	if err != nil {
		return nil, err
	}
	statuses := query.Statuses
	if len(statuses) == 0 {
		statuses = []string{"active"}
	}
	normalizedStatuses := make([]string, 0, len(statuses))
	for _, item := range dedupeStrings(statuses) {
		token, err := factEnumToken("status", item, factStatuses)
		if err != nil {
			return nil, err
		}
		normalizedStatuses = append(normalizedStatuses, token)
	}
	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	sqlText := fmt.Sprintf(`
		SELECT *
		FROM fact_claims
		WHERE scope_type = ? AND scope_id = ?
		  AND status IN (%s)
		`, factPlaceholders(len(normalizedStatuses)))
	args := []any{normalizedScopeType, normalizedScopeID}
	args = append(args, factStringsToAny(normalizedStatuses)...)
	if query.EffectiveAt != nil {
		point := *query.EffectiveAt
		sqlText += " AND (valid_from IS NULL OR valid_from <= ?) AND (valid_to IS NULL OR valid_to > ?)"
		args = append(args, point, point)
	}
	sqlText += " ORDER BY updated_at DESC, claim_id ASC LIMIT ?"
	args = append(args, factLimitArg(query.Limit, 200))
	rows, err := factQueryMaps(c, sqlText, args...)
	if err != nil {
		return nil, err
	}
	return factClaimRows(rows), nil
}

// ListCurrentPersonFactClaims 返回可进入人物画像的当前事实，排序不依赖向量召回分数
func (s *Store) ListCurrentPersonFactClaims(personID string, effectiveAt *float64, limit int) ([]map[string]any, error) {
	token, err := factRequiredToken("person_id", personID)
	if err != nil {
		return nil, err
	}
	point := nowTimestamp()
	if effectiveAt != nil {
		point = *effectiveAt
	}
	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	rows, err := factQueryMaps(c, `
		SELECT *
		FROM fact_claims
		WHERE scope_type = 'person' AND scope_id = ? AND status = 'active'
		  AND stability = 'stable'
		  AND NOT EXISTS (
		      SELECT 1 FROM fact_evidence e JOIN paragraphs p ON p.hash=e.evidence_id
		      WHERE e.claim_id=fact_claims.claim_id AND e.evidence_type='paragraph'
		        AND json_extract(p.metadata, '$.dedup_phase')='pending'
		  )
		  AND (valid_from IS NULL OR valid_from <= ?)
		  AND (valid_to IS NULL OR valid_to > ?)
		ORDER BY
		    CASE profile_section
		        WHEN 'identity_settings' THEN 0
		        WHEN 'relationship_settings' THEN 1
		        WHEN 'stable_facts' THEN 2
		        WHEN 'interaction_preferences' THEN 3
		        WHEN 'recent_interactions' THEN 4
		        ELSE 5
		    END ASC,
		    CASE authority
		        WHEN 'manual' THEN 0
		        WHEN 'direct_user' THEN 1
		        ELSE 2
		    END ASC,
		    fact_key ASC,
		    value_normalized ASC,
		    claim_id ASC
		LIMIT ?
		`, token, point, point, factLimitArg(limit, 200))
	if err != nil {
		return nil, err
	}
	return factClaimRows(rows), nil
}

// ListPersonProfileFactClaims 返回画像可投影 claim，包括可信稳定事实和明确标记的不确定事实
func (s *Store) ListPersonProfileFactClaims(personID string, effectiveAt *float64, limit int) ([]map[string]any, error) {
	token, err := factRequiredToken("person_id", personID)
	if err != nil {
		return nil, err
	}
	point := nowTimestamp()
	if effectiveAt != nil {
		point = *effectiveAt
	}
	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	rows, err := factQueryMaps(c, `
		SELECT *
		FROM fact_claims
		WHERE scope_type = 'person' AND scope_id = ? AND status = 'active'
		  AND (valid_from IS NULL OR valid_from <= ?)
		  AND (valid_to IS NULL OR valid_to > ?)
		  AND NOT EXISTS (
		      SELECT 1 FROM fact_evidence e JOIN paragraphs p ON p.hash=e.evidence_id
		      WHERE e.claim_id=fact_claims.claim_id AND e.evidence_type='paragraph'
		        AND json_extract(p.metadata, '$.dedup_phase')='pending'
		  )
		  AND (
		        (
		            stability = 'stable'
		        )
		        OR (
		            stability = 'uncertain'
		            AND authority = 'summary_derived'
		            AND profile_section = 'uncertain_notes'
		        )
		  )
		ORDER BY
		    CASE profile_section
		        WHEN 'identity_settings' THEN 0
		        WHEN 'relationship_settings' THEN 1
		        WHEN 'stable_facts' THEN 2
		        WHEN 'interaction_preferences' THEN 3
		        WHEN 'recent_interactions' THEN 4
		        ELSE 5
		    END ASC,
		    CASE authority
		        WHEN 'manual' THEN 0
		        WHEN 'direct_user' THEN 1
		        WHEN 'imported' THEN 2
		        ELSE 3
		    END ASC,
		    fact_key ASC,
		    value_normalized ASC,
		    claim_id ASC
		LIMIT ?
		`, token, point, point, factLimitArg(limit, 200))
	if err != nil {
		return nil, err
	}
	return factClaimRows(rows), nil
}

// CountUncertainPersonFactClaims 统计人物有效的未确认事实，避免为计数加载全部记录
func (s *Store) CountUncertainPersonFactClaims(personID string) (int, error) {
	token, err := factRequiredToken("person_id", personID)
	if err != nil {
		return 0, err
	}
	c, err := s.Conn()
	if err != nil {
		return 0, err
	}
	point := nowTimestamp()
	return countRows(c, `
		SELECT COUNT(*) AS total FROM fact_claims
		WHERE scope_type = 'person' AND scope_id = ? AND status = 'active'
		  AND stability = 'uncertain' AND authority = 'summary_derived'
		  AND profile_section = 'uncertain_notes'
		  AND (valid_from IS NULL OR valid_from <= ?)
		  AND (valid_to IS NULL OR valid_to > ?)
		`, token, point, point)
}

// ListUncertainPersonFactClaims 读取人物有效的未确认事实，limit 为 nil 表示不限制
func (s *Store) ListUncertainPersonFactClaims(personID string, limit *int) ([]map[string]any, error) {
	token, err := factRequiredToken("person_id", personID)
	if err != nil {
		return nil, err
	}
	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	point := nowTimestamp()
	sqlText := `
		SELECT * FROM fact_claims
		WHERE scope_type = 'person' AND scope_id = ? AND status = 'active'
		  AND stability = 'uncertain' AND authority = 'summary_derived'
		  AND profile_section = 'uncertain_notes'
		  AND (valid_from IS NULL OR valid_from <= ?)
		  AND (valid_to IS NULL OR valid_to > ?)
		ORDER BY last_confirmed_at DESC, claim_id ASC
		`
	args := []any{token, point, point}
	if limit != nil {
		sqlText += " LIMIT ?"
		args = append(args, factLimitArg(*limit, 1))
	}
	rows, err := factQueryMaps(c, sqlText, args...)
	if err != nil {
		return nil, err
	}
	return factClaimRows(rows), nil
}

// GetFactEvidence 读取 claim 的证据，按观测时间倒序
func (s *Store) GetFactEvidence(claimID string) ([]map[string]any, error) {
	token, err := factRequiredToken("claim_id", claimID)
	if err != nil {
		return nil, err
	}
	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	rows, err := factQueryMaps(c, `
		SELECT * FROM fact_evidence
		WHERE claim_id = ?
		ORDER BY observed_at DESC, evidence_type ASC, evidence_id ASC
		`, token)
	if err != nil {
		return nil, err
	}
	return factEvidenceRows(rows), nil
}

// GetFactTransitions 读取 claim 相关的状态转换审计记录
func (s *Store) GetFactTransitions(claimID string) ([]map[string]any, error) {
	token, err := factRequiredToken("claim_id", claimID)
	if err != nil {
		return nil, err
	}
	c, err := s.Conn()
	if err != nil {
		return nil, err
	}
	rows, err := factQueryMaps(c, `
		SELECT * FROM fact_transitions
		WHERE old_claim_id = ? OR new_claim_id = ?
		ORDER BY created_at ASC, transition_id ASC
		`, token, token)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		payload := make(map[string]any, len(row))
		for key, value := range row {
			payload[key] = value
		}
		payload["created_at"] = factFloatOf(row["created_at"])
		out = append(out, payload)
	}
	return out, nil
}

// factPersonFactParagraphFilter 供 backfill 复用的段落筛选条件
const factPersonFactParagraphSourcePrefix = "person_fact:"

var _ = strings.TrimSpace
