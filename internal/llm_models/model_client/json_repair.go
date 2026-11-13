package model_client

// 工具调用参数的 JSON 修复
// 对等 MaiBot src/llm_models/model_client/openai_client.py 的 _parse_tool_arguments 所用 json_repair 0.60.1
//（产品 pin 见 requirements.txt:13）；对齐记录见 docs/migration/verification/VERIFY_JSON_REPAIR_0601_2026-11-06.md

import (
	"encoding/json"
	"strings"

	jsonrepair "github.com/lee00jx/json-repair-go"
)

// chatRepairJSON 调用 Go 版 json_repair，并补齐与 MaiBot 产品 pin 的 Python json_repair 0.60.1 的差异：
//   - 去掉 UTF-8 BOM
//   - 剥离注释，避免注释内容干扰后续字面量扫描
//   - 把裸 None 字面量改成同样文本的字符串（0.60.1 只识别 true/false/null，None 按缺引号字符串处理）
//   - 不再补 missing opening object 的花括号：0.60.1 对 "a": 1} 返回空串，补花括号会让 Go 侧伪装成修复成功
func chatRepairJSON(raw string) (any, error) {
	normalized := chatNormalizeJSONRepairInput(raw)
	repaired, _, err := jsonrepair.RepairJSON(normalized, jsonrepair.Options{
		ReturnObjects: true,
		Logging:       false,
	})
	if err != nil {
		return nil, err
	}
	return chatPlainJSONValue(repaired), nil
}

// chatNormalizeJSONRepairInput 只做 Python repair_json 自带、但 Go 库缺失或行为不同的预处理。
func chatNormalizeJSONRepairInput(raw string) string {
	text := strings.TrimPrefix(raw, "\ufeff")
	if json.Valid([]byte(text)) {
		return text
	}
	text = chatStripJSONComments(text)
	if json.Valid([]byte(text)) {
		return text
	}
	return chatQuotePythonNoneLiterals(text)
}

// chatQuotePythonNoneLiterals 把裸 None 字面量改成同样文本的字符串，中和 Go 库额外的 none→null 支持。
//
// Python json_repair 0.60.1 的字面量表只有 true/false/null（parse_string_helpers/parse_boolean_or_null.py），
// 裸 None 会按缺引号字符串保留原文本；Go 库 v0.1.0 额外支持 none→nil（parse_string.go），
// 这里只在 Go 库会触发该 literal 的位置加引号：值位置（前一个非空白字符为 : , [ ( {）、
// 完整 token（前后不是字母数字下划线）、后接空白/分隔符/EOF，避免影响普通字符串与注释内容。
func chatQuotePythonNoneLiterals(text string) string {
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
		if strings.HasPrefix(text[index:], "\u201c") {
			end := len(text)
			if closeIndex := strings.Index(text[index+len("\u201c"):], "\u201d"); closeIndex >= 0 {
				end = index + len("\u201c") + closeIndex + len("\u201d")
			}
			out.WriteString(text[index:end])
			index = end
			continue
		}
		if end, ok := chatPythonNoneLiteralEnd(text, index); ok {
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

// chatPythonNoneLiteralEnd 判断 index 处是否为需要加引号的裸 None；返回 token 结束位置。
func chatPythonNoneLiteralEnd(text string, index int) (int, bool) {
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

// chatStripJSONComments 剥离字符串外的 //、/* */ 与 # 注释，字符串内的内容保持原样。
func chatStripJSONComments(text string) string {
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

// chatPlainJSONValue 把库的 OrderedMap 递归转成 map[string]any，保留数字的 int64/float64 类型。
func chatPlainJSONValue(value any) any {
	switch typed := value.(type) {
	case *jsonrepair.OrderedMap:
		converted := make(map[string]any, typed.Len())
		for _, key := range typed.Keys() {
			converted[key] = chatPlainJSONValue(typed.GetOrNil(key))
		}
		return converted
	case map[string]any:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			converted[key] = chatPlainJSONValue(item)
		}
		return converted
	case []any:
		converted := make([]any, len(typed))
		for index, item := range typed {
			converted[index] = chatPlainJSONValue(item)
		}
		return converted
	default:
		return value
	}
}
