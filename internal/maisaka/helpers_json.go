package maisaka

import (
	"encoding/json"
	"sort"
)

// marshalArguments 序列化工具参数，失败时退化为空对象
func marshalArguments(arguments map[string]any) string {
	if len(arguments) == 0 {
		return "{}"
	}
	data, err := json.Marshal(arguments)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// truncateRunes 按字符数截断，避免多字节文本被切坏
func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func sortInts(values []int) { sort.Ints(values) }
