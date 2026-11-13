package utils

// A_memorix 时间解析工具，对齐 Python core/utils/time_parser.py
// 查询参数只接受 YYYY/MM/DD 与 YYYY/MM/DD HH:mm；入库允许时间戳与更宽松的格式
// Python 侧所有朴素时间都按本机时区解释，这里默认同样使用 time.Local

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	// 对齐 r"^\d{4}/\d{2}/\d{2}$"（\d 先按 Unicode 数字归一化）
	queryDateRe = regexp.MustCompile("^[0-9]{4}/[0-9]{2}/[0-9]{2}$")
	// 对齐 r"^\d{4}/\d{2}/\d{2} \d{2}:\d{2}$"
	queryMinuteRe = regexp.MustCompile("^[0-9]{4}/[0-9]{2}/[0-9]{2} [0-9]{2}:[0-9]{2}$")
	// 对齐 r"^-?\d+(?:\.\d+)?$"
	numericRe = regexp.MustCompile("^-?[0-9]+(?:\\.[0-9]+)?$")
)

// ingestLayout 描述入库可接受的字符串格式
type ingestLayout struct {
	format   string
	dateOnly bool
}

// 对齐 _INGEST_FORMATS 与 _INGEST_DATE_FORMATS，顺序即优先级
var ingestLayouts = []ingestLayout{
	{format: "%Y/%m/%d %H:%M:%S"},
	{format: "%Y/%m/%d %H:%M"},
	{format: "%Y-%m-%d %H:%M:%S"},
	{format: "%Y-%m-%d %H:%M"},
	{format: "%Y-%m-%dT%H:%M:%S"},
	{format: "%Y-%m-%dT%H:%M"},
	{format: "%Y/%m/%d", dateOnly: true},
	{format: "%Y-%m-%d", dateOnly: true},
}

// strptimePattern 复刻 CPython _strptime 对单个格式编译出的正则
type strptimePattern struct {
	regex      *regexp.Regexp
	directives []byte
}

var strptimePatterns = buildStrptimePatterns()

func buildStrptimePatterns() map[string]*strptimePattern {
	formats := make([]string, 0, len(ingestLayouts)+2)
	for _, layout := range ingestLayouts {
		formats = append(formats, layout.format)
	}
	formats = append(formats, "%Y/%m/%d", "%Y/%m/%d %H:%M")
	patterns := make(map[string]*strptimePattern, len(formats))
	for _, format := range formats {
		patterns[format] = newStrptimePattern(format)
	}
	return patterns
}

func newStrptimePattern(format string) *strptimePattern {
	var b strings.Builder
	b.WriteString("^")
	directives := make([]byte, 0, 8)
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c != '%' || i+1 >= len(format) {
			b.WriteString(regexp.QuoteMeta(string(c)))
			continue
		}
		i++
		directives = append(directives, format[i])
switch format[i] {
			case 'Y':
				// CPython strptime 的 %Y 接受 Unicode Nd；%m/%d/%H/%M/%S 仍只要 ASCII
				b.WriteString("((?:[0-9]|\\p{Nd}){4})")
			case 'm':
				b.WriteString("(1[0-2]|0[1-9]|[1-9])")
			case 'd':
				b.WriteString("(3[01]|[12][0-9]|0[1-9]|[1-9])")
			case 'H':
				b.WriteString("(2[0-3]|[0-1][0-9]|[0-9])")
			case 'M':
				b.WriteString("([0-5][0-9]|[0-9])")
			case 'S':
				b.WriteString("(6[01]|[0-5][0-9]|[0-9])")
			default:
				b.WriteString(regexp.QuoteMeta(string(format[i])))
			}
	}
	return &strptimePattern{regex: regexp.MustCompile(b.String()), directives: directives}
}

func strptimePatternFor(format string) *strptimePattern {
	if pattern, ok := strptimePatterns[format]; ok {
		return pattern
	}
	return newStrptimePattern(format)
}

