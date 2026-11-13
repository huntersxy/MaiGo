package storage

// A_memorix Episode 行整形与 Python 兼容取值/序列化
// 对齐 Python core/storage/metadata_episode.py 依赖的通用助手（_episode_row_to_dict / str() / float() / json.dumps）

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// episodeRunTx 在指定连接上开启事务；conn 为 nil 时使用存储主连接
func (s *Store) episodeRunTx(immediate bool, conn *Conn, fn func(tx *Tx) error) error {
	if conn == nil {
		return s.Transaction(immediate, fn)
	}
	return TransactionOn(conn, immediate, fn)
}

// episodeLeaseToken 生成 32 位小写十六进制租约令牌，对齐 Python uuid.uuid4().hex
// 未复用 imageUUIDHex 以免 Episode 模块依赖 file 模块内部助手
func episodeLeaseToken() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%032x", nowTimestamp())
	}
	return hex.EncodeToString(buf[:])
}

// episodeRowToDict 对齐 Python _episode_row_to_dict：把 *_json 列展开为列表
func episodeRowToDict(row map[string]any) map[string]any {
	if row == nil {
		return nil
	}
	data := make(map[string]any, len(row)+3)
	for key, value := range row {
		data[key] = value
	}
	data["participants"] = episodeLoadJSONList(data["participants_json"])
	data["keywords"] = episodeLoadJSONList(data["keywords_json"])
	data["evidence_ids"] = episodeLoadJSONList(data["evidence_ids_json"])
	delete(data, "participants_json")
	delete(data, "keywords_json")
	delete(data, "evidence_ids_json")
	return data
}

// episodeLoadJSONList 对齐 Python _episode_row_to_dict 内部的 _load_list
func episodeLoadJSONList(raw any) []any {
	if raw == nil {
		return []any{}
	}
	text, ok := raw.(string)
	if !ok || text == "" {
		return []any{}
	}
	if list, ok := jsonLoads(text, []any{}).([]any); ok {
		return list
	}
	return []any{}
}

// episodePyStr 对齐 Python str(value)
// 嵌套容器退化为 fmt.Sprint（Episode 载荷里不会出现容器型标量）
func episodePyStr(value any) string {
	switch v := value.(type) {
	case nil:
		return "None"
	case string:
		return v
	case bool:
		if v {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return episodePythonFloatRepr(v)
	case float32:
		return episodePythonFloatRepr(float64(v))
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(value)
	}
}

// episodeStrOrEmpty 对齐 Python str(value or "")
func episodeStrOrEmpty(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if !v {
			return ""
		}
		return "True"
	case int:
		if v == 0 {
			return ""
		}
		return strconv.Itoa(v)
	case int64:
		if v == 0 {
			return ""
		}
		return strconv.FormatInt(v, 10)
	case float64:
		if v == 0 {
			return ""
		}
		return episodePythonFloatRepr(v)
	case json.Number:
		return v.String()
	default:
		return episodePyStr(value)
	}
}

// episodeTruthy 对齐 Python 真值判断（None/False/0/”/空容器为假）
func episodeTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	case json.Number:
		return v.String() != "" && v.String() != "0"
	case []any:
		return len(v) > 0
	case []string:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	default:
		return true
	}
}

// episodeToInt 对齐 Python int(value)，失败时返回 false
func episodeToInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return int(v), true
	case float32:
		return int(v), true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n), true
		}
		if f, err := v.Float64(); err == nil {
			return int(f), true
		}
		return 0, false
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return int(n), true
		}
		return 0, false
	default:
		return 0, false
	}
}

// episodeToFloat 对齐 Python float(value)，失败时返回 false
func episodeToFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f, true
		}
		return 0, false
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// episodeOptionalFloat 对齐 Python _as_optional_float，额外支持 bool/json.Number
func episodeOptionalFloat(value any) *float64 {
	switch value.(type) {
	case bool, json.Number:
		if f, ok := episodeToFloat(value); ok {
			return &f
		}
		return nil
	default:
		return OptionalFloat(value)
	}
}

