package learners

import (
	"strings"

	"github.com/bytedance/sonic"
)

type jargonEvidenceWireRef struct {
	Platform  string `json:"platform"`
	MessageID string `json:"message_id"`
}

// NormalizeJargonEvidenceGroups 对齐 _normalize_evidence_message_groups：过滤空引用与重复组
func NormalizeJargonEvidenceGroups(groups []JargonEvidenceGroup) []JargonEvidenceGroup {
	out := make([]JargonEvidenceGroup, 0, len(groups))
	seenGroups := map[string]struct{}{}
	for _, group := range groups {
		cleaned := make(JargonEvidenceGroup, 0, len(group))
		seenRefs := map[JargonEvidenceRef]struct{}{}
		for _, ref := range group {
			ref.Platform = strings.TrimSpace(ref.Platform)
			ref.MessageID = strings.TrimSpace(ref.MessageID)
			if ref.Platform == "" || ref.MessageID == "" {
				continue
			}
			if _, ok := seenRefs[ref]; ok {
				continue
			}
			seenRefs[ref] = struct{}{}
			cleaned = append(cleaned, ref)
		}
		if len(cleaned) == 0 {
			continue
		}
		key := jargonEvidenceGroupKey(cleaned)
		if _, ok := seenGroups[key]; ok {
			continue
		}
		seenGroups[key] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}

// ParseJargonEvidenceGroups 解析数据库 evidence_messages JSON；非法内容返回空
func ParseJargonEvidenceGroups(raw string) []JargonEvidenceGroup {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var payload [][]jargonEvidenceWireRef
	if err := sonic.UnmarshalString(raw, &payload); err != nil {
		return nil
	}
	groups := make([]JargonEvidenceGroup, 0, len(payload))
	for _, wireGroup := range payload {
		group := make(JargonEvidenceGroup, 0, len(wireGroup))
		for _, ref := range wireGroup {
			group = append(group, JargonEvidenceRef{Platform: ref.Platform, MessageID: ref.MessageID})
		}
		groups = append(groups, group)
	}
	return NormalizeJargonEvidenceGroups(groups)
}

// EncodeJargonEvidenceGroups 序列化证据组；空组返回 nil（对齐 Python 写 None）
func EncodeJargonEvidenceGroups(groups []JargonEvidenceGroup) *string {
	normalized := NormalizeJargonEvidenceGroups(groups)
	if len(normalized) == 0 {
		return nil
	}
	payload := make([][]jargonEvidenceWireRef, 0, len(normalized))
	for _, group := range normalized {
		wireGroup := make([]jargonEvidenceWireRef, 0, len(group))
		for _, ref := range group {
			wireGroup = append(wireGroup, jargonEvidenceWireRef{Platform: ref.Platform, MessageID: ref.MessageID})
		}
		payload = append(payload, wireGroup)
	}
	data, err := sonic.Marshal(payload)
	if err != nil {
		return nil
	}
	encoded := string(data)
	return &encoded
}

// MergeJargonEvidenceGroups 对齐 _merge_evidence_message_groups：按组去重保序追加
func MergeJargonEvidenceGroups(current, incoming []JargonEvidenceGroup) []JargonEvidenceGroup {
	merged := NormalizeJargonEvidenceGroups(current)
	seen := make(map[string]struct{}, len(merged))
	for _, group := range merged {
		seen[jargonEvidenceGroupKey(group)] = struct{}{}
	}
	for _, group := range NormalizeJargonEvidenceGroups(incoming) {
		key := jargonEvidenceGroupKey(group)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, group)
	}
	return merged
}

func jargonEvidenceGroupKey(group JargonEvidenceGroup) string {
	var b strings.Builder
	for _, ref := range group {
		b.WriteString(ref.Platform)
		b.WriteByte(0)
		b.WriteString(ref.MessageID)
		b.WriteByte(1)
	}
	return b.String()
}
