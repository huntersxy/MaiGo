package webuiapi

import (
	"encoding/json"
	"html"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	"MaiGo/internal/common/llmjson"
)

// 本文件对齐 ../MaiBot/src/webui/routers/reasoning_process.py 的元数据与预览提取

const (
	reasoningPromptMetadataMarker = "[请求信息]"
	reasoningPromptSeparator      = "=================================================================================="
)

var (
	reasoningPromptMetadataScriptPattern = regexp.MustCompile("(?is)<script[^>]*id=[\"']prompt-preview-metadata[\"'][^>]*>(.*?)</script>")
	reasoningModelNamePattern            = regexp.MustCompile("\"(?:model_name|model)\"\\s*:\\s*\"([^\"]*)\"")
	reasoningDurationPattern             = regexp.MustCompile("\"duration_ms\"\\s*:\\s*(-?[0-9]+(?:\\.[0-9]+)?)")
	reasoningTokenPattern                = regexp.MustCompile("\"(prompt_tokens|completion_tokens|total_tokens)\"\\s*:\\s*([0-9]+)")
	reasoningNumberPattern               = regexp.MustCompile("-?[0-9]+(?:\\.[0-9]+)?")
	reasoningDigitsPattern               = regexp.MustCompile("[0-9]+")
	reasoningHTMLTagPattern              = regexp.MustCompile("<[^>]+>")
)

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func reasoningFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstAny(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func parseReasoningMetadataValue(line string) (string, string, bool) {
	normalized := strings.TrimSpace(line)
	if normalized == "" {
		return "", "", false
	}
	for _, separator := range []string{"：", ":"} {
		index := strings.Index(normalized, separator)
		if index < 0 {
			continue
		}
		key := strings.TrimSpace(normalized[:index])
		value := strings.TrimSpace(normalized[index+len(separator):])
		if key != "" && value != "" {
			return key, value, true
		}
	}
	return "", "", false
}

func parseReasoningDuration(value any) *float64 {
	switch typed := value.(type) {
	case float64:
		result := math.Round(typed*100) / 100
		return &result
	case int64:
		result := float64(typed)
		return &result
	case json.Number:
		if parsed, err := typed.Float64(); err == nil {
			result := math.Round(parsed*100) / 100
			return &result
		}
	}
	text := strings.TrimSpace(stringValue(value))
	if text == "" {
		return nil
	}
	match := reasoningNumberPattern.FindString(text)
	if match == "" {
		return nil
	}
	parsed, err := strconv.ParseFloat(match, 64)
	if err != nil {
		return nil
	}
	result := math.Round(parsed*100) / 100
	return &result
}

func parseReasoningTokenCount(value any) *int64 {
	switch typed := value.(type) {
	case int:
		result := int64(typed)
		return &result
	case int64:
		result := typed
		if result < 0 {
			result = 0
		}
		return &result
	case float64:
		if typed != float64(int64(typed)) {
			return nil
		}
		result := int64(typed)
		if result < 0 {
			result = 0
		}
		return &result
	case *int64:
		if typed == nil {
			return nil
		}
		result := *typed
		if result < 0 {
			result = 0
		}
		return &result
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			if parsed < 0 {
				parsed = 0
			}
			return &parsed
		}
	}
	text := strings.ReplaceAll(strings.TrimSpace(stringValue(value)), ",", "")
	match := reasoningDigitsPattern.FindString(text)
	if match == "" {
		return nil
	}
	parsed, err := strconv.ParseInt(match, 10, 64)
	if err != nil {
		return nil
	}
	return &parsed
}

