package storage

// A_memorix 事实证据与 claim 状态流转，对齐 Python core/storage/metadata_fact.py
// 证据只增强或反驳，不隐式改变 claim 状态；撤回、恢复、分类更新都留审计转换

import (
	"fmt"
	"math"
)

// FactEvidenceUpsert 对齐 Python add_fact_evidence 的关键字参数
type FactEvidenceUpsert struct {
	EvidenceType string
	EvidenceID   string
	Stance       string
	Weight       *float64
	Metadata     map[string]any
	Reason       string
	ObservedAt   *float64
}

// FactClaimAudit 对齐 Python retract_fact_claim 与 restore_fact_claim 的审计参数
// At 对应 retracted_at 或 restored_at，nil 表示取当前时间
type FactClaimAudit struct {
	Reason       string
	EvidenceType string
	EvidenceID   string
	At           *float64
}

// FactClaimClassification 对齐 Python update_fact_claim_classification 的参数
type FactClaimClassification struct {
	Stability      string
	ProfileSection string
	Authority      string
	Confidence     float64
	ValidFrom      *float64
	ValidTo        *float64
	Reason         string
	UpdatedAt      *float64
}

// AddFactEvidence 给既有 claim 增加证据，不隐式改变 claim 状态
func (s *Store) AddFactEvidence(claimID string, in FactEvidenceUpsert) (map[string]any, error) {
	claimToken, err := factRequiredToken("claim_id", claimID)
	if err != nil {
		return nil, err
	}
	normalizedType, normalizedID, err := factEvidenceIdentity(in.EvidenceType, in.EvidenceID)
	if err != nil {
		return nil, err
	}
	if normalizedType == "" {
		return nil, fmt.Errorf("事实证据不能为空")
	}
	normalizedStance, err := factEnumToken("stance", factStringOrDefault(in.Stance, "support"), factEvidenceStances)
	if err != nil {
		return nil, err
	}
	now := nowTimestamp()
	if in.ObservedAt != nil {
		now = *in.ObservedAt
	}

	var created map[string]any
	var result map[string]any
	err = s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		exists, err := factQueryMaps(c, "SELECT 1 FROM fact_claims WHERE claim_id = ?", claimToken)
		if err != nil {
			return err
		}
		if len(exists) == 0 {
			return fmt.Errorf("事实 claim 不存在: %s", claimToken)
		}
		existing, err := factLoadEvidence(c, claimToken, normalizedType, normalizedID, normalizedStance)
		if err != nil {
			return err
		}
		if existing != nil {
			result = factEvidenceResult(existing, false, true)
			return nil
		}
		if err := factUpsertEvidenceInTx(c, claimToken, normalizedType, normalizedID, normalizedStance,
			factFloatOrDefault(in.Weight, 1.0), now, in.Metadata); err != nil {
			return err
		}
		if _, err := factAppendTransition(c, &claimToken, &claimToken, normalizedStance+"_evidence",
			in.Reason, normalizedType, normalizedID, now); err != nil {
			return err
		}
		created, err = factLoadEvidence(c, claimToken, normalizedType, normalizedID, normalizedStance)
		return err
	})
	if err != nil {
		return nil, err
	}
	if result != nil {
		return result, nil
	}
	if created == nil {
		return nil, fmt.Errorf("事实证据写入后丢失: %s/%s/%s", claimToken, normalizedType, normalizedID)
	}
	return factEvidenceResult(created, true, false), nil
}

// RetractFactClaim 显式撤回 claim，不根据文本关键词推断
func (s *Store) RetractFactClaim(claimID string, in FactClaimAudit) (map[string]any, error) {
	token, err := factRequiredToken("claim_id", claimID)
	if err != nil {
		return nil, err
	}
	now := nowTimestamp()
	if in.At != nil {
		now = *in.At
	}
	var updated map[string]any
	err = s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		previous, err := factLoadClaim(c, token)
		if err != nil {
			return err
		}
		if previous == nil {
			return fmt.Errorf("事实 claim 不存在: %s", token)
		}
		if _, err := c.Exec(`
			UPDATE fact_claims
			SET status = 'retracted', valid_to = COALESCE(valid_to, ?), updated_at = ?
			WHERE claim_id = ?
			`, now, now, token); err != nil {
			return err
		}
		if factPyStrOrEmpty(previous["status"]) != "retracted" {
			if _, err := factAppendTransition(c, &token, nil, "retract", in.Reason, in.EvidenceType, in.EvidenceID, now); err != nil {
				return err
			}
		}
		updated, err = factLoadClaim(c, token)
		return err
	})
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, fmt.Errorf("事实 claim 撤回后丢失: %s", token)
	}
	return updated, nil
}

