package utils

// A_memorix 事实去重候选召回与辅助判断，对齐 Python FactDeduplicator 的内部方法

import (
	"strings"
	"time"
)

// scopeParagraphs 对齐 _scope_paragraphs，筛选范围可检索段落
func (d *FactDeduplicator) scopeParagraphs(scopeType string, scopeID string) ([]string, error) {
	source := "chat_summary:" + scopeID
	if scopeType == "person" {
		source = "person_fact:" + scopeID
	}
	query := `SELECT p.hash FROM paragraphs p JOIN (SELECT hash FROM paragraphs WHERE source=? UNION ` +
		`SELECT e.evidence_id AS hash FROM fact_claims c JOIN fact_evidence e ON e.claim_id=c.claim_id ` +
		`WHERE c.scope_type=? AND c.scope_id=? AND e.evidence_type='paragraph' UNION ` +
		`SELECT r.paragraph_hash AS hash FROM external_memory_refs r WHERE ?='chat' ` +
		`AND r.source_type='chat_summary' AND json_extract(r.metadata_json,'$.chat_id')=? ` +
		`AND COALESCE(json_extract(r.metadata_json,'$.dedup_phase'),'complete')='complete') owned ON owned.hash=p.hash ` +
		`WHERE COALESCE(p.is_deleted,0)=0 ` +
		`AND COALESCE(json_extract(p.metadata, '$.dedup_phase'),'complete')='complete' ` +
		`AND (json_extract(p.metadata, '$.memory_change.valid_to') IS NULL ` +
		`OR json_extract(p.metadata, '$.memory_change.valid_to')>?)`
	rows, err := d.store.Query(query, source, scopeType, scopeID, scopeType, scopeID, factDedupNow())
	if err != nil {
		return nil, err
	}
	hashes := make([]string, 0, len(rows))
	for _, row := range rows {
		hashes = append(hashes, factDedupString(row["hash"]))
	}
	return hashes, nil
}

// candidates 对齐 _candidates，先账本精确命中再做关键词召回
func (d *FactDeduplicator) candidates(scopeType string, scopeID string, text string, paragraphIDs []string) ([]factDedupCandidate, error) {
	now := factDedupNow()
	rows, err := d.store.Query(
		`SELECT * FROM fact_claims WHERE scope_type=? AND scope_id=? AND status='active' AND stability<>'uncertain' `+
			`AND (valid_from IS NULL OR valid_from<=?) AND (valid_to IS NULL OR valid_to>?) `+
			`AND value_normalized=? LIMIT 8`,
		scopeType, scopeID, now, now, NormalizedFact(text),
	)
	if err != nil {
		return nil, err
	}
	if len(rows) < 8 {
		words := factDedupTermPattern.FindAllString(text, -1)
		terms := make([]string, 0, 8)
		seen := make(map[string]struct{}, len(words))
		for _, word := range words {
			if _, exists := seen[word]; exists {
				continue
			}
			seen[word] = struct{}{}
			terms = append(terms, word)
			if len(terms) == 8 {
				break
			}
		}
		if len(terms) > 0 {
			conditions := make([]string, len(terms))
			params := make([]any, 0, 4+len(terms))
			params = append(params, scopeType, scopeID, now, now)
			for index, term := range terms {
				conditions[index] = "value_text LIKE ?"
				params = append(params, "%"+term+"%")
			}
			keywordRows, err := d.store.Query(
				`SELECT * FROM fact_claims WHERE scope_type=? AND scope_id=? AND status='active' AND stability<>'uncertain' `+
					`AND (valid_from IS NULL OR valid_from<=?) AND (valid_to IS NULL OR valid_to>?) `+
					`AND (`+strings.Join(conditions, " OR ")+`) ORDER BY updated_at DESC LIMIT 8`,
				params...,
			)
			if err != nil {
				return nil, err
			}
			rows = append(rows, keywordRows...)
		}
	}
	paragraphSet := make(map[string]struct{}, len(paragraphIDs))
	for _, item := range paragraphIDs {
		paragraphSet[item] = struct{}{}
	}
	order := make([]string, 0, len(rows))
	byID := make(map[string]factDedupCandidate, len(rows))
	for _, row := range rows {
		claimID := factDedupString(row["claim_id"])
		evidence, err := d.store.GetFactEvidence(claimID)
		if err != nil {
			return nil, err
		}
		paragraphHash := ""
		for _, item := range evidence {
			if factDedupString(item["evidence_type"]) != "paragraph" {
				continue
			}
			evidenceID := factDedupString(item["evidence_id"])
			if _, ok := paragraphSet[evidenceID]; ok {
				paragraphHash = evidenceID
				break
			}
		}
		if paragraphHash == "" {
			continue
		}
		personIDs, err := d.claimPeople(evidence, paragraphHash)
		if err != nil {
			return nil, err
		}
		candidate := factDedupCandidate{
			ID:            claimID,
			Text:          factDedupString(row["value_text"]),
			ParagraphHash: paragraphHash,
			ClaimID:       claimID,
			ValidFrom:     factDedupFloat(row["valid_from"]),
			ValidTo:       factDedupFloat(row["valid_to"]),
			PersonIDs:     personIDs,
		}
		for _, item := range evidence {
			if factDedupString(item["evidence_type"]) != "source" {
				continue
			}
			candidate.Observations = append(candidate.Observations, factDedupMetadata(item["metadata"]))
			if len(candidate.Observations) == 2 {
				break
			}
		}
		if _, exists := byID[claimID]; !exists {
			order = append(order, claimID)
		}
		byID[claimID] = candidate
	}
	if len(order) > 8 {
		order = order[:8]
	}
	out := make([]factDedupCandidate, 0, len(order))
	for _, claimID := range order {
		out = append(out, byID[claimID])
	}
	return out, nil
}

