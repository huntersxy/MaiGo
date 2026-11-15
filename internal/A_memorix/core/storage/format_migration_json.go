package storage

// A_memorix 存储格式迁移的 JSON 工具
// 输出与 Python json.dumps(ensure_ascii=False, sort_keys=True) 对齐：键按字典序、分隔符为 ", " 与 ": "，
// 这样迁移出的文件内容不会因为编码器差异产生无意义改动

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	memorixutils "MaiGo/internal/A_memorix/core/utils"
)

// pythonJSONDumps 对齐 _json_dumps：紧凑输出、键排序、保留非 ASCII 字符
func pythonJSONDumps(value any) (string, error) {
	var buffer bytes.Buffer
	if err := writePythonJSON(&buffer, value, 0, -1); err != nil {
		return "", err
	}
	return buffer.String(), nil
}

// pythonJSONIndent 对齐 json.dump(..., indent=2) 的排版
func pythonJSONIndent(value any, indent int) (string, error) {
	var buffer bytes.Buffer
	if err := writePythonJSON(&buffer, value, 0, indent); err != nil {
		return "", err
	}
	return buffer.String(), nil
}

// writePythonJSON 按 indent 决定紧凑或缩进排版，indent 小于 0 表示紧凑
func writePythonJSON(buffer *bytes.Buffer, value any, level int, indent int) error {
	switch typed := value.(type) {
	case nil:
		buffer.WriteString("null")
	case bool:
		if typed {
			buffer.WriteString("true")
		} else {
			buffer.WriteString("false")
		}
	case string:
		writePythonJSONString(buffer, typed)
	case int:
		buffer.WriteString(strconv.Itoa(typed))
	case int64:
		buffer.WriteString(strconv.FormatInt(typed, 10))
	case float64:
		buffer.WriteString(pythonFloatRepr(typed))
	case map[string]any:
		return writePythonJSONObject(buffer, typed, level, indent)
	case []any:
		return writePythonJSONArray(buffer, typed, level, indent)
	case pickleTupleValue:
		return writePythonJSONArray(buffer, []any(typed), level, indent)
	case pickleSet, *pickleSet:
		return fmt.Errorf("JSON 无法序列化 set 类型")
	default:
		return fmt.Errorf("JSON 无法序列化 %s 类型", pythonTypeName(value))
	}
	return nil
}

// writePythonJSONObject 输出排序后的对象
func writePythonJSONObject(buffer *bytes.Buffer, object map[string]any, level int, indent int) error {
	if len(object) == 0 {
		buffer.WriteString("{}")
		return nil
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	buffer.WriteByte('{')
	for index, key := range keys {
		if index > 0 {
			buffer.WriteByte(',')
			if indent < 0 {
				buffer.WriteByte(' ')
			}
		}
		if indent >= 0 {
			buffer.WriteByte('\n')
			buffer.WriteString(strings.Repeat(" ", indent*(level+1)))
		}
		writePythonJSONString(buffer, key)
		buffer.WriteString(": ")
		if err := writePythonJSON(buffer, object[key], level+1, indent); err != nil {
			return err
		}
	}
	if indent >= 0 {
		buffer.WriteByte('\n')
		buffer.WriteString(strings.Repeat(" ", indent*level))
	}
	buffer.WriteByte('}')
	return nil
}

// writePythonJSONArray 输出数组，缩进排版与 Python 一致
func writePythonJSONArray(buffer *bytes.Buffer, items []any, level int, indent int) error {
	if len(items) == 0 {
		buffer.WriteString("[]")
		return nil
	}
	buffer.WriteByte('[')
	for index, item := range items {
		if index > 0 {
			buffer.WriteByte(',')
			if indent < 0 {
				buffer.WriteByte(' ')
			}
		}
		if indent >= 0 {
			buffer.WriteByte('\n')
			buffer.WriteString(strings.Repeat(" ", indent*(level+1)))
		}
		if err := writePythonJSON(buffer, item, level+1, indent); err != nil {
			return err
		}
	}
	if indent >= 0 {
		buffer.WriteByte('\n')
		buffer.WriteString(strings.Repeat(" ", indent*level))
	}
	buffer.WriteByte(']')
	return nil
}

// writePythonJSONString 输出字符串字面量，关闭 HTML 转义以对齐 ensure_ascii=False
func writePythonJSONString(buffer *bytes.Buffer, text string) {
	var quoted bytes.Buffer
	encoder := json.NewEncoder(&quoted)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(text)
	buffer.Write(bytes.TrimRight(quoted.Bytes(), "\n"))
}

// pythonFloatRepr 对齐 Python 浮点 repr：有效指数落在 [-4, 16) 时用定点，其余用科学计数法
func pythonFloatRepr(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	case value == 0:
		if math.Signbit(value) {
			return "-0.0"
		}
		return "0.0"
	}

	exponent := 0
	text := strconv.FormatFloat(value, 'e', -1, 64)
	if index := strings.IndexByte(text, 'e'); index >= 0 {
		exponent, _ = strconv.Atoi(text[index+1:])
	}

	var out string
	if exponent >= -4 && exponent < 16 {
		out = strconv.FormatFloat(value, 'f', -1, 64)
	} else {
		out = strconv.FormatFloat(value, 'e', -1, 64)
	}
	if !strings.ContainsAny(out, ".eE") {
		out += ".0"
	}
	return out
}