// RestoreFactClaim 恢复一条被撤回或取代的 claim；存在异值 active 时仅恢复为 conflicted
func (s *Store) RestoreFactClaim(claimID string, in FactClaimAudit) (map[string]any, error) {
	token, err := factRequiredToken("claim_id", claimID)
	if err != nil {
		return nil, err
	}
	now := nowTimestamp()
	if in.At != nil {
		now = *in.At
	}
	var updated map[string]any
	var result map[string]any
	err = s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		previous, err := factLoadClaim(c, token)
		if err != nil {
			return err
		}
		if previous == nil {
			return fmt.Errorf("事实 claim 不存在: %s", token)
		}
		conflicts, err := factActiveConflicts(c,
			factPyStrOrEmpty(previous["scope_type"]),
			factPyStrOrEmpty(previous["scope_id"]),
			factPyStrOrEmpty(previous["conflict_group"]),
			token,
		)
		if err != nil {
			return err
		}
		nextStatus := "active"
		if len(conflicts) > 0 {
			nextStatus = "conflicted"
		}
		if factPyStrOrEmpty(previous["status"]) == nextStatus {
			result = factClaimStateResult(previous, false, true, conflicts)
			return nil
		}
		if _, err := c.Exec(`
			UPDATE fact_claims
			SET status = ?, valid_to = NULL, last_confirmed_at = ?, updated_at = ?
			WHERE claim_id = ?
			`, nextStatus, now, now, token); err != nil {
			return err
		}
		if _, err := factAppendTransition(c, &token, &token, "restore", in.Reason, in.EvidenceType, in.EvidenceID, now); err != nil {
			return err
		}
		updated, err = factLoadClaim(c, token)
		if err != nil {
			return err
		}
		if updated == nil {
			return fmt.Errorf("事实 claim 恢复后丢失: %s", token)
		}
		result = factClaimStateResult(updated, true, false, conflicts)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("事实 claim 恢复后丢失: %s", token)
	}
	return result, nil
}

// UpdateFactClaimClassification 更新不参与 claim 身份计算的分类字段
func (s *Store) UpdateFactClaimClassification(claimID string, in FactClaimClassification) (map[string]any, error) {
	token, err := factRequiredToken("claim_id", claimID)
	if err != nil {
		return nil, err
	}
	normalizedStability, err := factEnumToken("stability", in.Stability, factStabilities)
	if err != nil {
		return nil, err
	}
	normalizedProfileSection, err := factEnumToken("profile_section", in.ProfileSection, factProfileSections)
	if err != nil {
		return nil, err
	}
	normalizedAuthority, err := factEnumToken("authority", in.Authority, factAuthorities)
	if err != nil {
		return nil, err
	}
	normalizedConfidence := in.Confidence
	if math.IsNaN(normalizedConfidence) || normalizedConfidence < 0 || normalizedConfidence > 1 {
		return nil, fmt.Errorf("confidence 必须在 0 到 1 之间")
	}
	normalizedValidFrom := asOptionalFloat(in.ValidFrom)
	normalizedValidTo := asOptionalFloat(in.ValidTo)
	if normalizedValidFrom != nil && normalizedValidTo != nil && *normalizedValidTo < *normalizedValidFrom {
		return nil, fmt.Errorf("valid_to 不能早于 valid_from")
	}
	now := nowTimestamp()
	if in.UpdatedAt != nil {
		now = *in.UpdatedAt
	}

	var updated map[string]any
	changed := false
	err = s.Transaction(true, func(tx *Tx) error {
		c := tx.Conn()
		previous, err := factLoadClaim(c, token)
		if err != nil {
			return err
		}
		if previous == nil {
			return fmt.Errorf("事实 claim 不存在: %s", token)
		}
		changed = factPyStrOrEmpty(previous["stability"]) != normalizedStability ||
			factPyStrOrEmpty(previous["profile_section"]) != normalizedProfileSection ||
			factPyStrOrEmpty(previous["authority"]) != normalizedAuthority ||
			factFloatOf(previous["confidence"]) != normalizedConfidence ||
			!factOptionalFloatEqual(previous["valid_from"], normalizedValidFrom) ||
			!factOptionalFloatEqual(previous["valid_to"], normalizedValidTo)
		if _, err := c.Exec(`
			UPDATE fact_claims
			SET stability = ?, profile_section = ?, authority = ?, confidence = ?,
				valid_from = ?, valid_to = ?, updated_at = ?
			WHERE claim_id = ?
			`,
			normalizedStability,
			normalizedProfileSection,
			normalizedAuthority,
			normalizedConfidence,
			factFloatArg(normalizedValidFrom),
			factFloatArg(normalizedValidTo),
			now,
			token,
		); err != nil {
			return err
		}
		if changed {
			if _, err := factAppendTransition(c, &token, &token, "classify", in.Reason, "manual", "", now); err != nil {
				return err
			}
		}
		updated, err = factLoadClaim(c, token)
		return err
	})
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, fmt.Errorf("事实 claim 分类更新后丢失: %s", token)
	}
	result := make(map[string]any, len(updated)+1)
	for key, value := range updated {
		result[key] = value
	}
	result["changed"] = changed
	return result, nil
}