// episodeFloatOr 对齐 Python float(value) 的异常兜底分支
func episodeFloatOr(value any, fallback float64) float64 {
	if f, ok := episodeToFloat(value); ok {
		return f
	}
	return fallback
}

// episodeRowInt 读取行内整数列，NULL 视为 0，对齐 Python int(row[k] or 0)
func episodeRowInt(row map[string]any, key string) int {
	value, ok := row[key]
	if !ok || value == nil {
		return 0
	}
	if n, ok := episodeToInt(value); ok {
		return n
	}
	return 0
}

// episodeRowFloat 读取行内浮点列，NULL 视为 0，对齐 Python float(row[k] or 0.0)
func episodeRowFloat(row map[string]any, key string) float64 {
	value, ok := row[key]
	if !ok || value == nil {
		return 0
	}
	if f, ok := episodeToFloat(value); ok {
		return f
	}
	return 0
}

// episodeIterList 对齐 Python value or [] 的迭代语义
func episodeIterList(value any) []any {
	switch v := value.(type) {
	case nil:
		return nil
	case []any:
		return v
	case []string:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = item
		}
		return out
	default:
		return nil
	}
}

// episodePyStrList 对齐 Python [str(item).strip() for item in (values or []) if str(item).strip()]
func episodePyStrList(value any) []string {
	items := episodeIterList(value)
	out := make([]string, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(episodePyStr(item))
		if text == "" {
			continue
		}
		out = append(out, text)
	}
	return out
}

// episodePythonFloatRepr 对齐 Python repr(float)，用于 episode_id 稳定哈希
func episodePythonFloatRepr(value float64) string {
	switch {
	case math.IsNaN(value):
		return "nan"
	case math.IsInf(value, 1):
		return "inf"
	case math.IsInf(value, -1):
		return "-inf"
	case value == 0:
		if math.Signbit(value) {
			return "-0.0"
		}
		return "0.0"
	}
	absolute := math.Abs(value)
	// Python 在十进制指数 >= 16 或 < -4 时使用科学计数法
	if absolute >= 1e-4 && absolute < 1e16 {
		text := strconv.FormatFloat(value, 'f', -1, 64)
		if !strings.Contains(text, ".") {
			text += ".0"
		}
		return text
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// episodePyJSONString 对齐 json.dumps(str, ensure_ascii=False) 的转义
func episodePyJSONString(text string) string {
	var b strings.Builder
	b.Grow(len(text) + 2)
	b.WriteByte('"')
	for _, r := range text {
		switch r {
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		case '\b':
			b.WriteString("\\b")
		case '\f':
			b.WriteString("\\f")
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, "\\u%04x", r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// episodeJSONScalar 对齐 json.dumps 对标量值的输出
func episodeJSONScalar(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return episodePyJSONString(v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return episodePythonFloatRepr(v)
	case float32:
		return episodePythonFloatRepr(float64(v))
	case json.Number:
		return v.String()
	default:
		return "null"
	}
}

// episodeHashSeed 对齐 Python json.dumps({...}, ensure_ascii=False, sort_keys=True)
// 键序固定为 event_time_end / event_time_start / evidence_ids / source / summary / title
func episodeHashSeed(source any, title string, summary string, eventStart any, eventEnd any, evidenceIDs []string) string {
	var b strings.Builder
	b.WriteString("{\"event_time_end\": ")
	b.WriteString(episodeJSONScalar(eventEnd))
	b.WriteString(", \"event_time_start\": ")
	b.WriteString(episodeJSONScalar(eventStart))
	b.WriteString(", \"evidence_ids\": [")
	for i, item := range evidenceIDs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(episodePyJSONString(item))
	}
	b.WriteString("], \"source\": ")
	b.WriteString(episodeJSONScalar(source))
	b.WriteString(", \"summary\": ")
	b.WriteString(episodePyJSONString(summary))
	b.WriteString(", \"title\": ")
	b.WriteString(episodePyJSONString(title))
	b.WriteString("}")
	return b.String()
}
