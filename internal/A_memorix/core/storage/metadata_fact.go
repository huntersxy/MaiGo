package storage

// A_memorix 事实账本基础语义，对齐 Python core/storage/metadata_fact.py
// 状态机安全边界：精确重复只增强原 claim；显式 supersedes 才让旧 claim 失效；
// 单值槽位出现未声明的异值时新 claim 进入 conflicted，不按时间静默覆盖已确认事实

import (
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// 事实枚举集合，对齐 Python metadata_fact.py 的模块级常量
var (
	factScopeTypes      = factStringSet("person", "chat")
	factPolarities      = factStringSet("positive", "negative")
	factCardinalities   = factStringSet("single", "set")
	factStabilities     = factStringSet("stable", "temporal", "uncertain")
	factAuthorities     = factStringSet("manual", "direct_user", "imported", "summary_derived")
	factStatuses        = factStringSet("active", "conflicted", "superseded", "retracted")
	factEvidenceStances = factStringSet("support", "refute")
	factProfileSections = factStringSet(
		"identity_settings",
		"relationship_settings",
		"stable_facts",
		"interaction_preferences",
		"recent_interactions",
		"uncertain_notes",
	)
)

// factStringSet 把枚举值构造为集合
func factStringSet(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

// factTruthy 对齐 Python 真值判断，用于 `value or ...` 语义
func factTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case []byte:
		return len(v) != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	case []any:
		return len(v) != 0
	case []string:
		return len(v) != 0
	case []map[string]any:
		return len(v) != 0
	case map[string]any:
		return len(v) != 0
	default:
		return true
	}
}

// factPyStr 对齐 Python str(value)
func factPyStr(value any) string {
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
		return factPyFloatRepr(v)
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(value)
	}
}

// factPyFloatRepr 贴近 Python repr(float)：整数结果补 .0
func factPyFloatRepr(value float64) string {
	switch {
	case math.IsInf(value, 1):
		return "inf"
	case math.IsInf(value, -1):
		return "-inf"
	case math.IsNaN(value):
		return "nan"
	}
	text := strconv.FormatFloat(value, 'g', -1, 64)
	if !strings.ContainsAny(text, ".eE") {
		return text + ".0"
	}
	return text
}

// factPyStrOrEmpty 对齐 Python str(value or "")
func factPyStrOrEmpty(value any) string {
	if !factTruthy(value) {
		return ""
	}
	return factPyStr(value)
}

// factPyRepr 对齐 Python repr(str)
func factPyRepr(value string) string {
	if !strings.Contains(value, "'") {
		return "'" + value + "'"
	}
	if !strings.Contains(value, "\"") {
		return "\"" + value + "\""
	}
	return "'" + strings.ReplaceAll(value, "'", "\\'") + "'"
}

