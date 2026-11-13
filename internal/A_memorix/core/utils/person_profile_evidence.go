package utils

// A_memorix 人物画像证据收集，对齐 _collect_relation_evidence 与 _collect_person_fact_evidence

import (
	"fmt"
	"sort"
	"strings"
)

// CollectRelationEvidence 对齐 _collect_relation_evidence
func (s *PersonProfileService) CollectRelationEvidence(aliases []string, limit int, personID string) ([]map[string]any, error) {
	if s.store == nil {
		return []map[string]any{}, nil
	}
	relationByHash := map[string]map[string]any{}
	order := make([]string, 0, len(aliases)*2)
	collect := func(rows []map[string]any) {
		for _, rel := range rows {
			hashValue := personProfileMapText(rel, "hash")
			if hashValue == "" {
				continue
			}
			if _, exists := relationByHash[hashValue]; !exists {
				order = append(order, hashValue)
			}
			relationByHash[hashValue] = rel
		}
	}
	for _, alias := range aliases {
		subjectRows, err := s.store.GetRelations(alias, "", false)
		if err != nil {
			return nil, err
		}
		collect(subjectRows)
		objectRows, err := s.store.GetRelations("", alias, false)
		if err != nil {
			return nil, err
		}
		collect(objectRows)
	}

	relations := make([]map[string]any, 0, len(order))
	for _, hashValue := range order {
		relations = append(relations, relationByHash[hashValue])
	}
	if personID != "" {
		filtered := make([]map[string]any, 0, len(relations))
		for _, rel := range relations {
			if s.isRelationBoundToPerson(rel, personID) {
				filtered = append(filtered, rel)
			}
		}
		relations = filtered
	}
	sort.SliceStable(relations, func(left int, right int) bool {
		return personProfileConfidenceSortKey(relations[left]) > personProfileConfidenceSortKey(relations[right])
	})
	limit = maxIntValue(1, limit)
	if len(relations) > limit {
		relations = relations[:limit]
	}

	supporting := map[string][]map[string]any{}
	if len(relations) > 0 {
		hashes := make([]string, 0, len(relations))
		for _, rel := range relations {
			hashes = append(hashes, pyStr(rel["hash"]))
		}
		rows, err := s.store.GetParagraphsByRelationHashes(hashes)
		if err != nil {
			return nil, err
		}
		if rows != nil {
			supporting = rows
		}
	}

	edges := make([]map[string]any, 0, len(relations))
	for _, rel := range relations {
		hashValue := personProfileMapText(rel, "hash")
		paragraphs := supporting[hashValue]
		if len(paragraphs) > 0 {
			supported := false
			supporter, ok := s.store.(PersonProfileFactSupport)
			if ok {
				for _, row := range paragraphs {
					hasSupport, supportErr := supporter.FactRelationHasSupport(hashValue, row)
					if supportErr != nil {
						return nil, supportErr
					}
					if hasSupport {
						supported = true
						break
					}
				}
			} else {
				// 未注入支撑校验时保留关系，避免证据被静默丢弃
				supported = true
			}
			if !supported {
				continue
			}
		}
		edges = append(edges, map[string]any{
			"hash":       hashValue,
			"subject":    personProfileMapText(rel, "subject"),
			"predicate":  personProfileMapText(rel, "predicate"),
			"object":     personProfileMapText(rel, "object"),
			"confidence": personProfileConfidence(rel),
		})
	}
	return edges, nil
}

// personProfileConfidenceSortKey 对齐排序用的 float(rel.get("confidence", 0.0))
func personProfileConfidenceSortKey(rel map[string]any) float64 {
	value, ok := rel["confidence"]
	if !ok || value == nil {
		return 0
	}
	parsed, err := pyFloat64(value)
	if err != nil {
		return 0
	}
	return parsed
}

// personProfileConfidence 对齐 float(rel.get("confidence", 1.0) or 1.0)
func personProfileConfidence(rel map[string]any) float64 {
	value, ok := rel["confidence"]
	if !ok || value == nil {
		return 1.0
	}
	parsed, err := pyFloat64(value)
	if err != nil || parsed == 0 {
		return 1.0
	}
	return parsed
}

// isRelationBoundToPerson 对齐 _is_relation_bound_to_person
func (s *PersonProfileService) isRelationBoundToPerson(relation map[string]any, personID string) bool {
	pid := pyStrip(personID)
	if pid == "" {
		return false
	}
	metadata := CoerceMetadataDict(relation["metadata"])
	if pyStrip(personProfileMapText(metadata, "person_id")) == pid {
		return true
	}
	for _, token := range personProfileListTokens(metadata["person_ids"]) {
		if token == pid {
			return true
		}
	}
	sourceParagraph := pyStrip(personProfileMapText(relation, "source_paragraph"))
	if sourceParagraph != "" && s.store != nil {
		paragraph, err := s.store.GetParagraph(sourceParagraph)
		if err != nil {
			paragraph = nil
		}
		if paragraph != nil {
			payload := map[string]any{
				"hash":     sourceParagraph,
				"source":   personProfileMapText(paragraph, "source"),
				"metadata": CoerceMetadataDict(paragraph["metadata"]),
			}
			return s.isEvidenceBoundToPerson(payload, pid)
		}
	}
	return false
}

// personProfileListTokens 对齐 _list_tokens
func personProfileListTokens(value any) []string {
	switch typed := value.(type) {
	case nil:
		return []string{}
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if token := pyStrip(pyStr(item)); token != "" {
				result = append(result, token)
			}
		}
		return result
	case []string:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if token := pyStrip(item); token != "" {
				result = append(result, token)
			}
		}
		return result
	default:
		if token := pyStrip(pyStr(value)); token != "" {
			return []string{token}
		}
		return []string{}
	}
}

