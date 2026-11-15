package utils

// A_memorix 人物画像证据富化与失效过滤，对齐 _enrich_paragraph_evidence_metadata 与 _filter_stale_paragraph_evidence

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// sourceTypeFromSource 对齐 _source_type_from_source
func sourceTypeFromSource(source string) string {
	token := pyStrip(source)
	if strings.HasPrefix(token, "chat_summary:") {
		return "chat_summary"
	}
	if strings.HasPrefix(token, "person_fact:") {
		return "person_fact"
	}
	return ""
}

// enrichParagraphEvidenceMetadata 对齐 _enrich_paragraph_evidence_metadata
func (s *PersonProfileService) enrichParagraphEvidenceMetadata(paragraphHash string, metadata map[string]any) (map[string]any, string) {
	merged := CoerceMetadataDict(metadata)
	source := pyStrip(personProfileMapText(merged, "source"))
	var paragraph map[string]any
	if s.store != nil {
		value, err := s.store.GetParagraph(paragraphHash)
		if err == nil {
			paragraph = value
		}
	}
	if paragraph != nil {
		paragraphMetadata := CoerceMetadataDict(paragraph["metadata"])
		if len(paragraphMetadata) > 0 {
			combined := make(map[string]any, len(paragraphMetadata)+len(merged))
			for key, value := range paragraphMetadata {
				combined[key] = value
			}
			for key, value := range merged {
				combined[key] = value
			}
			merged = combined
		}
		if source == "" {
			source = pyStrip(personProfileMapText(paragraph, "source"))
		}
	}
	sourceType := pyStrip(personProfileMapText(merged, "source_type"))
	if sourceType == "" {
		sourceType = sourceTypeFromSource(source)
	}
	if sourceType != "" {
		merged["source_type"] = sourceType
	}
	if source != "" {
		merged["source"] = source
	}
	return merged, source
}

// isChatSummaryEvidence 对齐 _is_chat_summary_evidence
func isChatSummaryEvidence(item map[string]any) bool {
	metadata := CoerceMetadataDict(item["metadata"])
	sourceType := pyStrip(personProfileMapText(metadata, "source_type"))
	source := personProfileMapText(item, "source")
	if pyStrip(source) == "" {
		source = personProfileMapText(metadata, "source")
	}
	return sourceType == "chat_summary" || strings.HasPrefix(pyStrip(source), "chat_summary:")
}