func normalizeReasoningMetadata(raw map[string]any) map[string]any {
	metadata := map[string]any{}
	modelName := strings.TrimSpace(reasoningFirstNonEmpty(stringValue(raw["model_name"]), stringValue(raw["model"])))
	if modelName != "" {
		metadata["model_name"] = modelName
	}
	if duration := parseReasoningDuration(raw["duration_ms"]); duration != nil {
		metadata["duration_ms"] = *duration
	}
	keys := []string{"prompt_tokens", "completion_tokens", "total_tokens"}
	present := map[string]bool{}
	for _, key := range keys {
		if tokenCount := parseReasoningTokenCount(raw[key]); tokenCount != nil {
			metadata[key] = *tokenCount
			present[key] = true
		}
	}
	if !present["total_tokens"] && present["prompt_tokens"] && present["completion_tokens"] {
		metadata["total_tokens"] = metadata["prompt_tokens"].(int64) + metadata["completion_tokens"].(int64)
	}
	return metadata
}

func extractReasoningMetadataFromText(content string) map[string]any {
	markerIndex := strings.Index(content, reasoningPromptMetadataMarker)
	if markerIndex < 0 {
		return map[string]any{}
	}
	metadataText := content[markerIndex+len(reasoningPromptMetadataMarker):]
	if separatorIndex := strings.Index(metadataText, reasoningPromptSeparator); separatorIndex >= 0 {
		metadataText = metadataText[:separatorIndex]
	}
	raw := map[string]any{}
	for _, line := range strings.Split(metadataText, "\n") {
		key, value, ok := parseReasoningMetadataValue(line)
		if !ok {
			continue
		}
		switch key {
		case "请求模型", "模型":
			raw["model_name"] = value
		case "推理耗时", "请求耗时", "耗时":
			raw["duration_ms"] = value
		case "输入 Token", "输入Token", "Prompt Token":
			raw["prompt_tokens"] = parseReasoningTokenCount(value)
		case "输出 Token", "输出Token", "Completion Token":
			raw["completion_tokens"] = parseReasoningTokenCount(value)
		case "总 Token", "总Token", "Token":
			raw["total_tokens"] = parseReasoningTokenCount(value)
		}
	}
	return normalizeReasoningMetadata(raw)
}

func extractReasoningMetadataFromHTML(content string) map[string]any {
	if match := reasoningPromptMetadataScriptPattern.FindStringSubmatch(content); match != nil {
		var raw map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(html.UnescapeString(match[1]))), &raw); err == nil {
			if metadata := normalizeReasoningMetadata(raw); len(metadata) > 0 {
				return metadata
			}
		}
	}
	plain := reasoningHTMLTagPattern.ReplaceAllString(content, "\n")
	return extractReasoningMetadataFromText(html.UnescapeString(plain))
}

func extractReasoningMetadataFromJSONPayload(payload map[string]any) map[string]any {
	raw, _ := payload["metadata"].(map[string]any)
	metadata := normalizeReasoningMetadata(raw)
	if metadata["prompt_tokens"] != nil && metadata["completion_tokens"] != nil && metadata["total_tokens"] != nil {
		return metadata
	}
	attempts, _ := payload["generation_attempts"].([]any)
	for index := len(attempts) - 1; index >= 0; index-- {
		attempt, _ := attempts[index].(map[string]any)
		if attempt == nil {
			continue
		}
		if trace, ok := attempt["trace"].(map[string]any); ok {
			hasToken := false
			for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
				if parsed := parseReasoningTokenCount(trace[key]); parsed != nil && *parsed > 0 {
					hasToken = true
				}
			}
			if hasToken {
				for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
					if _, exists := metadata[key]; !exists {
						if parsed := parseReasoningTokenCount(trace[key]); parsed != nil {
							metadata[key] = *parsed
						}
					}
				}
			}
		}
		if wire, ok := attempt["wire_response"].(map[string]any); ok {
			if usage, ok := wire["usage"].(map[string]any); ok {
				usageValues := map[string]any{
					"prompt_tokens":     firstAny(usage["prompt_tokens"], usage["input_tokens"]),
					"completion_tokens": firstAny(usage["completion_tokens"], usage["output_tokens"]),
					"total_tokens":      usage["total_tokens"],
				}
				for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
					if _, exists := metadata[key]; !exists {
						if parsed := parseReasoningTokenCount(usageValues[key]); parsed != nil {
							metadata[key] = *parsed
						}
					}
				}
			}
		}
		_, hasPrompt := metadata["prompt_tokens"]
		_, hasCompletion := metadata["completion_tokens"]
		_, hasTotal := metadata["total_tokens"]
		if hasPrompt && hasCompletion && hasTotal {
			break
		}
	}
	return normalizeReasoningMetadata(metadata)
}

