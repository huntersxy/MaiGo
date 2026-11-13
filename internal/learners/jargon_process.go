package learners

import (
	"context"
	"strings"

	"go.uber.org/zap"
)

// processJargonEntries 对齐 JargonLearner._process_jargon_entries：来源校验、上下文段落与证据组
func (l *JargonLearner) processJargonEntries(ctx context.Context, candidates []JargonLearningCandidate, messages []SourceItem, miner *JargonMiner, settings LearningSettings) (jargonProcessResult, error) {
	result := jargonProcessResult{Parsed: len(candidates)}
	if len(candidates) == 0 || len(messages) == 0 {
		l.logJargonUpdateProcess("empty_input", miner, messages, result.Parsed, 0, nil, 0, 0)
		return result, nil
	}
	botName := strings.TrimSpace(settings.BotNickname)
	entries := make([]Entry, 0, len(candidates))
	skipped := make([]skippedJargonEntry, 0)
	for _, candidate := range candidates {
		content := strings.TrimSpace(candidate.Content)
		sourceID := strings.TrimSpace(candidate.SourceID)
		if content == "" {
			skipped = append(skipped, skippedJargonEntry{Content: content, SourceID: sourceID, Reason: "empty_content"})
			continue
		}
		if strings.Contains(content, "SELF") {
			zap.L().Info("跳过包含 SELF 的黑话", zap.String("content", content))
			skipped = append(skipped, skippedJargonEntry{Content: content, SourceID: sourceID, Reason: "contains_self"})
			continue
		}
		if botName != "" && strings.Contains(content, botName) {
			zap.L().Info("跳过包含机器人昵称的黑话", zap.String("content", content))
			skipped = append(skipped, skippedJargonEntry{Content: content, SourceID: sourceID, Reason: "contains_bot_nickname"})
			continue
		}
		if !isAllDigits(sourceID) {
			zap.L().Warn("黑话条目 source_id 无效",
				zap.String("content", content),
				zap.String("source_id", sourceID))
			skipped = append(skipped, skippedJargonEntry{Content: content, SourceID: sourceID, Reason: "invalid_source_id"})
			continue
		}
		lineIndex := parseDigits(sourceID) - 1
		if lineIndex < 0 || lineIndex >= len(messages) {
			zap.L().Warn("黑话条目 source_id 超出范围",
				zap.String("content", content),
				zap.String("source_id", sourceID))
			skipped = append(skipped, skippedJargonEntry{Content: content, SourceID: sourceID, Reason: "source_id_out_of_range"})
			continue
		}
		origin := messages[lineIndex]
		if !isLearnableSourceItem(origin) || IsLearningBlacklisted(settings.JargonBlacklist, origin.Platform, origin.UserID) {
			skipped = append(skipped, skippedJargonEntry{Content: content, SourceID: sourceID, Reason: "excluded_source"})
			continue
		}
		start := lineIndex - 3
		if start < 0 {
			start = 0
		}
		end := lineIndex + 4
		if end > len(messages) {
			end = len(messages)
		}
		contextItems := messages[start:end]
		var paragraphLines []string
		var evidence JargonEvidenceGroup
		for offset, item := range contextItems {
			text := strings.TrimSpace(item.Content)
			if text == "" {
				continue
			}
			paragraphLines = append(paragraphLines, "["+jargonItoa(start+offset+1)+"] "+text)
			platform := strings.TrimSpace(item.Platform)
			if platform == "" {
				platform = "qq"
			}
			if messageID := strings.TrimSpace(item.MessageID); messageID != "" {
				evidence = append(evidence, JargonEvidenceRef{Platform: platform, MessageID: messageID})
			}
		}
		paragraph := strings.Join(paragraphLines, "\n")
		if paragraph == "" {
			zap.L().Warn("黑话条目上下文为空",
				zap.String("content", content),
				zap.String("source_id", sourceID))
			skipped = append(skipped, skippedJargonEntry{Content: content, SourceID: sourceID, Reason: "empty_context"})
			continue
		}
		entries = append(entries, NewEntry(content, []string{paragraph}, []JargonEvidenceGroup{evidence}))
	}
	result.Skipped = skipped
	if len(entries) == 0 {
		l.logJargonUpdateProcess("no_accepted_entries", miner, messages, result.Parsed, 0, skipped, 0, 0)
		return result, nil
	}
	entries = MergeEntries(entries)
	result.Accepted = len(entries)
	result.Contents = make([]string, 0, len(entries))
	for _, entry := range entries {
		result.Contents = append(result.Contents, entry.Content)
	}
	saved, updated, err := miner.ProcessExtractedEntries(ctx, entries)
	result.Saved, result.Updated = saved, updated
	if err != nil {
		return result, err
	}
	status := "no_database_change"
	if saved+updated > 0 {
		status = "persisted"
	}
	l.logJargonUpdateProcess(status, miner, messages, result.Parsed, result.Accepted, skipped, saved, updated)
	return result, nil
}

func isLearnableSourceItem(item SourceItem) bool {
	if item.IsBotSelf {
		return false
	}
	return item.SourceKind == "" || item.SourceKind == "user"
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseDigits(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}
