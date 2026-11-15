package learners

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"MaiGo/internal/plugin_runtime/hooks"

	"go.uber.org/zap"
)

const (
	hookJargonExtractBeforePersist    = "jargon.extract.before_persist"
	hookJargonInferenceBeforeFinalize = "jargon.inference.before_finalize"
)

// serializeJargonEntries 对齐 JargonMiner._serialize_jargon_entries
func serializeJargonEntries(candidates []string) []any {
	out := make([]any, 0, len(candidates))
	for _, content := range candidates {
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		out = append(out, map[string]any{
			"content":           content,
			"raw_content":       []any{},
			"evidence_messages": []any{},
		})
	}
	return out
}

// deserializeJargonEntries 对齐 JargonMiner._deserialize_jargon_entries（保留 content 与空证据）
func deserializeJargonEntries(raw any) []string {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		content := strings.TrimSpace(jargonHookText(item["content"]))
		if content == "" {
			continue
		}
		out = append(out, content)
	}
	return out
}

// emitJargonBeforePersist 触发 jargon.extract.before_persist；aborted 时整批不写库
func emitJargonBeforePersist(ctx context.Context, sessionID, sessionName string, candidates []string) ([]string, bool) {
	result := hooks.EmitGlobal(ctx, hookJargonExtractBeforePersist, map[string]any{
		"session_id":   sessionID,
		"session_name": sessionName,
		"entries":      serializeJargonEntries(candidates),
	})
	logJargonHookResult(hookJargonExtractBeforePersist, result)
	if result.Aborted {
		return nil, true
	}
	if raw, ok := result.Kwargs["entries"]; ok && raw != nil {
		return deserializeJargonEntries(raw), false
	}
	return candidates, false
}

// serializeJargonEntryPayload 对齐 _serialize_jargon_entries：content + 排序后的 raw_content + 证据组
func serializeJargonEntryPayload(entries []Entry) []any {
	out := make([]any, 0, len(entries))
	for _, entry := range entries {
		content := strings.TrimSpace(entry.Content)
		if content == "" {
			continue
		}
		rawContent := make([]any, 0, len(entry.RawContent))
		for _, paragraph := range sortedParagraphs(entry.RawContent) {
			rawContent = append(rawContent, paragraph)
		}
		evidence := make([]any, 0, len(entry.Evidence))
		for _, group := range entry.Evidence {
			refs := make([]any, 0, len(group))
			for _, ref := range group {
				refs = append(refs, map[string]any{
					"platform":   ref.Platform,
					"message_id": ref.MessageID,
				})
			}
			evidence = append(evidence, refs)
		}
		out = append(out, map[string]any{
			"content":           content,
			"raw_content":       rawContent,
			"evidence_messages": evidence,
		})
	}
	return out
}

// deserializeJargonEntryPayload 对齐 _deserialize_jargon_entries
func deserializeJargonEntryPayload(raw any) []Entry {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]Entry, 0, len(list))
	for _, item := range list {
		payload, ok := item.(map[string]any)
		if !ok {
			continue
		}
		content := strings.TrimSpace(jargonHookText(payload["content"]))
		if content == "" {
			continue
		}
		var paragraphs []string
		if rawList, ok := payload["raw_content"].([]any); ok {
			for _, value := range rawList {
				if paragraph := strings.TrimSpace(jargonHookText(value)); paragraph != "" {
					paragraphs = append(paragraphs, paragraph)
				}
			}
		}
		var evidence []JargonEvidenceGroup
		if groups, ok := payload["evidence_messages"].([]any); ok {
			for _, rawGroup := range groups {
				refs, ok := rawGroup.([]any)
				if !ok {
					continue
				}
				group := make(JargonEvidenceGroup, 0, len(refs))
				for _, rawRef := range refs {
					ref, ok := rawRef.(map[string]any)
					if !ok {
						continue
					}
					group = append(group, JargonEvidenceRef{
						Platform:  strings.TrimSpace(jargonHookText(ref["platform"])),
						MessageID: strings.TrimSpace(jargonHookText(ref["message_id"])),
					})
				}
				evidence = append(evidence, group)
			}
		}
		out = append(out, NewEntry(content, paragraphs, evidence))
	}
	return out
}