func extractReasoningMetadataFromJSONHead(content string) map[string]any {
	raw := map[string]any{}
	if match := reasoningModelNamePattern.FindStringSubmatch(content); match != nil {
		var decoded string
		if err := json.Unmarshal([]byte("\""+match[1]+"\""), &decoded); err == nil {
			raw["model_name"] = decoded
		} else {
			raw["model_name"] = match[1]
		}
	}
	if match := reasoningDurationPattern.FindStringSubmatch(content); match != nil {
		raw["duration_ms"] = match[1]
	}
	for _, match := range reasoningTokenPattern.FindAllStringSubmatch(content, -1) {
		if parsed, err := strconv.ParseInt(match[2], 10, 64); err == nil {
			raw[match[1]] = parsed
		}
	}
	return normalizeReasoningMetadata(raw)
}

func mergeReasoningMetadata(record *reasoningPromptFile, metadata map[string]any) {
	if record.ModelName == nil {
		if value, ok := metadata["model_name"].(string); ok && strings.TrimSpace(value) != "" {
			record.ModelName = stringPointer(strings.TrimSpace(value))
		}
	}
	if record.DurationMS == nil {
		if value, ok := metadata["duration_ms"].(float64); ok {
			record.DurationMS = &value
		}
	}
	targets := []struct {
		key    string
		target **int64
	}{
		{"prompt_tokens", &record.PromptTokens},
		{"completion_tokens", &record.CompletionTokens},
		{"total_tokens", &record.TotalTokens},
	}
	for _, item := range targets {
		if *item.target != nil {
			continue
		}
		if value, ok := metadata[item.key].(int64); ok {
			copied := value
			*item.target = &copied
		}
	}
}

func extractReasoningOutputBlock(content string) string {
	const marker = "[输出结果]"
	index := strings.Index(content, marker)
	if index < 0 {
		return ""
	}
	output := content[index+len(marker):]
	if separatorIndex := strings.Index(output, reasoningPromptSeparator); separatorIndex >= 0 {
		output = output[:separatorIndex]
	}
	return strings.TrimSpace(output)
}

func normalizeReasoningOutputLines(text string) string {
	lines := strings.Split(text, "\n")
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " ")
}

func extractOutputTextFromContent(content string) string {
	return normalizeReasoningOutputLines(extractReasoningOutputBlock(content))
}

func extractOutputTextFromJSONPayload(payload map[string]any) string {
	if items, ok := payload["output_items"].([]any); ok {
		texts := []string{}
		for _, rawItem := range items {
			item, _ := rawItem.(map[string]any)
			if item == nil {
				continue
			}
			switch stringValue(item["item_type"]) {
			case "AssistantMessageItem":
				parts, _ := item["parts"].([]any)
				for _, rawPart := range parts {
					part, _ := rawPart.(map[string]any)
					if part == nil {
						continue
					}
					switch stringValue(part["type"]) {
					case "text":
						if text, ok := part["text"].(string); ok {
							texts = append(texts, text)
						}
					case "refusal":
						if text, ok := part["refusal"].(string); ok {
							texts = append(texts, text)
						}
					}
				}
			case "ProviderOpaqueItem":
				if summary, ok := item["display_summary"].(string); ok {
					texts = append(texts, summary)
				}
			}
		}
		if normalized := normalizeReasoningOutputLines(strings.Join(texts, "\n")); normalized != "" {
			return normalized
		}
	}
	output, _ := payload["output"].(map[string]any)
	if output == nil {
		return ""
	}
	switch typed := output["content"].(type) {
	case string:
		return normalizeReasoningOutputLines(typed)
	case map[string]any:
		if response, ok := typed["response"].(string); ok && strings.TrimSpace(response) != "" {
			return normalizeReasoningOutputLines(response)
		}
		return normalizeReasoningOutputLines(structuredPromptContentToText(typed))
	case []any:
		return normalizeReasoningOutputLines(structuredPromptContentToText(typed))
	}
	return ""
}