// pyStrptime 对齐 datetime.strptime 的匹配与校验语义
// 报错文案沿用 CPython 的写法，便于对照排查
func pyStrptime(text string, format string, loc *time.Location) (time.Time, error) {
	pattern := strptimePatternFor(format)
	groups := pattern.regex.FindStringSubmatch(text)
	if groups == nil {
		return time.Time{}, fmt.Errorf("time data '%s' does not match format '%s'", text, format)
	}
	if rest := text[len(groups[0]):]; rest != "" {
		return time.Time{}, fmt.Errorf("unconverted data remains: %s", rest)
	}

	year, month, day := 1, 1, 1
	hour, minute, second := 0, 0, 0
	for i, directive := range pattern.directives {
		group := groups[i+1]
		if directive == 'Y' {
			group = pyDigitsToASCII(group)
		}
		value, err := strconv.Atoi(group)
		if err != nil {
			return time.Time{}, fmt.Errorf("time data '%s' does not match format '%s'", text, format)
		}
		switch directive {
		case 'Y':
			year = value
		case 'm':
			month = value
		case 'd':
			day = value
		case 'H':
			hour = value
		case 'M':
			minute = value
		case 'S':
			second = value
		}
	}

	if year < 1 || year > 9999 {
		return time.Time{}, fmt.Errorf("year must be in 1..9999, not %d", year)
	}
	if second > 59 {
		return time.Time{}, fmt.Errorf("second must be in 0..59")
	}
	lastDay := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, loc).Day()
	if day < 1 || day > lastDay {
		return time.Time{}, fmt.Errorf("day %d must be in range 1..%d for month %d in year %d", day, lastDay, month, year)
	}
	return time.Date(year, time.Month(month), day, hour, minute, second, 0, loc), nil
}

// ParseQueryDatetimeToTimestamp 解析查询时间，对齐 parse_query_datetime_to_timestamp
func ParseQueryDatetimeToTimestamp(value string, isEnd bool) (float64, error) {
	return parseQueryDatetimeToTimestampIn(value, isEnd, time.Local)
}

func parseQueryDatetimeToTimestampIn(value string, isEnd bool, loc *time.Location) (float64, error) {
	text := pyStrip(value)
	if text == "" {
		return 0, fmt.Errorf("时间不能为空")
	}
	// Python \d 接受 Unicode 数字，但 strptime 只对 %Y 放宽；匹配用归一化，解析用原文
	normalized := pyDigitsToASCII(text)
	if queryDateRe.MatchString(normalized) {
		parsed, err := pyStrptime(text, "%Y/%m/%d", loc)
		if err != nil {
			return 0, err
		}
		if isEnd {
			parsed = time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 23, 59, 0, 0, loc)
		}
		return float64(parsed.Unix()), nil
	}
	if queryMinuteRe.MatchString(normalized) {
		parsed, err := pyStrptime(text, "%Y/%m/%d %H:%M", loc)
		if err != nil {
			return 0, err
		}
		return float64(parsed.Unix()), nil
	}
	return 0, fmt.Errorf("时间格式错误: %s。仅支持 YYYY/MM/DD 或 YYYY/MM/DD HH:mm", text)
}

// ParseQueryTimeRange 解析查询窗口并校验区间，对齐 parse_query_time_range
func ParseQueryTimeRange(timeFrom *string, timeTo *string) (*float64, *float64, error) {
	return parseQueryTimeRangeIn(timeFrom, timeTo, time.Local)
}

func parseQueryTimeRangeIn(timeFrom *string, timeTo *string, loc *time.Location) (*float64, *float64, error) {
	var tsFrom, tsTo *float64
	if timeFrom != nil && *timeFrom != "" {
		value, err := parseQueryDatetimeToTimestampIn(*timeFrom, false, loc)
		if err != nil {
			return nil, nil, err
		}
		tsFrom = &value
	}
	if timeTo != nil && *timeTo != "" {
		value, err := parseQueryDatetimeToTimestampIn(*timeTo, true, loc)
		if err != nil {
			return nil, nil, err
		}
		tsTo = &value
	}
	if tsFrom != nil && tsTo != nil && *tsFrom > *tsTo {
		return nil, nil, fmt.Errorf("time_from 不能晚于 time_to")
	}
	return tsFrom, tsTo, nil
}

// ParseIngestDatetimeToTimestamp 解析入库时间，对齐 parse_ingest_datetime_to_timestamp
// value 可为 nil、数字、布尔或字符串：数字与布尔直接转 float，字符串先尝试数字再逐个尝试格式
func ParseIngestDatetimeToTimestamp(value any, isEnd bool) (*float64, error) {
	return parseIngestDatetimeToTimestampIn(value, isEnd, time.Local)
}

func parseIngestDatetimeToTimestampIn(value any, isEnd bool, loc *time.Location) (*float64, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case bool:
		number := 0.0
		if v {
			number = 1.0
		}
		return &number, nil
	case int:
		return floatPtr(float64(v)), nil
	case int8:
		return floatPtr(float64(v)), nil
	case int16:
		return floatPtr(float64(v)), nil
	case int32:
		return floatPtr(float64(v)), nil
	case int64:
		return floatPtr(float64(v)), nil
	case uint:
		return floatPtr(float64(v)), nil
	case uint8:
		return floatPtr(float64(v)), nil
	case uint16:
		return floatPtr(float64(v)), nil
	case uint32:
		return floatPtr(float64(v)), nil
	case uint64:
		return floatPtr(float64(v)), nil
	case float32:
		return floatPtr(float64(v)), nil
	case float64:
		return floatPtr(v), nil
	case string:
		return parseIngestStringIn(v, isEnd, loc)
	default:
		// 对齐 str(value) 后再按字符串解析
		return parseIngestStringIn(pyStr(value), isEnd, loc)
	}
}

