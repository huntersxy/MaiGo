// Package jsonrepair 封装 github.com/lee00jx/json-repair-go，并补齐与 MaiBot
// 产品 pin 的 Python json_repair 0.60.1 的差异。
//
// 两处调用方共用本包，避免各自维护一套「修复 + 归一化」实现：
//   - internal/llm_models/model_client：工具调用参数修复
//   - internal/learners：表达/黑话学习结果修复（对齐 _try_parse）
package jsonrepair

import (
	"encoding/json"
	"strings"

	lib "github.com/lee00jx/json-repair-go"
)

// Repair 调用 Go 版 json_repair 并返回普通 Go 值（map[string]any / []any / 标量）。
//
// Python 侧固定用法是 repair_json(text) 后再 json.loads；Go 库直接返回对象，
// 这里把库的 OrderedMap 递归转成 map[string]any，保留数字的 int64/float64 类型。
func Repair(raw string) (any, error) {
	repaired, _, err := lib.RepairJSON(raw, lib.Options{
		ReturnObjects: true,
		Logging:       false,
	})
	if err != nil {
		return nil, err
	}
	return PlainValue(repaired), nil
}

// RepairPythonParity 按 Python json_repair 0.60.1 的口径修复，用于工具调用参数。
//
// 与 Repair 的区别是输入归一化：
//   - 去掉 UTF-8 BOM
//   - 剥离注释，避免注释内容干扰后续字面量扫描
//   - 把裸 None 字面量改成同样文本的字符串（0.60.1 只识别 true/false/null）
//   - 不补 missing opening object 的花括号：0.60.1 对 "a": 1} 返回空串
func RepairPythonParity(raw string) (any, error) {
	return Repair(NormalizeForPythonParity(raw))
}

// NormalizeForPythonParity 只做 Python repair_json 自带、但 Go 库缺失或行为不同的预处理。
func NormalizeForPythonParity(raw string) string {
	text := strings.TrimPrefix(raw, "\uFEFF")
	if json.Valid([]byte(text)) {
		return text
	}
	text = StripComments(text)
	if json.Valid([]byte(text)) {
		return text
	}
	return QuotePythonNoneLiterals(text)
}

// QuotePythonNoneLiterals 把裸 None 字面量改成同样文本的字符串，中和 Go 库额外的 none→null 支持。
//
// Python json_repair 0.60.1 的字面量表只有 true/false/null，裸 None 会按缺引号字符串保留原文本；
// Go 库额外支持 none→nil，这里只在会触发该 literal 的位置加引号：值位置（前一个非空白字符
// 为 : , [ ( {）、完整 token（前后不是字母数字下划线）、后接空白/分隔符/EOF。
func QuotePythonNoneLiterals(text string) string {
	var out strings.Builder
	out.Grow(len(text) + 8)
	for index := 0; index < len(text); {
		ch := text[index]
		if ch == '"' || ch == '\'' {
			quote := ch
			out.WriteByte(ch)
			index++
			for index < len(text) {
				current := text[index]
				out.WriteByte(current)
				index++
				if current == '\\' && index < len(text) {
					out.WriteByte(text[index])
					index++
					continue
				}
				if current == quote {
					break
				}
			}
			continue
		}
		if strings.HasPrefix(text[index:], "“") {
			end := len(text)
			if closeIndex := strings.Index(text[index+len("“"):], "”"); closeIndex >= 0 {
				end = index + len("“") + closeIndex + len("”")
			}
			out.WriteString(text[index:end])
			index = end
			continue
		}
		if end, ok := pythonNoneLiteralEnd(text, index); ok {
			out.WriteByte('"')
			out.WriteString(text[index:end])
			out.WriteByte('"')
			index = end
			continue
		}
		out.WriteByte(ch)
		index++
	}
	return out.String()
}

// pythonNoneLiteralEnd 判断 index 处是否为需要加引号的裸 None；返回 token 结束位置。
func pythonNoneLiteralEnd(text string, index int) (int, bool) {
	const token = "none"
	if index+len(token) > len(text) || !strings.EqualFold(text[index:index+len(token)], token) {
		return 0, false
	}
	if index > 0 && isJSONIdentifierByte(text[index-1]) {
		return 0, false
	}
	end := index + len(token)
	if end < len(text) {
		next := text[end]
		if isJSONIdentifierByte(next) || !isJSONSeparatorOrSpace(next) {
			return 0, false
		}
	}
	previousSignificant := byte(0)
	for cursor := index - 1; cursor >= 0; cursor-- {
		switch text[cursor] {
		case ' ', '\t', '\n', '\r':
			continue
		}
		previousSignificant = text[cursor]
		break
	}
	switch previousSignificant {
	case ':', ',', '[', '(', '{':
		return end, true
	default:
		return 0, false
	}
}

func isJSONIdentifierByte(ch byte) bool {
	return ch == '_' || ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

func isJSONSeparatorOrSpace(ch byte) bool {
	switch ch {
	case ' ', '\t', '\n', '\r', ',', ')', ']', '}':
		return true
	default:
		return false
	}
}

// StripComments 剥离字符串外的 //、/* */ 与 # 注释，字符串内的内容保持原样。
func StripComments(text string) string {
	var out strings.Builder
	for index := 0; index < len(text); {
		ch := text[index]
		if ch == '"' || ch == '\'' {
			quote := ch
			out.WriteByte(ch)
			index++
			for index < len(text) {
				current := text[index]
				out.WriteByte(current)
				index++
				if current == '\\' && index < len(text) {
					out.WriteByte(text[index])
					index++
					continue
				}
				if current == quote {
					break
				}
			}
			continue
		}
		if ch == '/' && index+1 < len(text) && text[index+1] == '/' {
			index += 2
			for index < len(text) && text[index] != '\n' {
				index++
			}
			out.WriteByte('\n')
			continue
		}
		if ch == '/' && index+1 < len(text) && text[index+1] == '*' {
			index += 2
			for index+1 < len(text) && !(text[index] == '*' && text[index+1] == '/') {
				index++
			}
			if index+1 < len(text) {
				index += 2
			} else {
				index = len(text)
			}
			out.WriteByte(' ')
			continue
		}
		if ch == '#' {
			for index < len(text) && text[index] != '\n' {
				index++
			}
			out.WriteByte('\n')
			continue
		}
		out.WriteByte(ch)
		index++
	}
	return out.String()
}

// PlainValue 把库的 OrderedMap 递归转成 map[string]any，保留数字的 int64/float64 类型。
func PlainValue(value any) any {
	switch typed := value.(type) {
	case *lib.OrderedMap:
		converted := make(map[string]any, typed.Len())
		for _, key := range typed.Keys() {
			converted[key] = PlainValue(typed.GetOrNil(key))
		}
		return converted
	case map[string]any:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			converted[key] = PlainValue(item)
		}
		return converted
	case []any:
		converted := make([]any, len(typed))
		for index, item := range typed {
			converted[index] = PlainValue(item)
		}
		return converted
	default:
		return value
	}
}
