package context

import (
	"strconv"
	"strings"
)

// pythonJSONDumps 按 Python json.dumps 默认语义输出 JSON：键按插入顺序、
// ensure_ascii=False、布尔/null 小写、浮点按 repr 输出。
// 只用于字符计量，不参与协议传输。
func pythonJSONDumps(value any, itemSep, keySep string) string {
	var builder strings.Builder
	writePythonJSON(&builder, value, itemSep, keySep)
	return builder.String()
}

func writePythonJSON(builder *strings.Builder, value any, itemSep, keySep string) {
	switch typed := value.(type) {
	case nil:
		builder.WriteString("null")
	case bool:
		if typed {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case string:
		writePythonJSONString(builder, typed)
	case int:
		builder.WriteString(strconv.Itoa(typed))
	case int64:
		builder.WriteString(strconv.FormatInt(typed, 10))
	case float64:
		builder.WriteString(strconv.FormatFloat(typed, 'g', -1, 64))
	case []string:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = item
		}
		writePythonJSON(builder, items, itemSep, keySep)
	case []any:
		builder.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				builder.WriteString(itemSep)
			}
			writePythonJSON(builder, item, itemSep, keySep)
		}
		builder.WriteByte(']')
	case map[string]any:
		builder.WriteByte('{')
		first := true
		for key, item := range typed {
			if !first {
				builder.WriteString(itemSep)
			}
			first = false
			writePythonJSONString(builder, key)
			builder.WriteString(keySep)
			writePythonJSON(builder, item, itemSep, keySep)
		}
		builder.WriteByte('}')
	default:
		builder.WriteString("null")
	}
}

func writePythonJSONString(builder *strings.Builder, text string) {
	builder.WriteByte('"')
	for _, char := range text {
		switch char {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		default:
			if char < 0x20 {
				builder.WriteString(`\u00`)
				builder.WriteString(strconv.FormatInt(int64(char)>>4, 16))
				builder.WriteString(strconv.FormatInt(int64(char)&0xF, 16))
				continue
			}
			builder.WriteRune(char)
		}
	}
	builder.WriteByte('"')
}
