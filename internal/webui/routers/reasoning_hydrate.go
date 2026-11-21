package webuiapi

import (
	"os"
	"strconv"
	"strings"
)

// replyRequestStages 超级创意请求阶段沿用 Replyer 的请求预览
var replyRequestStages = map[string]struct{}{"replyer": {}, "maisaka.replyer": {}, "replyer_creative": {}}

// replyOutputStages 所有产出最终回复正文的阶段
var replyOutputStages = map[string]struct{}{"replyer": {}, "maisaka.replyer": {}, "replyer_creative": {}, "replyer_translation": {}}

func isReplyRequestStage(stage string) bool {
	_, ok := replyRequestStages[stage]
	return ok
}

func isReplyOutputStage(stage string) bool {
	_, ok := replyOutputStages[stage]
	return ok
}

// 本文件对齐 ../MaiBot/src/webui/routers/reasoning_process.py 的记录归属与搜索过滤
func hydrateReasoningRecord(root string, record *reasoningPromptFile, includePreviews, includeActionPreview, includeOutputPreview bool) *reasoningPromptFile {
	stage := record.Stage
	extractAction := includeActionPreview && (stage == "jargon_learning_update" || stage == "planner")
	extractOutput := includeOutputPreview && (isReplyOutputStage(stage) || stage == "expression_selector")
	extractError := stage == "llm_error"
	var payload map[string]any
	jsonPath, hasJSON := resolveReasoningRecordPath(root, record.JSONPath, ".json")
	if hasJSON {
		if includePreviews || extractAction || extractOutput || extractError {
			payload, _ = readJSONMap(jsonPath)
			mergeReasoningMetadata(record, extractReasoningMetadataFromJSONPayload(payload))
			switch {
			case extractError:
				if title := extractLLMErrorDisplayTitle(payload); title != "" {
					record.DisplayTitle = stringPointer(title)
				}
			case stage == "expression_selector":
				pairs, ok := extractSelectedExpressionPairs(payload)
				if ok {
					record.SelectedExpressions = pairs
					if len(pairs) > 0 {
						labels := make([]string, 0, len(pairs))
						for _, pair := range pairs {
							labels = append(labels, pair.Situation+" → "+pair.Style)
						}
						record.OutputPreview = stringPointer(strings.Join(labels, "；"))
					} else {
						record.OutputPreview = stringPointer("未选择表达")
					}
				} else {
					record.OutputPreview = stringPointer("无法从记录还原已选表达")
				}
			case isReplyOutputStage(stage) && (includePreviews || extractOutput):
				if preview := truncateReasoningPreview(extractOutputTextFromJSONPayload(payload), 160); preview != "" {
					record.OutputPreview = stringPointer(preview)
				}
			case stage == "planner" || isJargonLearningUpdatePreviewPayload(payload):
				if record.DisplayTitle == nil {
					if preview := extractActionPreviewFromJSONPayload(payload, 4); preview != "" {
						record.ActionPreview = stringPointer(preview)
					}
				}
			}
		} else {
			content, _ := os.ReadFile(jsonPath)
			mergeReasoningMetadata(record, extractReasoningMetadataFromJSONHead(string(content)))
		}
	}
	metadataMissing := record.ModelName == nil || record.DurationMS == nil
	previewMissing := (includePreviews || extractOutput) && isReplyOutputStage(stage) && record.OutputPreview == nil
	if textPath, ok := resolveReasoningRecordPath(root, record.TextPath, ".txt"); ok && (metadataMissing || previewMissing) {
		content, _ := os.ReadFile(textPath)
		mergeReasoningMetadata(record, extractReasoningMetadataFromText(string(content)))
		if isReplyOutputStage(stage) && record.OutputPreview == nil {
			if preview := truncateReasoningPreview(extractOutputTextFromContent(string(content)), 160); preview != "" {
				record.OutputPreview = stringPointer(preview)
			}
		}
		metadataMissing = record.ModelName == nil || record.DurationMS == nil
	}
	if htmlPath, ok := resolveReasoningRecordPath(root, record.HTMLPath, ".html"); ok && metadataMissing {
		content, _ := os.ReadFile(htmlPath)
		mergeReasoningMetadata(record, extractReasoningMetadataFromHTML(string(content)))
	}
	return record
}

