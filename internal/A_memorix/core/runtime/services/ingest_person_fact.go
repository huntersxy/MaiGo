package services

// 人物事实语义去重写入，对齐 Python MemoryIngestService._ingest_person_fact

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// ingestFactDedupStore 把 storage.Store 适配为 FactDedupStore
type ingestFactDedupStore struct {
	store *storage.Store
}

func (a ingestFactDedupStore) GetDBPath() (string, error) {
	return a.store.DBPath()
}

func (a ingestFactDedupStore) Query(sql string, params ...any) ([]map[string]any, error) {
	return a.store.Query(sql, params...)
}

func (a ingestFactDedupStore) GetParagraph(hashValue string) (map[string]any, error) {
	return a.store.GetParagraph(hashValue)
}

func (a ingestFactDedupStore) GetFactClaim(claimID string) (map[string]any, error) {
	return a.store.GetFactClaim(claimID)
}

func (a ingestFactDedupStore) GetFactEvidence(claimID string) ([]map[string]any, error) {
	return a.store.GetFactEvidence(claimID)
}

func (a ingestFactDedupStore) UpsertFactClaim(in utils.FactClaimUpsert) (string, error) {
	row, err := a.store.UpsertFactClaim(storage.FactClaimUpsert{
		ScopeType: in.ScopeType, ScopeID: in.ScopeID, FactKey: in.FactKey, ValueText: in.ValueText,
		Polarity: in.Polarity, Cardinality: in.Cardinality, Stability: in.Stability, ProfileSection: in.ProfileSection,
		Authority: in.Authority, Confidence: in.Confidence, ValidFrom: in.ValidFrom, ValidTo: in.ValidTo,
		EvidenceType: in.EvidenceType, EvidenceID: in.EvidenceID, EvidenceStance: in.EvidenceStance,
		EvidenceWeight: in.EvidenceWeight, EvidenceMetadata: in.EvidenceMetadata,
		SupersedesClaimIDs: in.SupersedesClaimIDs, Reason: in.Reason, ObservedAt: in.ObservedAt,
	})
	if err != nil {
		return "", err
	}
	return asString(row["claim_id"]), nil
}

func (a ingestFactDedupStore) AddFactEvidence(claimID string, in utils.FactEvidenceUpsert) (utils.FactEvidenceResult, error) {
	row, err := a.store.AddFactEvidence(claimID, storage.FactEvidenceUpsert{
		EvidenceType: in.EvidenceType, EvidenceID: in.EvidenceID, Stance: in.Stance,
		Weight: in.Weight, Metadata: in.Metadata, Reason: in.Reason, ObservedAt: in.ObservedAt,
	})
	if err != nil {
		return utils.FactEvidenceResult{}, err
	}
	return utils.FactEvidenceResult{
		Created:    pyTruthy(row["created"]),
		ObservedAt: optionalFloat(row["observed_at"]),
	}, nil
}

func (a ingestFactDedupStore) Transaction(immediate bool, fn func(tx utils.FactDedupTx) error) error {
	return a.store.Transaction(immediate, func(tx *storage.Tx) error {
		return fn(ingestFactDedupTx{tx: tx})
	})
}

type ingestFactDedupTx struct {
	tx *storage.Tx
}

func (a ingestFactDedupTx) Execute(sql string, args ...any) error {
	_, err := a.tx.Exec(sql, args...)
	return err
}

