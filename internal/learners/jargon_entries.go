package learners

import (
	"sort"
	"strings"
)

// Entry 对齐 JargonEntry：内容 + 原始上下文段落集合 + 证据消息组
type Entry struct {
	Content    string
	RawContent map[string]struct{}
	Evidence   []JargonEvidenceGroup
}

// NewEntry 构建单条候选
func NewEntry(content string, rawContent []string, evidence []JargonEvidenceGroup) Entry {
	entry := Entry{
		Content:    strings.TrimSpace(content),
		RawContent: map[string]struct{}{},
	}
	for _, paragraph := range rawContent {
		if paragraph = strings.TrimSpace(paragraph); paragraph != "" {
			entry.RawContent[paragraph] = struct{}{}
		}
	}
	entry.Evidence = NormalizeJargonEvidenceGroups(evidence)
	return entry
}

// MergeEntries 按 content 合并 raw_content 并集与证据组
func MergeEntries(entries []Entry) []Entry {
	merged := make([]Entry, 0, len(entries))
	index := map[string]int{}
	for _, entry := range entries {
		content := strings.TrimSpace(entry.Content)
		if content == "" {
			continue
		}
		if pos, ok := index[content]; ok {
			for paragraph := range entry.RawContent {
				merged[pos].RawContent[paragraph] = struct{}{}
			}
			merged[pos].Evidence = MergeJargonEvidenceGroups(merged[pos].Evidence, entry.Evidence)
			continue
		}
		index[content] = len(merged)
		merged = append(merged, Entry{
			Content:    content,
			RawContent: copyParagraphs(entry.RawContent),
			Evidence:   NormalizeJargonEvidenceGroups(entry.Evidence),
		})
	}
	return merged
}

func copyParagraphs(source map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(source))
	for paragraph := range source {
		out[paragraph] = struct{}{}
	}
	return out
}

func sortedParagraphs(paragraphs map[string]struct{}) []string {
	out := make([]string, 0, len(paragraphs))
	for paragraph := range paragraphs {
		out = append(out, paragraph)
	}
	sort.Strings(out)
	return out
}
