package learners

import (
	"strings"

	"github.com/bytedance/sonic"
)

type inferenceJSON struct {
	Meaning  string `json:"meaning"`
	NoInfo   bool   `json:"no_info"`
	IsJargon *bool  `json:"is_jargon"`
}

func parseInferenceJSON(raw string) (inferenceJSON, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return inferenceJSON{}, false
	}
	if obj, ok := decodeInferenceObject(raw); ok {
		return obj, true
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return inferenceJSON{}, false
	}
	obj, ok := decodeInferenceObject(raw[start : end+1])
	return obj, ok
}

func decodeInferenceObject(raw string) (inferenceJSON, bool) {
	var obj inferenceJSON
	if err := sonic.UnmarshalString(raw, &obj); err != nil {
		return inferenceJSON{}, false
	}
	return obj, true
}

// parseInferenceMap 宽松解析推断响应为字典，对齐 response_model 的 dict 解析
func parseInferenceMap(raw string) (map[string]any, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	if obj, ok := ParseJSONLoose(raw).(map[string]any); ok {
		return obj, true
	}
	return nil, false
}

// heuristicIsJargon 无 LLM is_jargon 字段时的默认：有有效含义且非 no_info 则视为黑话候选
func heuristicIsJargon(meaning string, noInfo bool) bool {
	if noInfo {
		return false
	}
	return strings.TrimSpace(meaning) != ""
}