func hydrateReasoningRecords(root string, records []*reasoningPromptFile, includePreviews, includeActionPreview, includeOutputPreview bool) []*reasoningPromptFile {
	result := make([]*reasoningPromptFile, 0, len(records))
	for _, record := range records {
		result = append(result, hydrateReasoningRecord(root, record, includePreviews, includeActionPreview, includeOutputPreview))
	}
	return result
}

func deduplicateReplyerRecords(items []*reasoningPromptFile) []*reasoningPromptFile {
	result := []*reasoningPromptFile{}
	seen := map[string][]int{}
	for _, item := range items {
		if !isReplyOutputStage(item.Stage) || item.OutputPreview == nil || *item.OutputPreview == "" {
			result = append(result, item)
			continue
		}
		key := item.SessionID + reasoningCacheSeparator + strings.TrimSpace(*item.OutputPreview)
		existingIndex := -1
		for _, index := range seen[key] {
			if duplicateReplyerRecordTimeClose(result[index], item) {
				existingIndex = index
				break
			}
		}
		if existingIndex < 0 {
			seen[key] = append(seen[key], len(result))
			result = append(result, item)
			continue
		}
		if shouldReplaceDuplicateReplyerRecord(result[existingIndex], item) {
			result[existingIndex] = item
		}
	}
	return result
}

func duplicateReplyerRecordTimeClose(left, right *reasoningPromptFile) bool {
	if left.Timestamp != nil && right.Timestamp != nil {
		diff := *left.Timestamp - *right.Timestamp
		if diff < 0 {
			diff = -diff
		}
		return diff <= 10_000
	}
	diff := left.ModifiedAt - right.ModifiedAt
	if diff < 0 {
		diff = -diff
	}
	return diff <= 10
}

func shouldReplaceDuplicateReplyerRecord(existing, candidate *reasoningPromptFile) bool {
	if existing.ModelName != nil && candidate.ModelName == nil {
		return false
	}
	if candidate.ModelName != nil && existing.ModelName == nil {
		return true
	}
	if existing.DurationMS != nil && candidate.DurationMS == nil {
		return false
	}
	if candidate.DurationMS != nil && existing.DurationMS == nil {
		return true
	}
	return candidate.ModifiedAt > existing.ModifiedAt
}

func matchesReasoningSearch(item *reasoningPromptFile, search string) bool {
	values := []string{item.Stage, item.SessionID, item.Stem}
	if item.SessionDisplayName != nil {
		values = append(values, *item.SessionDisplayName)
	}
	if item.ResolvedSessionID != nil {
		values = append(values, *item.ResolvedSessionID)
	}
	if item.OutputPreview != nil {
		values = append(values, *item.OutputPreview)
	}
	if item.ActionPreview != nil {
		values = append(values, *item.ActionPreview)
	}
	if item.DisplayTitle != nil {
		values = append(values, *item.DisplayTitle)
	}
	if item.ModelName != nil {
		values = append(values, *item.ModelName)
	}
	if item.DurationMS != nil {
		values = append(values, strconv.FormatFloat(*item.DurationMS, 'f', -1, 64))
	}
	for _, value := range []*int64{item.PromptTokens, item.CompletionTokens, item.TotalTokens} {
		if value != nil {
			values = append(values, strconv.FormatInt(*value, 10))
		}
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), search) {
			return true
		}
	}
	return false
}

func matchesReasoningAction(item *reasoningPromptFile, action string) bool {
	if item.ActionPreview == nil {
		return false
	}
	preview := strings.TrimSpace(*item.ActionPreview)
	for _, prefix := range []string{"动作：", "动作:"} {
		if strings.HasPrefix(preview, prefix) {
			preview = strings.TrimSpace(strings.TrimPrefix(preview, prefix))
			break
		}
	}
	return strings.Contains(strings.ToLower(preview), action)
}
