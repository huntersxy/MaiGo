package utils

// A_memorix 事实去重提交阶段，对齐 Python FactDeduplicator.commit

import (
	"context"
	"fmt"
)

// Commit 落库去重计划并返回涉及的 claim_id，对齐 Python commit
// 同一来源只增加一次证据，疑似重复不会变成不确定事实
func (d *FactDeduplicator) Commit(
	ctx context.Context,
	plan *FactDedupPlan,
	paragraphHash string,
	classification *FactDedupClassification,
) ([]string, error) {
	if d == nil || d.store == nil {
		return nil, fmt.Errorf("事实去重缺少存储")
	}
	if plan == nil {
		return nil, fmt.Errorf("去重计划不能为空")
	}
	observationID := factDedupString(plan.Source["observation_id"])
	if observationID == "" {
		return nil, fmt.Errorf("去重计划缺少来源标识")
	}
	sourceObservedAt := factDedupFloat(plan.Source["observed_at"])
	facts := make(map[string]FactDedupFact, len(plan.Facts))
	for _, fact := range plan.Facts {
		facts[fact.FactID] = fact
	}
	order := make([]string, 0, len(plan.Decisions))
	resolved := make(map[string]FactDedupTarget, len(plan.Decisions))
	for _, decision := range plan.Decisions {
		fact, ok := facts[decision.FactID]
		if !ok {
			return nil, fmt.Errorf("去重决定引用了输入以外的事实")
		}
		target := FactDedupTarget{}
		if decision.Target != nil {
			target = *decision.Target
		}
		if target.InputFactID != "" {
			previous, ok := resolved[target.InputFactID]
			if !ok {
				return nil, fmt.Errorf("去重决定引用了尚未提交的输入事实")
			}
			target = previous
		}
		targetParagraph := target.ParagraphHash
		if targetParagraph == "" {
			targetParagraph = paragraphHash
		}
		claimID := target.ClaimID
		if targetParagraph == "" {
			return nil, fmt.Errorf("去重事实缺少可检索的段落")
		}
		paragraph, err := d.store.GetParagraph(targetParagraph)
		if err != nil {
			return nil, err
		}
		if paragraph == nil || factDedupTruthy(paragraph["is_deleted"]) {
			return nil, fmt.Errorf("去重目标段落在提交前已删除")
		}
		observedAt := target.ObservedAt
		if observedAt == nil {
			observedAt = sourceObservedAt
		}
		if claimID != "" {
			claim, err := d.store.GetFactClaim(claimID)
			if err != nil {
				return nil, err
			}
			if !factDedupClaimCommitValid(claim, plan.ScopeType, plan.ScopeID) {
				return nil, fmt.Errorf("去重目标在提交前已失效")
			}
		} else {
			origin := observationID
			if decision.Action == "reuse" {
				origin = targetParagraph
			}
			authority := factDedupClassificationValue(classification, true)
			if decision.Action == "reuse" || plan.ScopeType == "chat" {
				authority = "summary_derived"
			}
			personIDs := fact.PersonIDs
			if personIDs == nil {
				personIDs = []string{}
			}
			writtenClaimID, err := d.store.UpsertFactClaim(FactClaimUpsert{
				ScopeType:      plan.ScopeType,
				ScopeID:        plan.ScopeID,
				FactKey:        "statement:" + origin + ":" + factDedupFactHash(fact.Text),
				ValueText:      fact.Text,
				EvidenceType:   "paragraph",
				EvidenceID:     targetParagraph,
				Authority:      authority,
				Stability:      factDedupClassificationValue(classification, false),
				ProfileSection: factDedupClassificationSection(classification),
				EvidenceMetadata: map[string]any{
					"fact_id":        fact.FactID,
					"source_managed": decision.Action != "reuse",
					"person_ids":     personIDs,
				},
				Reason:     "dedup_fact_registered",
				ObservedAt: observedAt,
			})
			if err != nil {
				return nil, err
			}
			if writtenClaimID == "" {
				return nil, fmt.Errorf("去重事实写入后缺少 claim_id")
			}
			claimID = writtenClaimID
		}
		metadata := make(map[string]any, len(plan.Source)+2)
		for key, value := range plan.Source {
			metadata[key] = value
		}
		metadata["paragraph_hash"] = targetParagraph
		candidateIDs := decision.CandidateIDs
		if candidateIDs == nil {
			candidateIDs = []string{}
		}
		metadata["dedup_candidate_ids"] = candidateIDs
		evidence, err := d.store.AddFactEvidence(claimID, FactEvidenceUpsert{
			EvidenceType: "source",
			EvidenceID:   observationID,
			Metadata:     metadata,
			Reason:       "dedup_source_observed",
			ObservedAt:   sourceObservedAt,
		})
		if err != nil {
			return nil, err
		}
		if evidence.Created {
			observed := any(nil)
			if evidence.ObservedAt != nil {
				observed = *evidence.ObservedAt
			}
			if err := d.store.Transaction(true, func(tx FactDedupTx) error {
				return tx.Execute(
					`UPDATE fact_claims SET last_confirmed_at=MAX(last_confirmed_at,?), `+
						`updated_at=MAX(updated_at,?) WHERE claim_id=?`,
					observed, observed, claimID,
				)
			}); err != nil {
				return nil, err
			}
		}
		if _, exists := resolved[fact.FactID]; !exists {
			order = append(order, fact.FactID)
		}
		resolved[fact.FactID] = FactDedupTarget{ClaimID: claimID, ParagraphHash: targetParagraph}
	}
	claimIDs := make([]string, 0, len(order))
	for _, factID := range order {
		claimIDs = append(claimIDs, resolved[factID].ClaimID)
	}
	return claimIDs, nil
}

// factDedupClaimCommitValid 对齐 commit 前对既有 claim 的有效性检查
func factDedupClaimCommitValid(claim map[string]any, scopeType string, scopeID string) bool {
	if claim == nil {
		return false
	}
	if factDedupString(claim["status"]) != "active" || factDedupString(claim["stability"]) == "uncertain" {
		return false
	}
	if factDedupString(claim["scope_type"]) != scopeType || factDedupString(claim["scope_id"]) != scopeID {
		return false
	}
	now := factDedupNow()
	if validFrom := factDedupFloat(claim["valid_from"]); validFrom != nil && *validFrom > now {
		return false
	}
	if validTo := factDedupFloat(claim["valid_to"]); validTo != nil && *validTo <= now {
		return false
	}
	return true
}

// factDedupClassificationValue 读取分类字段，stability 与其它默认值不同
func factDedupClassificationValue(classification *FactDedupClassification, authority bool) string {
	if authority {
		if classification != nil && classification.Authority != nil {
			return *classification.Authority
		}
		return "direct_user"
	}
	if classification != nil && classification.Stability != nil {
		return *classification.Stability
	}
	return "stable"
}

// factDedupClassificationSection 读取画像段落默认值
func factDedupClassificationSection(classification *FactDedupClassification) string {
	if classification != nil && classification.ProfileSection != nil {
		return *classification.ProfileSection
	}
	return "stable_facts"
}

// factDedupFactHash 对齐 compute_hash(normalized_fact(text))
func factDedupFactHash(text string) string {
	hashValue, _ := ComputeHash(NormalizedFact(text))
	return hashValue
}
