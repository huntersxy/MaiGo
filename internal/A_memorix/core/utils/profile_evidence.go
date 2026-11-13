package utils

// A_memorix 画像证据工具，对齐 Python core/utils/profile_evidence.py

import "strings"

// ProfileEvidenceTypeFromSource 对齐 profile_evidence_type_from_source
func ProfileEvidenceTypeFromSource(source string, metadata map[string]any) string {
	meta := metadata
	if meta == nil {
		meta = map[string]any{}
	}
	sourceType := profileEvidenceString(meta["source_type"])
	if sourceType == "person_fact" || sourceType == "chat_summary" {
		return sourceType
	}
// 对齐 Python str(source or meta.get("source","") or "").strip()：
	// 非空 source（含纯空白）为真值，strip 后不再回落 meta。
	token := ""
	if source != "" {
		token = pyStrip(source)
	} else {
		token = profileEvidenceString(meta["source"])
	}
	if strings.HasPrefix(token, "person_fact:") {
		return "person_fact"
	}
	if strings.HasPrefix(token, "chat_summary:") {
		return "chat_summary"
	}
	return "paragraph"
}

// ProfileRelationContent 对齐 profile_relation_content
func ProfileRelationContent(relation map[string]any) string {
	subject := profileEvidenceString(relation["subject"])
	predicate := profileEvidenceString(relation["predicate"])
	object := profileEvidenceString(relation["object"])
	if subject != "" && predicate != "" && object != "" {
		return subject + " -[" + predicate + "]-> " + object
	}
	parts := make([]string, 0, 3)
	for _, item := range []string{subject, predicate, object} {
		if item != "" {
			parts = append(parts, item)
		}
	}
	return pyStrip(strings.Join(parts, " "))
}

// profileEvidenceString 对齐 str(value or "").strip()
func profileEvidenceString(value any) string {
	if !isTruthy(value) {
		return ""
	}
	return pyStrip(pyStr(value))
}
