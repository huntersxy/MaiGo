package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	memutils "MaiGo/internal/A_memorix/core/utils"
)

const (
	promptVersionFingerprintSchema = 3
	chatExtraAttentionMarker       = "当前聊天额外注意事项："
	attentionBlockHeader           = "在该聊天中的注意事项："
)

var outputInstructionMarkers = []string{
	"请注意不要输出多余内容",
	"Please do not output any extra content",
	"余計な内容",
}

// extractGenerationFingerprints 对齐 reply_effect_fingerprint.extract_generation_fingerprints
func extractGenerationFingerprints(metadata map[string]any) (string, string, string) {
	monitor := mapValue(metadata["monitor_detail"])
	metrics := mapValue(monitor["metrics"])
	modelName := strings.TrimSpace(pyTextOrEmpty(metrics["model_name"]))
	promptPayload := monitor["request_messages"]
	if !pyTruthy(promptPayload) {
		promptPayload = monitor["prompt_text"]
	}
	if !pyTruthy(promptPayload) {
		promptPayload = ""
	}
	return modelName, fingerprintRequestPayload(promptPayload), fingerprintPromptVersion(promptPayload)
}

func fingerprintRequestPayload(promptPayload any) string {
	if !pyTruthy(promptPayload) {
		return ""
	}
	serialized, err := memutils.PyJSONMarshal(normalizeFingerprintNumbers(promptPayload))
	if err != nil {
		return ""
	}
	return sha256Hex(serialized)
}

func fingerprintPromptVersion(promptPayload any) string {
	items, ok := promptPayload.([]any)
	if !ok {
		return ""
	}
	systemMessages := make([]string, 0)
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		itemType := pyTextOrEmpty(item["item_type"])
		if itemType == "" {
			itemType = pyTextOrEmpty(item["type"])
		}
		role := pyTextOrEmpty(item["role"])
		if itemType != "SystemMessageItem" && strings.ToLower(role) != "system" {
			continue
		}
		parts, ok := item["parts"].([]any)
		if !ok {
			continue
		}
		textParts := make([]string, 0, len(parts))
		for _, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			partType := pyTextOrEmpty(part["type"])
			if partType != "text" && partType != "input_text" {
				continue
			}
			textParts = append(textParts, pyTextOrEmpty(part["text"]))
		}
		normalizedText := strings.TrimSpace(strings.Join(textParts, "\n"))
		if normalizedText != "" {
			systemMessages = append(systemMessages, normalizeSystemPromptForVersion(normalizedText))
		}
	}
	if len(systemMessages) == 0 {
		return ""
	}
	stablePayload, err := memutils.PyJSONMarshal(map[string]any{
		"schema":          promptVersionFingerprintSchema,
		"system_messages": systemMessages,
	})
	if err != nil {
		return ""
	}
	return sha256Hex(stablePayload)
}

func normalizeSystemPromptForVersion(systemPrompt string) string {
	markerIndex := strings.Index(systemPrompt, chatExtraAttentionMarker)
	if markerIndex < 0 {
		return normalizePromptWhitespace(systemPrompt)
	}
	suffixIndex := len(systemPrompt)
	for _, marker := range outputInstructionMarkers {
		if index := strings.Index(systemPrompt[markerIndex:], marker); index >= 0 {
			index += markerIndex
			if index < suffixIndex {
				suffixIndex = index
			}
		}
	}
	prefix := strings.TrimRightFunc(systemPrompt[:markerIndex], unicode.IsSpace)
	if strings.HasSuffix(prefix, attentionBlockHeader) {
		prefix = strings.TrimRightFunc(strings.TrimSuffix(prefix, attentionBlockHeader), unicode.IsSpace)
	}
	suffix := strings.TrimLeftFunc(systemPrompt[suffixIndex:], unicode.IsSpace)
	parts := make([]string, 0, 2)
	if prefix != "" {
		parts = append(parts, prefix)
	}
	if suffix != "" {
		parts = append(parts, suffix)
	}
	return normalizePromptWhitespace(strings.Join(parts, "\n"))
}

func normalizePromptWhitespace(systemPrompt string) string {
	return strings.Join(strings.Fields(systemPrompt), " ")
}

// normalizeFingerprintNumbers 把 json.Number 归一为 Python json.loads 后的 int/float
func normalizeFingerprintNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		text := string(typed)
		if strings.ContainsAny(text, ".eE") {
			if parsed, err := strconv.ParseFloat(text, 64); err == nil {
				return parsed
			}
			return value
		}
		if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
			return parsed
		}
		return value
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = normalizeFingerprintNumbers(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = normalizeFingerprintNumbers(item)
		}
		return result
	default:
		return value
	}
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func pyTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case []byte:
		return len(typed) > 0
	case json.Number:
		number, err := typed.Float64()
		return err == nil && number != 0
	case float64:
		return typed != 0
	case float32:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return value != nil
	}
}

func pyTextOrEmpty(value any) string {
	if !pyTruthy(value) {
		return ""
	}
	return pyString(value)
}

func mapValue(value any) map[string]any {
	if mapped, ok := value.(map[string]any); ok {
		return mapped
	}
	return map[string]any{}
}

func marshalRecordJSON(record map[string]any) (string, error) {
	return memutils.PyJSONMarshal(record)
}

func decodeRecordObject(serialized string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(serialized))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("回复效果 record_json 包含多余内容")
		}
		return nil, err
	}
	payload, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("回复效果记录必须是 JSON 对象")
	}
	return payload, nil
}