// decodeJSONValue 对齐 json.loads：整数解析为 int64，小数解析为 float64
func decodeJSONValue(text string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("JSON 存在多余内容")
		}
		return nil, err
	}
	return normalizeJSONNumbers(value), nil
}

// readJSONDict 对齐 _read_json_dict：必须是 JSON 对象
func readJSONDict(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	value, err := decodeJSONValue(string(data))
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JSON 元数据必须是对象: %s", path)
	}
	return object, nil
}

// writeJSONFile 对齐 _write_json：原子写入、缩进 2、结尾换行
func writeJSONFile(path string, payload map[string]any) error {
	text, err := pythonJSONIndent(payload, 2)
	if err != nil {
		return err
	}
	return memorixutils.AtomicWriteFile(path, 0o666, func(file *os.File) error {
		if _, err := file.WriteString(text + "\n"); err != nil {
			return err
		}
		return nil
	})
}

// migrationSummaryJSON 把完成摘要写成 Python 风格的 JSON 文本
func migrationSummaryJSON(summary *FormatMigrationSummary) (string, error) {
	raw, err := json.Marshal(summary)
	if err != nil {
		return "", err
	}
	value, err := decodeJSONValue(string(raw))
	if err != nil {
		return "", err
	}
	return pythonJSONDumps(value)
}

// pythonTruthy 对齐 Python 的真值判断
func pythonTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case string:
		return typed != ""
	case []byte:
		return len(typed) > 0
	case []any:
		return len(typed) > 0
	case pickleTupleValue:
		return len(typed) > 0
	case pickleSet:
		return len(typed.items) > 0
	case *pickleSet:
		return typed != nil && len(typed.items) > 0
	case map[string]any:
		return len(typed) > 0
	}
	return true
}

// pythonInt 对齐 Python int() 对基础标量的转换
func pythonInt(value any) (int, error) {
	switch typed := value.(type) {
	case bool:
		if typed {
			return 1, nil
		}
		return 0, nil
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, fmt.Errorf("无法把 %v 转换为整数", typed)
		}
		return int(typed), nil
	case string:
		text := strings.ReplaceAll(strings.TrimSpace(typed), "_", "")
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("无法把 %q 转换为整数", typed)
		}
		return int(parsed), nil
	}
	return 0, fmt.Errorf("无法把 %s 类型转换为整数", pythonTypeName(value))
}

// pythonStr 对齐 Python str() 对基础容器的输出，供边键与关系哈希归一使用
func pythonStr(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return pythonFloatRepr(typed)
	case []byte:
		return "b'" + string(typed) + "'"
	case []any:
		return "[" + joinPythonStr(typed) + "]"
	case pickleTupleValue:
		items := []any(typed)
		if len(items) == 1 {
			return "(" + pythonStr(items[0]) + ",)"
		}
		return "(" + joinPythonStr(items) + ")"
	case pickleSet:
		return "{" + joinPythonStr(typed.items) + "}"
	case *pickleSet:
		if typed == nil {
			return "None"
		}
		return "{" + joinPythonStr(typed.items) + "}"
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, "'"+key+"': "+pythonStr(typed[key]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(value)
}

// joinPythonStr 用 ", " 连接各元素的 str 结果
func joinPythonStr(items []any) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, pythonStr(item))
	}
	return strings.Join(parts, ", ")
}

// pythonTypeName 对齐 type(value).__name__ 的常用取值
func pythonTypeName(value any) string {
	switch value.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case []byte:
		return "bytes"
	case int, int64:
		return "int"
	case float64:
		return "float"
	case []any:
		return "list"
	case pickleTupleValue:
		return "tuple"
	case pickleSet, *pickleSet:
		return "set"
	case map[string]any:
		return "dict"
	}
	return fmt.Sprintf("%T", value)
}