func structuredPromptContentToText(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		parts := []string{}
		for _, rawItem := range typed {
			switch item := rawItem.(type) {
			case string:
				parts = append(parts, item)
			case map[string]any:
				if stringValue(item["type"]) == "text" {
					if text, ok := item["text"].(string); ok {
						parts = append(parts, text)
					}
					continue
				}
				kind := strings.ToLower(stringValue(item["type"]))
				if kind == "image" || kind == "image_url" || kind == "input_image" {
					format := reasoningFirstNonEmpty(stringValue(item["image_format"]), stringValue(item["format"]), "unknown")
					sizeText := ""
					if size, ok := item["size_bytes"].(float64); ok {
						sizeText = " " + strconv.FormatInt(int64(size), 10) + " B"
					}
					parts = append(parts, "[图片 image/"+format+sizeText+"]")
					continue
				}
				if encoded, err := json.MarshalIndent(item, "", "  "); err == nil {
					parts = append(parts, string(encoded))
				}
			default:
				if encoded, err := json.MarshalIndent(item, "", "  "); err == nil {
					parts = append(parts, string(encoded))
				}
			}
		}
		return strings.TrimSpace(strings.Join(nonEmptyStrings(parts), "\n"))
	case nil:
		return ""
	default:
		if encoded, err := json.MarshalIndent(content, "", "  "); err == nil {
			return string(encoded)
		}
	}
	return ""
}

func nonEmptyStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func truncateReasoningPreview(text string, maxChars int) string {
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	return strings.TrimRight(string(runes[:maxChars]), " ") + "..."
}

