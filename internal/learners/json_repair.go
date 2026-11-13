package learners

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bytedance/sonic"
)

// ParseJSONLoose 宽松解析 LLM 返回的 JSON，对齐 expression_utils._try_parse
// 先直接解析，失败后修复单引号、裸键、尾逗号、Python 字面量与截断括号再试
func ParseJSONLoose(text string) any {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if value := decodeLooseJSON(text); value != nil {
		return value
	}
	repaired := repairJSONText(text)
	if repaired == text {
		return nil
	}
	return decodeLooseJSON(repaired)
}

func decodeLooseJSON(text string) any {
	var value any
	if err := sonic.Unmarshal([]byte(text), &value); err != nil {
		return nil
	}
	return value
}

func repairJSONText(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return text
	}
	if start, end := jsonBounds(text); start >= 0 && end > start {
		text = text[start : end+1]
	}
	text = normalizeJSONQuotes(text)
	text = replaceJSONLiterals(text)
	text = quoteBareJSONKeys(text)
	text = stripTrailingJSONCommas(text)
	text = balanceJSONDelimiters(text)
	return stripTrailingJSONCommas(text)
}

// jsonBounds 截取首个 JSON 容器到最后一个闭合符号，丢弃前后说明文字
func jsonBounds(text string) (int, int) {
	start := -1
	for _, marker := range []string{"{", "["} {
		if index := strings.Index(text, marker); index >= 0 && (start < 0 || index < start) {
			start = index
		}
	}
	end := -1
	for _, marker := range []string{"}", "]"} {
		if index := strings.LastIndex(text, marker); index > end {
			end = index
		}
	}
	return start, end
}

func normalizeJSONQuotes(text string) string {
	var b strings.Builder
	inDouble, inSingle, escape := false, false, false
	for _, r := range text {
		if escape {
			b.WriteRune(r)
			escape = false
			continue
		}
		if r == '\\' {
			b.WriteRune(r)
			escape = true
			continue
		}
		if inDouble {
			if r == '"' {
				inDouble = false
			}
			b.WriteRune(r)
			continue
		}
		if inSingle {
			switch r {
			case '\'', '”':
				inSingle = false
				b.WriteRune('"')
			case '"':
				b.WriteString("\\\"")
			default:
				b.WriteRune(r)
			}
			continue
		}
		switch r {
		case '"':
			inDouble = true
			b.WriteRune(r)
		case '\'', '“':
			inSingle = true
			b.WriteRune('"')
		default:
			b.WriteRune(r)
		}
	}
	if inDouble || inSingle {
		b.WriteRune('"')
	}
	return b.String()
}

// replaceJSONLiterals 把 Python 风格 True/False/None 换成 JSON 字面量
func replaceJSONLiterals(text string) string {
	var b strings.Builder
	inDouble, inSingle, escape := false, false, false
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if escape {
			b.WriteRune(r)
			escape = false
			i += size
			continue
		}
		if r == '\\' {
			b.WriteRune(r)
			escape = true
			i += size
			continue
		}
		if inDouble {
			if r == '"' {
				inDouble = false
			}
			b.WriteRune(r)
			i += size
			continue
		}
		if inSingle {
			if r == '\'' || r == '”' {
				inSingle = false
			}
			b.WriteRune(r)
			i += size
			continue
		}
		switch r {
		case '"':
			inDouble = true
		case '\'', '“':
			inSingle = true
		}
		if isASCIILetter(r) {
			j := i
			for j < len(text) {
				r2, size2 := utf8.DecodeRuneInString(text[j:])
				if !isASCIILetter(r2) && !unicode.IsDigit(r2) && r2 != '_' {
					break
				}
				j += size2
			}
			word := text[i:j]
			switch word {
			case "None":
				b.WriteString("null")
			case "True":
				b.WriteString("true")
			case "False":
				b.WriteString("false")
			default:
				b.WriteString(word)
			}
			i = j
			continue
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

// quoteBareJSONKeys 给 {key: 或 ,key: 形式的裸键补引号
func quoteBareJSONKeys(text string) string {
	var b strings.Builder
	inDouble, inSingle, escape := false, false, false
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if escape {
			b.WriteRune(r)
			escape = false
			i += size
			continue
		}
		if r == '\\' {
			b.WriteRune(r)
			escape = true
			i += size
			continue
		}
		if inDouble {
			if r == '"' {
				inDouble = false
			}
			b.WriteRune(r)
			i += size
			continue
		}
		if inSingle {
			if r == '\'' || r == '”' {
				inSingle = false
			}
			b.WriteRune(r)
			i += size
			continue
		}
		switch r {
		case '"':
			inDouble = true
			b.WriteRune(r)
			i += size
			continue
		case '\'', '“':
			inSingle = true
			b.WriteRune(r)
			i += size
			continue
		case '{', ',':
			b.WriteRune(r)
			i += size
			j := skipJSONSpace(text, i)
			b.WriteString(text[i:j])
			i = j
			start := i
			for i < len(text) {
				r2, size2 := utf8.DecodeRuneInString(text[i:])
				if !isKeyRune(r2) {
					break
				}
				i += size2
			}
			if i > start {
				keyEnd := i
				colonAt := skipJSONSpace(text, i)
				if colonAt < len(text) && text[colonAt] == ':' {
					b.WriteString("\"")
					b.WriteString(text[start:keyEnd])
					b.WriteString("\"")
					i = keyEnd
					continue
				}
				i = start
			}
			continue
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

func stripTrailingJSONCommas(text string) string {
	var b strings.Builder
	inDouble, inSingle, escape := false, false, false
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if escape {
			b.WriteRune(r)
			escape = false
			i += size
			continue
		}
		if r == '\\' {
			b.WriteRune(r)
			escape = true
			i += size
			continue
		}
		if inDouble {
			if r == '"' {
				inDouble = false
			}
			b.WriteRune(r)
			i += size
			continue
		}
		if inSingle {
			if r == '\'' || r == '”' {
				inSingle = false
			}
			b.WriteRune(r)
			i += size
			continue
		}
		switch r {
		case '"':
			inDouble = true
		case '\'', '“':
			inSingle = true
		}
		if r == ',' {
			next := skipJSONSpace(text, i+size)
			if next >= len(text) {
				i += size
				continue
			}
			nextRune, _ := utf8.DecodeRuneInString(text[next:])
			if nextRune == '}' || nextRune == ']' {
				i += size
				continue
			}
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

func balanceJSONDelimiters(text string) string {
	stack := make([]rune, 0, 8)
	inDouble, inSingle, escape := false, false, false
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if escape {
			escape = false
			i += size
			continue
		}
		if r == '\\' {
			escape = true
			i += size
			continue
		}
		if inDouble {
			if r == '"' {
				inDouble = false
			}
			i += size
			continue
		}
		if inSingle {
			if r == '\'' || r == '”' {
				inSingle = false
			}
			i += size
			continue
		}
		switch r {
		case '"':
			inDouble = true
		case '\'', '“':
			inSingle = true
		case '{', '[':
			stack = append(stack, r)
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
		i += size
	}
	if len(stack) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(text)
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			b.WriteString("}")
		} else {
			b.WriteString("]")
		}
	}
	return b.String()
}

func skipJSONSpace(text string, index int) int {
	for index < len(text) {
		switch text[index] {
		case ' ', '\t', '\n', '\r':
			index++
		default:
			return index
		}
	}
	return index
}

func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func isKeyRune(r rune) bool {
	return isASCIILetter(r) || unicode.IsDigit(r) || r == '_'
}
