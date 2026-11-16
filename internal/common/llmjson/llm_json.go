// Package llmjson 解析模型返回的 JSON，统一处理 Markdown 代码块包装
// 对齐 src/common/llm_json.py
package llmjson

import (
	"encoding/json"
	"regexp"
	"strings"

	"MaiGo/internal/common/jsonrepair"
)

// fencePattern 只剥离完整的代码块外壳，不完整输出留给解析器报错
var fencePattern = regexp.MustCompile("(?is)\\A```(?:json)?[ \t]*\\r?\\n(.*?)\\r?\\n```[ \t]*\\z")

// Normalize 移除完整的 JSON 代码块外壳
func Normalize(response string) string {
	text := strings.TrimSpace(response)
	if match := fencePattern.FindStringSubmatch(text); match != nil {
		return strings.TrimSpace(match[1])
	}
	return text
}

// Parse 解析模型 JSON；repair 显式启用修复，allowCodeFence 控制是否剥离代码块
func Parse(response string, repair bool, allowCodeFence bool) (any, error) {
	text := response
	if allowCodeFence {
		text = Normalize(response)
	}
	if repair {
		return jsonrepair.RepairPythonParity(text)
	}
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil, err
	}
	return value, nil
}

// Unmarshal 对齐 validate_llm_json：剥离代码块后再按结构校验
func Unmarshal(response string, target any) error {
	return json.Unmarshal([]byte(Normalize(response)), target)
}