func parseIngestStringIn(value string, isEnd bool, loc *time.Location) (*float64, error) {
	text := pyStrip(value)
	if text == "" {
		return nil, nil
	}
	normalized := pyDigitsToASCII(text)
	if numericRe.MatchString(normalized) {
		number, err := strconv.ParseFloat(normalized, 64)
		if err != nil {
			return nil, fmt.Errorf("无法解析时间: %s", text)
		}
		return &number, nil
	}
	for _, layout := range ingestLayouts {
		parsed, err := pyStrptime(text, layout.format, loc)
		if err != nil {
			continue
		}
		if layout.dateOnly && isEnd {
			parsed = time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 23, 59, 0, 0, loc)
		}
		return floatPtr(float64(parsed.Unix())), nil
	}
	return nil, fmt.Errorf("无法解析时间: %s", text)
}

// NormalizeTimeMeta 归一化 time_meta 到存储层字段，对齐 normalize_time_meta
// 返回的键：event_time、event_time_start、event_time_end（float64 时间戳）、
// time_granularity（"minute" 或 "day"，来自 time_granularity 时为其 str 结果）、
// time_confidence（float64，仅当输入非 None）
func NormalizeTimeMeta(timeMeta map[string]any) (map[string]any, error) {
	return normalizeTimeMetaIn(timeMeta, time.Local)
}

func normalizeTimeMetaIn(timeMeta map[string]any, loc *time.Location) (map[string]any, error) {
	if len(timeMeta) == 0 {
		return map[string]any{}, nil
	}

	eventTime, err := parseIngestDatetimeToTimestampIn(timeMeta["event_time"], false, loc)
	if err != nil {
		return nil, err
	}
	eventStart, err := parseIngestDatetimeToTimestampIn(timeMeta["event_time_start"], false, loc)
	if err != nil {
		return nil, err
	}
	eventEnd, err := parseIngestDatetimeToTimestampIn(timeMeta["event_time_end"], true, loc)
	if err != nil {
		return nil, err
	}

	if timeRange, ok := timeRangeValues(timeMeta["time_range"]); ok {
		if eventStart == nil {
			eventStart, err = parseIngestDatetimeToTimestampIn(timeRange[0], false, loc)
			if err != nil {
				return nil, err
			}
		}
		if eventEnd == nil {
			eventEnd, err = parseIngestDatetimeToTimestampIn(timeRange[1], true, loc)
			if err != nil {
				return nil, err
			}
		}
	}

	if eventStart != nil && eventEnd != nil && *eventStart > *eventEnd {
		return nil, fmt.Errorf("event_time_start 不能晚于 event_time_end")
	}

	normalized := make(map[string]any, 5)
	if eventTime != nil {
		normalized["event_time"] = *eventTime
	}
	if eventStart != nil {
		normalized["event_time_start"] = *eventStart
	}
	if eventEnd != nil {
		normalized["event_time_end"] = *eventEnd
	}

	if granularity := timeMeta["time_granularity"]; isTruthy(granularity) {
		normalized["time_granularity"] = pyStr(granularity)
	} else {
		rawValues := []any{timeMeta["event_time"], timeMeta["event_time_start"], timeMeta["event_time_end"]}
		hasMinute := false
		for _, raw := range rawValues {
			if raw == nil {
				continue
			}
			if text, ok := raw.(string); ok && strings.Contains(text, ":") {
				hasMinute = true
				break
			}
		}
		if hasMinute {
			normalized["time_granularity"] = "minute"
		} else {
			normalized["time_granularity"] = "day"
		}
	}

	if confidence, ok := timeMeta["time_confidence"]; ok && confidence != nil {
		number, err := pyFloat64(confidence)
		if err != nil {
			return nil, err
		}
		normalized["time_confidence"] = number
	}

	return normalized, nil
}

// FormatTimestamp 把时间戳格式化为 YYYY/MM/DD HH:mm，对齐 format_timestamp
// 时区按 time.Local 解释；Python 在 Windows 上对 0 以下或超出 CRT 范围的时间戳抛 OSError，
// 这里只拒绝 datetime 表示范围（1..9999 年）之外的取值
func FormatTimestamp(ts *float64) (*string, error) {
	return formatTimestampIn(ts, time.Local)
}

func formatTimestampIn(ts *float64, loc *time.Location) (*string, error) {
	if ts == nil {
		return nil, nil
	}
	seconds := math.Floor(*ts)
	// 对齐 datetime.fromtimestamp：先舍入到微秒（银行家舍入）再格式化
	micros := math.RoundToEven((*ts - seconds) * 1e6)
	moment := time.Unix(int64(seconds), int64(micros)*1000).In(loc)
	if year := moment.Year(); year < 1 || year > 9999 {
		return nil, fmt.Errorf("year %d is out of range", year)
	}
	text := moment.Format("2006/01/02 15:04")
	return &text, nil
}