// factPyReprList 对齐 Python list 的 repr，用于错误文案中的 ID 列表
func factPyReprList(values []string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, factPyRepr(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// factPyControlSpace Python 把 0x1c 到 0x1f 的控制符也当空白，此处补齐该差异
func factPyControlSpace(r rune) bool {
	return r >= 0x1c && r <= 0x1f
}

// factStrip 对齐 Python str.strip()
func factStrip(text string) string {
	return strings.TrimFunc(text, func(r rune) bool {
		return factPyControlSpace(r) || unicode.IsSpace(r)
	})
}

// factSplitWhitespace 对齐 Python str.split()：0x1c 到 0x1f 同样视为分隔符
func factSplitWhitespace(text string) []string {
	return strings.Fields(strings.Map(func(r rune) rune {
		if factPyControlSpace(r) {
			return ' '
		}
		return r
	}, text))
}

// factNormalizedToken 对齐 Python _normalized_token（NFKC + 折叠空白 + casefold）
func factNormalizedToken(value any) string {
	normalized := norm.NFKC.String(factPyStrOrEmpty(value))
	return cases.Fold().String(strings.Join(factSplitWhitespace(normalized), " "))
}

// factRequiredToken 对齐 Python _required_token
func factRequiredToken(name, value string) (string, error) {
	token := factStrip(value)
	if token == "" {
		return "", fmt.Errorf("%s 不能为空", name)
	}
	return token, nil
}

// factEnumToken 对齐 Python _enum_token
func factEnumToken(name, value string, allowed map[string]struct{}) (string, error) {
	token := factNormalizedToken(value)
	if _, ok := allowed[token]; !ok {
		return "", fmt.Errorf("无效 %s: %s", name, value)
	}
	return token, nil
}

// factEvidenceIdentity 对齐 Python _evidence_identity：类型与 ID 必须同时存在或同时为空
func factEvidenceIdentity(evidenceType, evidenceID string) (string, string, error) {
	normalizedType := factStrip(evidenceType)
	normalizedID := factStrip(evidenceID)
	if (normalizedType != "") != (normalizedID != "") {
		return "", "", fmt.Errorf("evidence_type 与 evidence_id 必须同时提供")
	}
	return normalizedType, normalizedID, nil
}

// factNormalizeSQLValue 把驱动原始值归一为 Python sqlite3 风格
func factNormalizeSQLValue(value any) any {
	if raw, ok := value.([]byte); ok {
		return string(raw)
	}
	return value
}

// factScanMaps 把查询结果读成 Python 同构字典，读完立即关闭游标
// 单连接句柄下必须读完再发下一条语句，避免连接被游标占用
func factScanMaps(rows *sql.Rows) ([]map[string]any, error) {
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, 8)
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		record := make(map[string]any, len(columns))
		for i, name := range columns {
			record[name] = factNormalizeSQLValue(values[i])
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// factQueryMaps 执行查询并返回字典切片
func factQueryMaps(c *Conn, query string, args ...any) ([]map[string]any, error) {
	rows, err := c.Query(query, args...)
	if err != nil {
		return nil, err
	}
	return factScanMaps(rows)
}

// factFirstRow 取首行，无结果返回 nil
func factFirstRow(rows []map[string]any) map[string]any {
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// factPlaceholders 生成 `?,?,?` 形式的占位符
func factPlaceholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

// factStringsToAny 把字符串切片转换为查询参数
func factStringsToAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// factStringIn 判断字符串是否在切片中
func factStringIn(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// factStringList 转为 JSON 友好的字符串列表，nil 归一为空列表
func factStringList(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// factMapSlice 过滤对象列表，对齐 Python 的 isinstance(item, dict) 过滤
func factMapSlice(value any) []map[string]any {
	switch items := value.(type) {
	case []map[string]any:
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if item != nil {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if record, ok := item.(map[string]any); ok && record != nil {
				out = append(out, record)
			}
		}
		return out
	default:
		return []map[string]any{}
	}
}

// factToList 把列表值统一为 []any，非列表值视为单元素
func factToList(value any) []any {
	switch items := value.(type) {
	case []any:
		return items
	case []string:
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, item)
		}
		return out
	case nil:
		return []any{}
	default:
		return []any{value}
	}
}

// factFloatOf 对齐 Python float(value or 0.0)
func factFloatOf(value any) float64 {
	parsed := OptionalFloat(value)
	if parsed == nil {
		return 0
	}
	return *parsed
}

// factFloatOrDefault 解析可选浮点参数，nil 时取默认值
func factFloatOrDefault(value *float64, def float64) float64 {
	if value == nil {
		return def
	}
	return *value
}

// factFloatByDefault 解析行内浮点，0 或不可解析时取默认值（对齐 Python 的 ★or★ 语义）
func factFloatByDefault(value any, def float64) float64 {
	parsed := OptionalFloat(value)
	if parsed == nil || *parsed == 0 {
		return def
	}
	return *parsed
}

// factOptionalFloatEqual 比较行内浮点与可选参数，对齐 Python 的 != None 语义
func factOptionalFloatEqual(value any, target *float64) bool {
	if target == nil {
		return value == nil
	}
	if value == nil {
		return false
	}
	parsed := OptionalFloat(value)
	if parsed == nil {
		return false
	}
	return *parsed == *target
}

// factStringOrDefault 空字符串回退默认值（Go 以零值表达 Python 的缺省关键字参数）
func factStringOrDefault(value, def string) string {
	if value == "" {
		return def
	}
	return value
}

// factLimitArg 对齐 Python max(1, int(limit))，limit 为 0 时回退默认值
func factLimitArg(limit, def int) int {
	if limit == 0 {
		limit = def
	}
	if limit < 1 {
		return 1
	}
	return limit
}

// factClamp01 对齐 Python min(1.0, max(0.0, value))
func factClamp01(value float64) float64 {
	switch {
	case math.IsNaN(value):
		return 0
	case value < 0:
		return 0
	case value > 1:
		return 1
	}
	return value
}

// factClaimRow 对齐 Python _fact_claim_row：浮点列统一转 float64
func factClaimRow(row map[string]any) map[string]any {
	if row == nil {
		return nil
	}
	payload := make(map[string]any, len(row))
	for key, value := range row {
		payload[key] = value
	}
	for _, key := range []string{
		"confidence",
		"valid_from",
		"valid_to",
		"first_observed_at",
		"last_confirmed_at",
		"created_at",
		"updated_at",
	} {
		payload[key] = factOptionalFloatAny(payload[key])
	}
	return payload
}

// factOptionalFloatAny 对齐 Python float(value) if value is not None else None
func factOptionalFloatAny(value any) any {
	if value == nil {
		return nil
	}
	parsed := OptionalFloat(value)
	if parsed == nil {
		return nil
	}
	return *parsed
}

// factClaimRows 批量转换 claim 行
func factClaimRows(rows []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if claim := factClaimRow(row); claim != nil {
			out = append(out, claim)
		}
	}
	return out
}

// factEvidenceRow 对齐 Python _fact_evidence_row：metadata_json 解码为 metadata
func factEvidenceRow(row map[string]any) map[string]any {
	if row == nil {
		return nil
	}
	payload := make(map[string]any, len(row))
	for key, value := range row {
		if key == "metadata_json" {
			continue
		}
		payload[key] = value
	}
	metadata := map[string]any{}
	if raw, ok := row["metadata_json"]; ok && factTruthy(raw) {
		if parsed, isMap := jsonLoads(raw, map[string]any{}).(map[string]any); isMap {
			metadata = parsed
		}
	}
	payload["metadata"] = metadata
	payload["weight"] = factFloatByDefault(row["weight"], 1.0)
	observed := 0.0
	if parsed := OptionalFloat(row["observed_at"]); parsed != nil {
		observed = *parsed
	}
	payload["observed_at"] = observed
	return payload
}

// factEvidenceRows 批量转换证据行
func factEvidenceRows(rows []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if evidence := factEvidenceRow(row); evidence != nil {
			out = append(out, evidence)
		}
	}
	return out
}

// factClaimOutcome 对齐 Python 结果字典里的状态标记
type factClaimOutcome struct {
	created     bool
	reinforced  bool
	restored    bool
	idempotent  bool
	superseded  []string
	conflicting []string
}

// factClaimResult 拼装 claim 行与状态标记
func factClaimResult(claim map[string]any, outcome factClaimOutcome) map[string]any {
	result := make(map[string]any, len(claim)+6)
	for key, value := range claim {
		result[key] = value
	}
	result["created"] = outcome.created
	result["reinforced"] = outcome.reinforced
	result["restored"] = outcome.restored
	result["idempotent"] = outcome.idempotent
	result["superseded_claim_ids"] = factStringList(outcome.superseded)
	result["conflicting_claim_ids"] = factStringList(outcome.conflicting)
	return result
}

// factEvidenceResult 拼装证据行与写入标记
func factEvidenceResult(evidence map[string]any, created, idempotent bool) map[string]any {
	result := make(map[string]any, len(evidence)+2)
	for key, value := range evidence {
		result[key] = value
	}
	result["created"] = created
	result["idempotent"] = idempotent
	return result
}

// factClaimStateResult 拼装 restore 语义的结果字典
func factClaimStateResult(claim map[string]any, restored, idempotent bool, conflicts []string) map[string]any {
	result := make(map[string]any, len(claim)+3)
	for key, value := range claim {
		result[key] = value
	}
	result["restored"] = restored
	result["idempotent"] = idempotent
	result["conflicting_claim_ids"] = factStringList(conflicts)
	return result
}
