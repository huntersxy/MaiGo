package learners

// 表达/黑话学习结果解析的 JSON 修复。
//
// Python 对照：src/learners/expression_utils.py 的 _try_parse
//   json.loads(text) 失败后 repair_json(text) 再 json.loads，仍失败返回 None
// 这里直接复用 internal/common/jsonrepair 的 Go 版 json_repair，
// 不再维护手写的宽松解析（旧实现约 400 行，边界与 Python 口径容易漂移）。

import (
	"encoding/json"

	"maibot-go/internal/common/jsonrepair"

	"github.com/bytedance/sonic"
)

// ParseJSONLoose 宽松解析 LLM 返回的 JSON，对齐 _try_parse。
// 解析失败返回 nil；调用方按 nil 走各自的兜底文案。
func ParseJSONLoose(text string) any {
	text = trimJSONText(text)
	if text == "" {
		return nil
	}
	if value, ok := decodeStrictJSON(text); ok {
		return value
	}
	value, err := jsonrepair.RepairPythonParity(text)
	if err != nil {
		return nil
	}
	return value
}

// trimJSONText 只做首尾空白裁剪，保留内部结构交给修复库处理。
func trimJSONText(text string) string {
	start := 0
	for start < len(text) {
		switch text[start] {
		case ' ', '\t', '\r', '\n':
			start++
			continue
		}
		break
	}
	end := len(text)
	for end > start {
		switch text[end-1] {
		case ' ', '\t', '\r', '\n':
			end--
			continue
		}
		break
	}
	return text[start:end]
}

// decodeStrictJSON 先用 sonic 做严格解析，命中时避免进入修复路径。
func decodeStrictJSON(text string) (any, bool) {
	if !json.Valid([]byte(text)) {
		return nil, false
	}
	var value any
	if err := sonic.Unmarshal([]byte(text), &value); err != nil {
		return nil, false
	}
	return value, true
}