// timeRangeValues 取 time_range 的两个元素，对齐 isinstance(time_range, (list, tuple)) and len == 2
func timeRangeValues(value any) ([2]any, bool) {
	var result [2]any
	switch v := value.(type) {
	case []any:
		if len(v) != 2 {
			return result, false
		}
		return [2]any{v[0], v[1]}, true
	case []string:
		if len(v) != 2 {
			return result, false
		}
		return [2]any{v[0], v[1]}, true
	case [2]any:
		return v, true
	case [2]string:
		return [2]any{v[0], v[1]}, true
	default:
		return result, false
	}
}

func floatPtr(value float64) *float64 {
	return &value
}

// isTruthy 对齐 Python 的真值判定（None、False、0、空串、空容器为假）
func isTruthy(value any) bool {
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
	case float32:
		return v != 0
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	default:
		return true
	}
}

// pyStr 近似 Python str()：布尔为 True/False，浮点补 .0，其余走默认格式化
func pyStr(value any) string {
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
	case float64:
		return pyFloatRepr(v)
	case float32:
		return pyFloatRepr(float64(v))
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return fmt.Sprint(value)
	}
}

// pyStrOrEmpty 对齐 Python str(value or "")：假值（含 nil）→""，真值再走 pyStr
func pyStrOrEmpty(value any) string {
	if !isTruthy(value) {
		return ""
	}
	return pyStr(value)
}

// pyFloatRepr 对齐 Python repr(float)/str(float)：-4≤exp<16 用定点，否则科学计数；整数补 ".0"
func pyFloatRepr(value float64) string {
	switch {
	case math.IsNaN(value):
		return "nan"
	case math.IsInf(value, 1):
		return "inf"
	case math.IsInf(value, -1):
		return "-inf"
	}
	scientific := strconv.FormatFloat(value, 'e', -1, 64)
	exponent := 0
	if index := strings.IndexByte(scientific, 'e'); index >= 0 {
		exponent, _ = strconv.Atoi(scientific[index+1:])
	}
	if exponent >= -4 && exponent < 16 {
		text := strconv.FormatFloat(value, 'f', -1, 64)
		if !strings.ContainsAny(text, ".eE") {
			text += ".0"
		}
		return text
	}
	return scientific
}

// pyFloat64 对齐 float(value)，字符串按 Python 的报错文案返回错误
func pyFloat64(value any) (float64, error) {
	switch v := value.(type) {
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	case int:
		return float64(v), nil
	case int8:
		return float64(v), nil
	case int16:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case uint:
		return float64(v), nil
	case uint8:
		return float64(v), nil
	case uint16:
		return float64(v), nil
	case uint32:
		return float64(v), nil
	case uint64:
		return float64(v), nil
	case float32:
		return float64(v), nil
case float64:
			return v, nil
		case json.Number:
			number, err := v.Float64()
			if err != nil {
				return 0, fmt.Errorf("could not convert %s to float", string(v))
			}
			return number, nil
		case string:
			number, err := strconv.ParseFloat(pyStrip(v), 64)
			if err != nil {
				return 0, fmt.Errorf("could not convert string to float: '%s'", v)
			}
			return number, nil
		default:
			return 0, fmt.Errorf("could not convert %s to float", pyStr(value))
		}
	}

// pyDigitsToASCII 把 Unicode 十进制数字映射为 ASCII 数字
// Python 的 \d 与 int()/float() 都接受任意 Nd 数字，所以先归一化再匹配
func pyDigitsToASCII(text string) string {
	needsNormalize := false
	for _, r := range text {
		if r > 0x7f && unicode.IsDigit(r) {
			needsNormalize = true
			break
		}
	}
	if !needsNormalize {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if r > 0x7f {
			if digit, ok := unicodeDigitValue(r); ok {
				b.WriteByte(byte('0' + digit))
				continue
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// unicodeDigitValue 取 Nd 数字的数值，Nd 的范围都以 0 起算每 10 个一循环
func unicodeDigitValue(r rune) (int, bool) {
	if r >= '0' && r <= '9' {
		return int(r - '0'), true
	}
	for _, table := range []*unicode.RangeTable{unicode.Nd} {
		for _, entry := range table.R16 {
			if r >= rune(entry.Lo) && r <= rune(entry.Hi) {
				return int(r-rune(entry.Lo)) % 10, true
			}
		}
		for _, entry := range table.R32 {
			if r >= rune(entry.Lo) && r <= rune(entry.Hi) {
				return int(r-rune(entry.Lo)) % 10, true
			}
		}
	}
	return 0, false
}