// paragraphCandidates 对齐 _paragraph_candidates，从向量命中段落召回具体事实
func (d *FactDeduplicator) paragraphCandidates(ids []string, scopeType string, scopeID string) ([]factDedupCandidate, error) {
	out := make([]factDedupCandidate, 0, len(ids))
	for _, paragraphID := range ids {
		paragraph, err := d.store.GetParagraph(paragraphID)
		if err != nil {
			return nil, err
		}
		if paragraph == nil || factDedupTruthy(paragraph["is_deleted"]) {
			continue
		}
		claims, err := d.store.Query(
			`SELECT c.* FROM fact_claims c JOIN fact_evidence e ON e.claim_id=c.claim_id `+
				`WHERE e.evidence_type='paragraph' AND e.evidence_id=? AND c.scope_type=? AND c.scope_id=?`,
			paragraphID, scopeType, scopeID,
		)
		if err != nil {
			return nil, err
		}
		if len(claims) > 0 {
			for _, claim := range claims {
				if !factDedupClaimValid(claim) {
					continue
				}
				claimID := factDedupString(claim["claim_id"])
				evidence, err := d.store.GetFactEvidence(claimID)
				if err != nil {
					return nil, err
				}
				personIDs, err := d.claimPeople(evidence, paragraphID)
				if err != nil {
					return nil, err
				}
				candidate := factDedupCandidate{
					ID:            claimID,
					Text:          factDedupString(claim["value_text"]),
					ClaimID:       claimID,
					ParagraphHash: paragraphID,
					ValidFrom:     factDedupFloat(claim["valid_from"]),
					ValidTo:       factDedupFloat(claim["valid_to"]),
					PersonIDs:     personIDs,
				}
				for _, item := range evidence {
					if factDedupString(item["evidence_type"]) != "source" {
						continue
					}
					candidate.Observations = append(candidate.Observations, factDedupMetadata(item["metadata"]))
					if len(candidate.Observations) == 2 {
						break
					}
				}
				out = append(out, candidate)
			}
			continue
		}
		personIDs, err := d.claimPeople(nil, paragraphID)
		if err != nil {
			return nil, err
		}
		out = append(out, factDedupCandidate{
			ID:             "paragraph:" + paragraphID,
			Text:           factDedupString(paragraph["content"]),
			ParagraphHash:  paragraphID,
			PersonIDs:      personIDs,
			EventTimeStart: factDedupFloat(paragraph["event_time_start"]),
			EventTimeEnd:   factDedupFloat(paragraph["event_time_end"]),
			ObservedAt:     factDedupFloat(paragraph["created_at"]),
		})
	}
	return out, nil
}

// claimPeople 对齐 _claim_people，人员列表只有在能唯一指向一个人时才算依据
func (d *FactDeduplicator) claimPeople(evidence []map[string]any, paragraphHash string) ([]string, error) {
	for _, row := range evidence {
		if factDedupString(row["evidence_type"]) != "paragraph" || factDedupString(row["evidence_id"]) != paragraphHash {
			continue
		}
		metadata := factDedupMetadata(row["metadata"])
		if metadata != nil {
			if value, exists := metadata["person_ids"]; exists {
				return factDedupStringList(value), nil
			}
		}
	}
	paragraph, err := d.store.GetParagraph(paragraphHash)
	if err != nil {
		return nil, err
	}
	var people []string
	if paragraph != nil {
		metadata := factDedupMetadata(paragraph["metadata"])
		if metadata != nil {
			people = factDedupStringList(metadata["person_ids"])
		}
	}
	if len(people) == 1 {
		return people, nil
	}
	return nil, nil
}