func extractActionNames(toolCalls any) []string {
	switch typed := toolCalls.(type) {
	case map[string]any:
		toolCalls = []any{typed}
	}
	items, _ := toolCalls.([]any)
	names := []string{}
	for _, rawItem := range items {
		item, _ := rawItem.(map[string]any)
		if item == nil {
			continue
		}
		name := ""
		if function, ok := item["function"].(map[string]any); ok {
			name = strings.TrimSpace(stringValue(function["name"]))
		}
		if name == "" {
			name = strings.TrimSpace(stringValue(item["name"]))
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func extractActionPreviewFromJSONPayload(payload map[string]any, maxActions int) string {
	names := []string{}
	if items, ok := payload["output_items"].([]any); ok {
		for _, rawItem := range items {
			item, _ := rawItem.(map[string]any)
			if item == nil {
				continue
			}
			name := ""
			switch stringValue(item["item_type"]) {
			case "FunctionCallItem":
				if call, ok := item["tool_call"].(map[string]any); ok {
					name = strings.TrimSpace(stringValue(call["func_name"]))
				}
			case "ProviderActivityItem":
				name = strings.TrimSpace(reasoningFirstNonEmpty(stringValue(item["provider_type"]), stringValue(item["action_type"])))
			}
			if name != "" {
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		if output, ok := payload["output"].(map[string]any); ok {
			names = extractActionNames(output["tool_calls"])
		}
	}
	if len(names) == 0 {
		return ""
	}
	shown := names
	if len(shown) > maxActions {
		shown = shown[:maxActions]
	}
	preview := "动作：" + strings.Join(shown, "、")
	if len(names) > maxActions {
		preview += " 等 " + strconv.Itoa(len(names)) + " 个"
	}
	return preview
}

func extractLLMErrorDisplayTitle(payload map[string]any) string {
	metadata, _ := payload["metadata"].(map[string]any)
	status := strings.TrimSpace(stringValue(metadata["status"]))
	labels := map[string]string{
		"failed": "请求失败", "final_failed": "最终失败", "retrying": "等待重试",
		"switching_model": "切换模型", "succeeded": "请求成功", "succeeded_after_retry": "重试后成功",
	}
	label := labels[status]
	if label == "" {
		label = status
	}
	if label == "" {
		label = "请求异常"
	}
	attempts, _ := payload["generation_attempts"].([]any)
	for index := len(attempts) - 1; index >= 0; index-- {
		attempt, _ := attempts[index].(map[string]any)
		if attempt == nil {
			continue
		}
		if errorInfo, ok := attempt["error"].(map[string]any); ok {
			if message := strings.TrimSpace(stringValue(errorInfo["message"])); message != "" {
				return label + " · " + message
			}
		}
	}
	return label
}

func extractSelectedExpressionPairs(payload map[string]any) ([]reasoningSelectedExpression, bool) {
	output := extractOutputTextFromJSONPayload(payload)
	if output == "" {
		return nil, false
	}
	parsed, err := llmjson.Parse(output, true, true)
	if err != nil {
		return nil, false
	}
	result, ok := parsed.(map[string]any)
	if !ok {
		return nil, false
	}
	rawIDs, ok := result["selected_ids"].([]any)
	if !ok {
		return nil, false
	}
	candidates := map[int64]reasoningSelectedExpression{}
	if requestItems, ok := payload["request_items"].([]any); ok {
		for _, rawItem := range requestItems {
			item, _ := rawItem.(map[string]any)
			if item == nil || stringValue(item["item_type"]) != "SystemMessageItem" {
				continue
			}
			parts, _ := item["parts"].([]any)
			texts := []string{}
			for _, rawPart := range parts {
				part, _ := rawPart.(map[string]any)
				if part != nil && stringValue(part["type"]) == "text" {
					if text, ok := part["text"].(string); ok {
						texts = append(texts, text)
					}
				}
			}
			for id, pair := range parseReasoningExpressionCandidates(strings.Join(texts, "\n")) {
				candidates[id] = pair
			}
		}
	}
	pairs := []reasoningSelectedExpression{}
	seen := map[int64]bool{}
	for _, rawID := range rawIDs {
		number, ok := rawID.(float64)
		if !ok || number != float64(int64(number)) {
			continue
		}
		id := int64(number)
		pair, ok := candidates[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		pairs = append(pairs, pair)
		if len(pairs) >= 5 {
			break
		}
	}
	if len(rawIDs) > 0 && len(candidates) == 0 {
		return nil, false
	}
	return pairs, true
}

var reasoningExpressionLinePattern = regexp.MustCompile("^([0-9]+): 情景=(.*) \\| 风格=(.*)$")

func parseReasoningExpressionCandidates(text string) map[int64]reasoningSelectedExpression {
	candidates := map[int64]reasoningSelectedExpression{}
	lines := strings.Split(text, "\n")
	var currentID int64
	currentSituation := ""
	currentStyle := ""
	flush := func() {
		if currentSituation == "" && currentStyle == "" {
			return
		}
		candidates[currentID] = reasoningSelectedExpression{Situation: strings.TrimSpace(currentSituation), Style: strings.TrimSpace(currentStyle)}
	}
	for _, line := range lines {
		if match := reasoningExpressionLinePattern.FindStringSubmatch(line); match != nil {
			flush()
			id, err := strconv.ParseInt(match[1], 10, 64)
			if err != nil {
				currentID, currentSituation, currentStyle = 0, "", ""
				continue
			}
			currentID = id
			currentSituation = match[2]
			currentStyle = match[3]
			continue
		}
		if currentSituation != "" || currentStyle != "" {
			currentSituation += "\n" + line
		}
	}
	flush()
	return candidates
}

func isJargonLearningUpdatePreviewPayload(payload map[string]any) bool {
	return stringValue(payload["record_type"]) == "jargon_learning_update" && payload["request_items"] == nil
}

func readJSONMap(path string) (map[string]any, bool) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var payload map[string]any
	if err := json.Unmarshal(content, &payload); err != nil {
		return nil, false
	}
	return payload, true
}