// collectPersonFactEvidence 对齐 _collect_person_fact_evidence
func (s *PersonProfileService) collectPersonFactEvidence(personID string, limit int) ([]map[string]any, error) {
	token := pyStrip(personID)
	if token == "" || s.store == nil {
		return []map[string]any{}, nil
	}
	source := "person_fact:" + token
	rows, err := s.store.GetParagraphsBySource(source)
	if err != nil {
		return nil, err
	}
	paragraphs := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if isTruthy(row["is_deleted"]) {
			continue
		}
		if pyStr(CoerceMetadataDict(row["metadata"])["dedup_phase"]) == "pending" {
			continue
		}
		paragraph := row
		if projector, ok := s.store.(PersonProfileFactProjector); ok {
			projected, projectErr := projector.ProjectFactParagraph(row)
			if projectErr != nil {
				return nil, projectErr
			}
			if projected == nil {
				continue
			}
			paragraph = projected
		}
		paragraphs = append(paragraphs, paragraph)
	}
	sort.SliceStable(paragraphs, func(left int, right int) bool {
		return personProfileUpdatedAt(paragraphs[left]) > personProfileUpdatedAt(paragraphs[right])
	})
	if limit < 1 {
		limit = 1
	}
	if len(paragraphs) > limit {
		paragraphs = paragraphs[:limit]
	}

	evidence := make([]map[string]any, 0, len(paragraphs))
	for _, row := range paragraphs {
		paragraphHash := personProfileMapText(row, "hash")
		content := pyStrip(personProfileMapText(row, "content"))
		if paragraphHash == "" || content == "" {
			continue
		}
		rowSource := personProfileMapText(row, "source")
		if rowSource == "" {
			rowSource = source
		}
		evidence = append(evidence, map[string]any{
			"hash":     paragraphHash,
			"type":     "paragraph",
			"score":    1.1,
			"content":  personProfileTruncateRunes(content, 220),
			"source":   rowSource,
			"metadata": CoerceMetadataDict(row["metadata"]),
		})
	}
	return s.filterStaleParagraphEvidence(evidence)
}

// personProfileUpdatedAt 对齐 float(item.get("updated_at") or item.get("created_at") or 0.0)
func personProfileUpdatedAt(row map[string]any) float64 {
	for _, key := range []string{"updated_at", "created_at"} {
		value, ok := row[key]
		if !ok || !isTruthy(value) {
			continue
		}
		if parsed, err := pyFloat64(value); err == nil {
			return parsed
		}
	}
	return 0
}

// collectPersonFactClaims 对齐 _collect_person_fact_claims
func (s *PersonProfileService) collectPersonFactClaims(personID string, limit int) ([]map[string]any, error) {
	if s.store == nil {
		return []map[string]any{}, nil
	}
	effectiveAt := s.nowFunc()
	claims, err := s.store.ListPersonProfileFactClaims(personID, &effectiveAt, maxIntValue(1, limit))
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// mergeFactClaimBuckets 对齐 _merge_fact_claim_buckets
// 将账本事实置于分类证据之前，保证 top-k 抖动不能挤出稳定事实
func (s *PersonProfileService) mergeFactClaimBuckets(classifiedBuckets map[string][]string, factClaims []map[string]any) (map[string][]string, error) {
	merged := make(map[string][]string, len(classifiedBuckets))
	for key := range classifiedBuckets {
		merged[key] = []string{}
	}
	for _, claim := range factClaims {
		section := pyStrip(personProfileMapText(claim, "profile_section"))
		if section == "" {
			section = "stable_facts"
		}
		if _, exists := merged[section]; !exists {
			return nil, fmt.Errorf("事实 claim 使用了未知画像段落: %s", section)
		}
		text := pyStrip(personProfileMapText(claim, "value_text"))
		if text != "" {
			appendPersonProfileBucket(merged, section, text)
		}
	}
	for section, values := range classifiedBuckets {
		for _, value := range values {
			appendPersonProfileBucket(merged, section, value)
		}
	}
	return merged, nil
}

// appendPersonProfileBucket 对齐 _append_profile_bucket
func appendPersonProfileBucket(buckets map[string][]string, bucket string, text string) {
	clean := personProfileCleanBucketText(text)
	if clean == "" {
		return
	}
	values := buckets[bucket]
	for _, existing := range values {
		if existing == clean {
			return
		}
	}
	buckets[bucket] = append(values, clean)
}

// personProfileCleanBucketText 对齐 str(text or "").strip().strip("- ")
func personProfileCleanBucketText(text string) string {
	trimmed := pyStrip(text)
	return strings.Trim(trimmed, "- ")
}

// isEvidenceBoundToPerson 对齐 _is_evidence_bound_to_person
// 画像证据必须显式绑定到 person_id，避免别名全局召回串人
func (s *PersonProfileService) isEvidenceBoundToPerson(item map[string]any, personID string) bool {
	pid := pyStrip(personID)
	if pid == "" {
		return false
	}
	metadata := CoerceMetadataDict(item["metadata"])
	source := pyStr(item["source"])
	if pyStrip(source) == "" {
		source = pyStr(metadata["source"])
	}
	if pyStrip(source) == "person_fact:"+pid {
		return true
	}
	if pyStrip(personProfileMapText(metadata, "person_id")) == pid {
		return true
	}
	for _, token := range personProfileListTokens(metadata["person_ids"]) {
		if token == pid {
			return true
		}
	}
	return false
}