// emitJargonEntryBeforePersist 触发 jargon.extract.before_persist 并回读完整条目
func emitJargonEntryBeforePersist(ctx context.Context, sessionID, sessionName string, entries []Entry) ([]Entry, bool) {
	result := hooks.EmitGlobal(ctx, hookJargonExtractBeforePersist, map[string]any{
		"session_id":   sessionID,
		"session_name": sessionName,
		"entries":      serializeJargonEntryPayload(entries),
	})
	logJargonHookResult(hookJargonExtractBeforePersist, result)
	if result.Aborted {
		return nil, true
	}
	if raw, ok := result.Kwargs["entries"]; ok && raw != nil {
		return deserializeJargonEntryPayload(raw), false
	}
	return entries, false
}

// jargonFinalizeState 对齐 before_finalize 读回字段
type jargonFinalizeState struct {
	IsJargon           bool
	Meaning            string
	IsComplete         bool
	LastInferenceCount int
}

// emitJargonBeforeFinalize 触发 jargon.inference.before_finalize 并读回判定结果
func emitJargonBeforeFinalize(
	ctx context.Context,
	sessionID, sessionName, content string,
	count int,
	rawContentList []string,
	comparisonResult map[string]any,
	isJargon bool,
	meaning string,
	isComplete bool,
	lastInferenceCount int,
) (jargonFinalizeState, bool) {
	state := jargonFinalizeState{
		IsJargon:           isJargon,
		Meaning:            meaning,
		IsComplete:         isComplete,
		LastInferenceCount: lastInferenceCount,
	}
	result := hooks.EmitGlobal(ctx, hookJargonInferenceBeforeFinalize, map[string]any{
		"session_id":           sessionID,
		"session_name":         sessionName,
		"content":              content,
		"count":                count,
		"raw_content_list":     jargonStringValues(rawContentList),
		"comparison_result":    comparisonResult,
		"is_jargon":            isJargon,
		"meaning":              meaning,
		"is_complete":          isComplete,
		"last_inference_count": lastInferenceCount,
	})
	logJargonHookResult(hookJargonInferenceBeforeFinalize, result)
	if result.Aborted {
		return state, true
	}
	if value, ok := result.Kwargs["is_jargon"]; ok {
		state.IsJargon = jargonHookBool(value, state.IsJargon)
	}
	if value, ok := result.Kwargs["meaning"]; ok {
		state.Meaning = strings.TrimSpace(jargonHookText(value))
	}
	if value, ok := result.Kwargs["is_complete"]; ok {
		state.IsComplete = jargonHookBool(value, state.IsComplete)
	}
	if value, ok := result.Kwargs["last_inference_count"]; ok {
		state.LastInferenceCount = jargonHookInt(value, state.LastInferenceCount)
	}
	return state, false
}

func jargonStringValues(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// jargonHookBool 对齐 Python bool(value)
func jargonHookBool(value any, fallback bool) bool {
	switch v := value.(type) {
	case nil:
		return fallback
	case bool:
		return v
	case string:
		return v != ""
	case int:
		return v != 0
	case int64:
		return v != 0
	case uint64:
		return v != 0
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	default:
		return fallback
	}
}

// jargonHookInt 对齐 Python int(value) 的容错分支
func jargonHookInt(value any, fallback int) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case uint64:
		return int(v)
	case float64:
		return int(v)
	case bool:
		if v {
			return 1
		}
		return 0
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fallback
		}
		return parsed
	case nil:
		return fallback
	default:
		return fallback
	}
}

// jargonHookText 对齐 Python str(value or "")
func jargonHookText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprintf("%v", v)
	}
}

func logJargonHookResult(name string, result hooks.DispatchResult) {
	for _, message := range result.Errors {
		zap.L().Warn("黑话 Hook 执行失败",
			zap.String("hook", name),
			zap.String("error", message))
	}
	if result.Aborted {
		zap.L().Info("黑话 Hook 中止",
			zap.String("hook", name),
			zap.String("stopped_by", result.StoppedBy))
	}
}