// filterStaleParagraphEvidence 对齐 _filter_stale_paragraph_evidence
func (s *PersonProfileService) filterStaleParagraphEvidence(evidence []map[string]any) ([]map[string]any, error) {
	projected := make([]map[string]any, 0, len(evidence))
	for _, item := range evidence {
		itemMetadata := CoerceMetadataDict(item["metadata"])
		typeText := pyStr(item["type"])
		hashValue := pyStr(item["hash"])
		dedupManaged := isTruthy(itemMetadata["dedup_managed"]) || pyStr(itemMetadata["dedup_phase"]) == "pending"
		if typeText == "paragraph" && hashValue != "" && dedupManaged && s.store != nil {
			paragraph, err := s.store.GetParagraph(hashValue)
			if err != nil {
				return nil, err
			}
			if paragraph != nil {
				paragraphMetadata := CoerceMetadataDict(paragraph["metadata"])
				if isTruthy(paragraphMetadata["dedup_managed"]) || pyStr(paragraphMetadata["dedup_phase"]) == "pending" {
					projector, ok := s.store.(PersonProfileFactProjector)
					if ok {
						value, projectErr := projector.ProjectFactParagraph(paragraph)
						if projectErr != nil {
							return nil, projectErr
						}
						if value == nil {
							continue
						}
						// 投影只删除失效事实，不能扩大调用方已经截断的证据预算
						content := pyStr(value["content"])
						budget := len([]rune(pyStr(item["content"])))
						updated := make(map[string]any, len(item))
						for key, value := range item {
							updated[key] = value
						}
						updated["content"] = personProfileTruncateRunes(content, budget)
						item = updated
					}
				}
			}
		}
		if _, exists := item["metadata"]; exists {
			updated := make(map[string]any, len(item))
			for key, value := range item {
				updated[key] = value
			}
			updated["metadata"] = VisibleFactMetadata(CoerceMetadataDict(item["metadata"]))
			item = updated
		}
		projected = append(projected, item)
	}
	evidence = projected

	if !isTruthy(s.cfg("integration.feedback_correction_paragraph_hard_filter_enabled", true)) {
		return evidence, nil
	}
	paragraphHashes := make([]string, 0, len(evidence))
	for _, item := range evidence {
		if pyStr(item["type"]) == "paragraph" && pyStrip(pyStr(item["hash"])) != "" {
			paragraphHashes = append(paragraphHashes, pyStrip(pyStr(item["hash"])))
		}
	}
	if len(paragraphHashes) == 0 || s.store == nil {
		return evidence, nil
	}

	marksByParagraph, err := s.store.GetParagraphStaleRelationMarksBatch(paragraphHashes)
	if err != nil {
		return nil, err
	}
	relationHashes := make([]string, 0, len(marksByParagraph))
	seen := map[string]struct{}{}
	for _, marks := range marksByParagraph {
		for _, mark := range marks {
			relationHash := pyStrip(pyStr(mark["relation_hash"]))
			if relationHash == "" {
				continue
			}
			if _, exists := seen[relationHash]; exists {
				continue
			}
			seen[relationHash] = struct{}{}
			relationHashes = append(relationHashes, relationHash)
		}
	}
	statusMap := map[string]map[string]any{}
	if len(relationHashes) > 0 {
		statusMap, err = s.store.GetRelationStatusBatch(relationHashes)
		if err != nil {
			return nil, err
		}
	}

	filtered := make([]map[string]any, 0, len(evidence))
	for _, item := range evidence {
		itemType := pyStr(item["type"])
		itemHash := pyStr(item["hash"])
		if itemType != "paragraph" || itemHash == "" {
			filtered = append(filtered, item)
			continue
		}
		shouldHide := false
		for _, mark := range marksByParagraph[itemHash] {
			relationHash := pyStrip(pyStr(mark["relation_hash"]))
			if relationHash == "" {
				continue
			}
			status, exists := statusMap[relationHash]
			if !exists || isTruthy(status["is_inactive"]) {
				shouldHide = true
				break
			}
		}
		if shouldHide {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered, nil
}

// CollectVectorEvidence 对齐 _collect_vector_evidence
func (s *PersonProfileService) CollectVectorEvidence(ctx context.Context, aliases []string, topK int, personID string) ([]map[string]any, error) {
	aliasQueries := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		if alias != "" {
			aliasQueries = append(aliasQueries, alias)
		}
	}
	if len(aliasQueries) == 0 && personID == "" {
		return []map[string]any{}, nil
	}
	if s.store == nil {
		return []map[string]any{}, nil
	}

	if s.retriever == nil {
		return s.collectFallbackVectorEvidence(aliasQueries, topK, personID)
	}

	perAliasTopK := max(2, topK/max(1, len(aliasQueries)))
	seenHash := map[string]struct{}{}
	evidence := make([]map[string]any, 0, topK)
	factLimit := max(2, min(4, topK))
	factEvidence, err := s.collectPersonFactEvidence(personID, factLimit)
	if err != nil {
		return nil, err
	}
	for _, item := range factEvidence {
		hashValue := pyStr(item["hash"])
		if hashValue == "" {
			continue
		}
		if _, exists := seenHash[hashValue]; exists {
			continue
		}
		seenHash[hashValue] = struct{}{}
		evidence = append(evidence, item)
	}

	for _, alias := range aliasQueries {
		results, err := s.retriever.RetrievePersonProfileEvidence(ctx, alias, perAliasTopK)
		if err != nil {
			s.logger.Warning(fmt.Sprintf("向量证据召回失败: alias=%s, err=%v", alias, err))
			continue
		}
		for _, item := range results {
			hashValue := item.Hash
			if hashValue == "" {
				continue
			}
			if _, exists := seenHash[hashValue]; exists {
				continue
			}
			metadata, source := s.enrichParagraphEvidenceMetadata(hashValue, item.Metadata)
			payload := map[string]any{
				"hash":     hashValue,
				"type":     item.ResultType,
				"score":    item.Score,
				"content":  personProfileTruncateRunes(item.Content, 220),
				"source":   source,
				"metadata": metadata,
			}
			if !s.isEvidenceBoundToPerson(payload, personID) {
				continue
			}
			seenHash[hashValue] = struct{}{}
			evidence = append(evidence, payload)
		}
	}
	sort.SliceStable(evidence, func(left int, right int) bool {
		return personProfileScore(evidence[left]) > personProfileScore(evidence[right])
	})
	if len(evidence) > topK {
		evidence = evidence[:topK]
	}
	return s.filterStaleParagraphEvidence(evidence)
}

// collectFallbackVectorEvidence 对齐无检索器时的内容匹配回退
func (s *PersonProfileService) collectFallbackVectorEvidence(aliasQueries []string, topK int, personID string) ([]map[string]any, error) {
	fallback := make([]map[string]any, 0, topK)
	seenHash := map[string]struct{}{}
	for _, alias := range aliasQueries {
		rows, err := s.store.SearchParagraphsByContent(alias)
		if err != nil {
			return nil, err
		}
		limit := max(2, topK/2)
		if len(rows) > limit {
			rows = rows[:limit]
		}
		for _, paragraph := range rows {
			hashValue := personProfileMapText(paragraph, "hash")
			if hashValue == "" {
				continue
			}
			if _, exists := seenHash[hashValue]; exists {
				continue
			}
			seenHash[hashValue] = struct{}{}
			source := pyStr(paragraph["source"])
			item := map[string]any{
				"hash":     hashValue,
				"type":     "paragraph",
				"score":    0.0,
				"content":  personProfileTruncateRunes(personProfileMapText(paragraph, "content"), 180),
				"source":   source,
				"metadata": CoerceMetadataDict(paragraph["metadata"]),
			}
			if !s.isEvidenceBoundToPerson(item, personID) {
				delete(seenHash, hashValue)
				continue
			}
			fallback = append(fallback, item)
		}
	}
	if len(fallback) > topK {
		fallback = fallback[:topK]
	}
	return s.filterStaleParagraphEvidence(fallback)
}

// personProfileScore 读取证据得分
func personProfileScore(item map[string]any) float64 {
	value, ok := item["score"]
	if !ok || value == nil {
		return 0
	}
	parsed, err := pyFloat64(value)
	if err != nil {
		return 0
	}
	return parsed
}

// personProfileTruncateRunes 按码点截断，对齐 Python 字符串切片
func personProfileTruncateRunes(text string, limit int) string {
	runes := []rune(text)
	if limit < 0 {
		limit = 0
	}
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