// factDedupCompatibleCandidates 对齐 _compatible_candidates，聊天范围按人员集合过滤
func factDedupCompatibleCandidates(scopeType string, fact FactDedupFact, rows []factDedupCandidate) []factDedupCandidate {
	if scopeType != "chat" || len(fact.PersonIDs) == 0 {
		return rows
	}
	out := make([]factDedupCandidate, 0, len(rows))
	for _, row := range rows {
		if factDedupSetEqual(fact.PersonIDs, row.PersonIDs) {
			out = append(out, row)
		}
	}
	return out
}

// factDedupFindExact 查找规范化后文本完全相同的候选
func factDedupFindExact(rows []factDedupCandidate, text string) *factDedupCandidate {
	normalized := NormalizedFact(text)
	for index := range rows {
		if NormalizedFact(rows[index].Text) == normalized {
			return &rows[index]
		}
	}
	return nil
}

// factDedupInterleave 对齐 zip_longest 交错合并
func factDedupInterleave(found []factDedupCandidate, inputs []factDedupCandidate) []factDedupCandidate {
	total := len(found)
	if len(inputs) > total {
		total = len(inputs)
	}
	out := make([]factDedupCandidate, 0, len(found)+len(inputs))
	for index := 0; index < total; index++ {
		if index < len(found) {
			out = append(out, found[index])
		}
		if index < len(inputs) {
			out = append(out, inputs[index])
		}
	}
	return out
}

// factDedupUnique 按 id 去重，重复时保留首次位置并覆盖内容
func factDedupUnique(rows []factDedupCandidate) []factDedupCandidate {
	order := make([]string, 0, len(rows))
	byID := make(map[string]factDedupCandidate, len(rows))
	for _, row := range rows {
		if _, exists := byID[row.ID]; !exists {
			order = append(order, row.ID)
		}
		byID[row.ID] = row
	}
	out := make([]factDedupCandidate, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// factDedupClaimValid 对齐段落候选中的 claim 有效性判断
func factDedupClaimValid(claim map[string]any) bool {
	now := factDedupNow()
	if factDedupString(claim["status"]) != "active" || factDedupString(claim["stability"]) == "uncertain" {
		return false
	}
	if validTo := factDedupFloat(claim["valid_to"]); validTo != nil && *validTo <= now {
		return false
	}
	if validFrom := factDedupFloat(claim["valid_from"]); validFrom != nil && *validFrom > now {
		return false
	}
	return true
}

// factDedupTargetFromCandidate 把内部候选转换为计划里的目标对象
func factDedupTargetFromCandidate(row factDedupCandidate) FactDedupTarget {
	observations := make([]map[string]any, 0, len(row.Observations))
	for _, observation := range row.Observations {
		observations = append(observations, observation)
	}
	personIDs := row.PersonIDs
	if personIDs == nil {
		personIDs = []string{}
	}
	return FactDedupTarget{
		ID:             row.ID,
		Text:           row.Text,
		ClaimID:        row.ClaimID,
		ParagraphHash:  row.ParagraphHash,
		InputFactID:    row.InputFactID,
		ValidFrom:      row.ValidFrom,
		ValidTo:        row.ValidTo,
		EventTimeStart: row.EventTimeStart,
		EventTimeEnd:   row.EventTimeEnd,
		ObservedAt:     row.ObservedAt,
		PersonIDs:      personIDs,
		Observations:   observations,
	}
}

// factDedupModelCandidate 构建提示词中的候选对象
func factDedupModelCandidate(row factDedupCandidate, aliases map[string]string) map[string]any {
	item := map[string]any{"id": aliases[row.ID], "text": row.Text}
	if row.ObservedAt != nil {
		item["observed_at"] = *row.ObservedAt
	}
	if row.EventTimeStart != nil {
		item["event_time_start"] = *row.EventTimeStart
	}
	if row.EventTimeEnd != nil {
		item["event_time_end"] = *row.EventTimeEnd
	}
	if row.ValidFrom != nil {
		item["valid_from"] = *row.ValidFrom
	}
	if row.ValidTo != nil {
		item["valid_to"] = *row.ValidTo
	}
	observations := make([]any, 0, len(row.Observations))
	for _, observation := range row.Observations {
		filtered := make(map[string]any)
		for _, key := range factDedupTemporalKeys {
			if value, ok := observation[key]; ok && value != nil {
				filtered[key] = value
			}
		}
		observations = append(observations, filtered)
	}
	item["observations"] = observations
	return item
}

// factDedupNow 对齐 time.time() 的秒级浮点
func factDedupNow() float64 {
	return float64(time.Now().UnixNano()) / float64(time.Second)
}