// ingestPersonFact 对齐 _ingest_person_fact
func (s *IngestService) ingestPersonFact(
	ctx context.Context,
	params IngestTextParams,
	metadata map[string]any,
	personID string,
	dedup *utils.FactDeduplicator,
) (map[string]any, error) {
	store := s.store()
	if store == nil {
		return nil, fmt.Errorf("元数据存储未初始化")
	}
	content := utils.NormalizeText(params.Text)
	if content == "" {
		return map[string]any{"success": true, "stored_ids": []string{}, "reason": "empty_text"}, nil
	}
	messageIDs := make([]string, 0)
	seen := map[string]struct{}{}
	for _, item := range asList(metadata["evidence_message_ids"]) {
		token := strings.TrimSpace(asString(item))
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		messageIDs = append(messageIDs, token)
	}
	sort.Strings(messageIDs)
	source := map[string]any{
		"platform":    asString(metadata["platform"]),
		"chat_id":     params.ChatID,
		"message_ids": messageIDs,
		"observed_at": params.Timestamp,
	}
	obsID, err := utils.ObservationID(asString(source["platform"]), asString(source["chat_id"]), messageIDs, "")
	if err != nil {
		return nil, err
	}
	source["observation_id"] = obsID
	externalID := strings.TrimSpace(params.ExternalID) + ":" + obsID

	var plan *utils.FactDedupPlan
	var paragraphHash string
	var saved map[string]any
	receipt, err := store.GetExternalMemoryRef(externalID)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		saved = cloneAnyMap(asMap(receipt["metadata"]))
		if asString(saved["input_text"]) != content || asString(saved["person_id"]) != personID {
			return nil, fmt.Errorf("人物事实来源标识与已保存的输入不一致")
		}
		if asString(saved["dedup_phase"]) == "complete" {
			paragraph, err := store.GetParagraph(asString(receipt["paragraph_hash"]))
			if err != nil {
				return nil, err
			}
			if paragraph == nil || pyTruthy(paragraph["is_deleted"]) {
				return nil, fmt.Errorf("人物事实来源已处理，但关联段落已删除")
			}
			claimIDs := make([]string, 0)
			for _, item := range asList(saved["claim_ids"]) {
				if token := strings.TrimSpace(asString(item)); token != "" {
					claimIDs = append(claimIDs, token)
				}
			}
			return map[string]any{
				"success": true, "stored_ids": []string{}, "skipped_ids": []string{asString(receipt["paragraph_hash"])},
				"fact_claim_ids": claimIDs, "detail": "dedup_replayed",
			}, nil
		}
		plan, err = factDedupPlanFromAny(saved["dedup_plan"])
		if err != nil {
			return nil, err
		}
		paragraphHash = asString(receipt["paragraph_hash"])
	} else {
		plan, err = dedup.Plan(ctx, "person", personID, []utils.FactDedupFact{{
			FactID: "f1", Text: content,
		}}, source, nil)
		if err != nil {
			return nil, err
		}
		if len(plan.Decisions) == 0 {
			return nil, fmt.Errorf("人物事实去重计划缺少决定")
		}
		decision := plan.Decisions[0]
		if decision.Action == "reuse" && decision.Target != nil {
			paragraphHash = strings.TrimSpace(decision.Target.ParagraphHash)
		} else {
			paragraphMeta := cloneAnyMap(metadata)
			paragraphMeta["dedup_phase"] = "pending"
			paragraphMeta["chat_id"] = params.ChatID
			paragraphMeta["person_ids"] = []string{personID}
			paragraphHash, err = store.AddParagraph(
				content, nil, "person_fact:"+personID, paragraphMeta, "factual", nil,
			)
			if err != nil {
				return nil, err
			}
		}
		saved = map[string]any{
			"person_id": personID, "chat_id": params.ChatID, "input_text": content,
			"dedup_phase": "pending", "dedup_plan": plan,
		}
		if _, err := store.UpsertExternalMemoryRef(externalID, paragraphHash, "person_fact", saved); err != nil {
			return nil, err
		}
	}
	if plan == nil || len(plan.Decisions) == 0 {
		return nil, fmt.Errorf("人物事实去重计划缺少决定")
	}
	reused := plan.Decisions[0].Action == "reuse"
	if !reused {
		rawMeta := cloneAnyMap(metadata)
		rawMeta["dedup_managed"] = true
		rawMeta["dedup_phase"] = "pending"
		rawMeta["dedup_fact_texts"] = []string{content}
		rawMeta["dedup_receipt"] = saved
		rawParams := params
		rawParams.ExternalID = externalID
		rawParams.Metadata = rawMeta
		if _, err := s.ingestTextRaw(ctx, rawParams, rawMeta); err != nil {
			return nil, err
		}
	}
	classification := factDedupClassificationFromAny(metadata["fact_claim"])
	claimIDs, err := dedup.Commit(ctx, plan, paragraphHash, classification)
	if err != nil {
		return nil, err
	}
	if !reused {
		if _, err := store.UpdateParagraphMetadata(paragraphHash, map[string]any{
			"fact_claim_ids": claimIDs, "dedup_phase": "complete",
		}, true); err != nil {
			return nil, err
		}
		store.EnqueueEpisodeSourceRebuild("person_fact:"+personID, "dedup_person_fact_complete", storage.EpisodeEnqueueOptions{})
	}
	s.persist()
	completeMeta := cloneAnyMap(saved)
	completeMeta["dedup_phase"] = "complete"
	completeMeta["claim_ids"] = claimIDs
	if _, err := store.UpsertExternalMemoryRef(externalID, paragraphHash, "person_fact", completeMeta); err != nil {
		return nil, err
	}
	if s.deps.EnqueuePersonProfileRefresh != nil {
		s.deps.EnqueuePersonProfileRefresh(personID, "person_fact_deduplicated")
	}
	storedIDs := []string{}
	skippedIDs := []string{}
	detail := "fact_added"
	if reused {
		skippedIDs = []string{paragraphHash}
		detail = "fact_reused"
	} else {
		storedIDs = []string{paragraphHash}
	}
	return map[string]any{
		"success": true, "stored_ids": storedIDs, "skipped_ids": skippedIDs,
		"fact_claim_ids": claimIDs, "detail": detail,
	}, nil
}

func factDedupPlanFromAny(value any) (*utils.FactDedupPlan, error) {
	if value == nil {
		return nil, fmt.Errorf("人物事实去重计划缺失")
	}
	if typed, ok := value.(*utils.FactDedupPlan); ok {
		return typed, nil
	}
	if typed, ok := value.(utils.FactDedupPlan); ok {
		copy := typed
		return &copy, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var plan utils.FactDedupPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

func factDedupClassificationFromAny(value any) *utils.FactDedupClassification {
	claim := asMap(value)
	if claim == nil {
		return nil
	}
	out := &utils.FactDedupClassification{}
	if text := strings.TrimSpace(asString(claim["authority"])); text != "" {
		out.Authority = &text
	}
	if text := strings.TrimSpace(asString(claim["stability"])); text != "" {
		out.Stability = &text
	}
	if text := strings.TrimSpace(asString(claim["profile_section"])); text != "" {
		out.ProfileSection = &text
	}
	if out.Authority == nil && out.Stability == nil && out.ProfileSection == nil {
		return nil
	}
	return out
}
