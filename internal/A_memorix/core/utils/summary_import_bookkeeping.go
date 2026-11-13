package utils

// A_memorix 摘要去重完成后的段落元数据、外部引用与重建入队

import (
	"fmt"
	"sort"
	"strconv"
)

// completeDedupBookkeeping 对齐 _finish_fact_summary 的段落元数据与外部引用收尾
func (i *SummaryImporter) completeDedupBookkeeping(
	metadata map[string]any,
	payload summaryImportPayload,
	plan *FactDedupPlan,
	kept []summaryPayloadFact,
	claimIDs []string,
	streamID string,
	externalID string,
	paragraphHash string,
	profileEnabled bool,
) error {
	claimMap := make(map[string]string, len(payload.Plan.Decisions))
	for index, decision := range payload.Plan.Decisions {
		if index < len(claimIDs) {
			claimMap[decision.FactID] = claimIDs[index]
		}
	}
	keptIDs := make(map[string]struct{}, len(kept))
	factClaimIDs := make([]string, 0, len(kept))
	for _, fact := range kept {
		keptIDs[fact.FactID] = struct{}{}
		factClaimIDs = append(factClaimIDs, claimMap[fact.FactID])
	}
	relationClaims := make(map[string]any, len(payload.Relations))
	for index, relation := range payload.Relations {
		claimIDList := make([]string, 0)
		for _, factID := range plan.RelationSupport[strconv.Itoa(index)] {
			if _, exists := keptIDs[factID]; !exists {
				continue
			}
			claimIDList = append(claimIDList, claimMap[factID])
		}
		relationHash, err := i.store.ComputeRelationHash(relation["subject"], relation["predicate"], relation["object"])
		if err != nil {
			return err
		}
		relationClaims[relationHash] = claimIDList
	}
	if _, err := i.store.UpdateParagraphMetadata(paragraphHash, map[string]any{
		"fact_claim_ids":        factClaimIDs,
		"dedup_phase":           "complete",
		"dedup_relation_claims": relationClaims,
	}, true); err != nil {
		return err
	}
	personIDs := summarySortedPersonIDs(summaryKeptPersonIDs(kept))
	triggerCount := summaryIntValue(metadata["trigger_message_count"], 0)
	if externalID != "" {
		if _, err := i.store.UpsertExternalMemoryRef(externalID, paragraphHash, "chat_summary", map[string]any{
			"chat_id":               streamID,
			"person_ids":            personIDs,
			"dedup_phase":           "complete",
			"trigger_message_count": triggerCount,
		}); err != nil {
			return err
		}
	}
	if paragraphHash != "" {
		i.store.EnqueueEpisodeSourceRebuild("chat_summary:"+streamID, "dedup_summary_complete")
	}
	if profileEnabled {
		for _, personID := range personIDs {
			if err := i.store.EnqueuePersonProfileRefresh(personID, "dedup_summary_complete"); err != nil {
				return fmt.Errorf("人物画像刷新入队失败: %w", err)
			}
		}
	}
	return nil
}

// summaryKeptPersonIDs 收集保留事实的人物标识
func summaryKeptPersonIDs(kept []summaryPayloadFact) []string {
	values := make([]string, 0, len(kept))
	for _, fact := range kept {
		for _, personID := range fact.PersonIDs {
			if pyStrip(personID) == "" {
				continue
			}
			values = append(values, pyStrip(personID))
		}
	}
	sort.Strings(values)
	return values
}
