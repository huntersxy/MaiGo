package utils

// A_memorix 人物画像指纹，对齐 _canonical_profile_value 与 _profile_evidence_fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// 指纹计算时忽略的检索评分字段，对齐 _profile_evidence_fingerprint
var personProfileIgnoredMetadataFields = map[string]struct{}{
	"score_breakdown":        {},
	"bm25_score":             {},
	"ppr_signal":             {},
	"ppr_focus_entity_count": {},
	"ppr_boost":              {},
}

// canonicalProfileValue 对齐 _canonical_profile_value
// 消除字典顺序、集合顺序和召回排序抖动
func CanonicalPersonProfileValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		result := make(map[string]any, len(typed))
		for _, key := range keys {
			result[key] = CanonicalPersonProfileValue(typed[key])
		}
		return result
	case []any:
		normalized := make([]any, 0, len(typed))
		for _, item := range typed {
			normalized = append(normalized, CanonicalPersonProfileValue(item))
		}
		sort.SliceStable(normalized, func(left int, right int) bool {
			return personProfileJSONText(normalized[left]) < personProfileJSONText(normalized[right])
		})
		return normalized
	case []string:
		normalized := make([]any, 0, len(typed))
		for _, item := range typed {
			normalized = append(normalized, item)
		}
		sort.SliceStable(normalized, func(left int, right int) bool {
			return personProfileJSONText(normalized[left]) < personProfileJSONText(normalized[right])
		})
		return normalized
	case float64:
		return pyRound(typed, 12)
	case float32:
		return pyRound(float64(typed), 12)
	default:
		return value
	}
}

// personProfileJSONText 输出用于排序的 JSON 文本，失败时退回普通字符串
func personProfileJSONText(value any) string {
	text, err := pyJSONMarshal(value)
	if err != nil {
		return pyStr(value)
	}
	return text
}

// profileGenerationSignature 对齐 _profile_generation_signature
func (s *PersonProfileService) profileGenerationSignature() map[string]any {
	modelSignature := map[string]any{"mode": "rule_based"}
	if s.modelSignature != nil {
		if custom := s.modelSignature(); len(custom) > 0 {
			modelSignature = custom
		}
	}
	return map[string]any{
		"generation_version":        PersonProfileGenerationVersion,
		"classification_max_tokens": s.profileClassificationMaxTokens(),
		"model":                     modelSignature,
	}
}

// ProfileEvidenceFingerprint 对齐 _profile_evidence_fingerprint
// 检索分数不属于证据内容，故不进入指纹
func (s *PersonProfileService) ProfileEvidenceFingerprint(personID string, primaryName string, aliases []string, relationEdges []map[string]any, vectorEvidence []map[string]any, factClaims []map[string]any) string {
	stableVectorEvidence := make([]map[string]any, 0, len(vectorEvidence))
	for _, item := range vectorEvidence {
		copied := make(map[string]any, len(item))
		for key, value := range item {
			if key == "score" {
				continue
			}
			copied[key] = value
		}
		if rawMetadata, ok := copied["metadata"]; ok {
			metadata := CoerceMetadataDict(rawMetadata)
			filtered := make(map[string]any, len(metadata))
			for key, value := range metadata {
				if _, ignored := personProfileIgnoredMetadataFields[key]; ignored {
					continue
				}
				filtered[key] = value
			}
			copied["metadata"] = filtered
		}
		stableVectorEvidence = append(stableVectorEvidence, copied)
	}

	relationRows := make([]any, 0, len(relationEdges))
	for _, item := range relationEdges {
		relationRows = append(relationRows, item)
	}
	vectorRows := make([]any, 0, len(stableVectorEvidence))
	for _, item := range stableVectorEvidence {
		vectorRows = append(vectorRows, item)
	}
	claimRows := make([]any, 0, len(factClaims))
	for _, item := range factClaims {
		claimRows = append(claimRows, item)
	}
	aliasRows := personProfileAnySlice(aliases)
	payload := CanonicalPersonProfileValue(map[string]any{
		"person_id":       personID,
		"primary_name":    primaryName,
		"aliases":         aliasRows,
		"relation_edges":  relationRows,
		"vector_evidence": vectorRows,
		"fact_claims":     claimRows,
		"generation":      s.profileGenerationSignature(),
	})
	encoded := personProfileJSONText(payload)
	sum := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(sum[:])
}
