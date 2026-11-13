package llm

// 生成诊断脱敏
// 对等 MaiBot src/llm_models/generation_diagnostics.py：敏感键、URL userinfo/鉴权 query、
// 大体积 base64 与 data URI 统一转 sha256/size，避免快照与诊断日志落盘原始凭据或二进制。
//
// 已知边界：
//   - 仅识别 Go 快照中实际出现的 map/list/基础类型；任意结构体走 JSON 往返，失败时记录类型名
//   - URL 解析失败、非 http(s) 或无 host 时原样返回，与 Python urlsplit 的保守分支一致

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const (
	// diagnosticSensitivePlaceholder 对齐 sanitize_generation_diagnostic 的 [REDACTED]
	diagnosticSensitivePlaceholder = "[REDACTED]"
	// snapshotSensitivePlaceholder 对齐 request_snapshot._sanitize_provider_request 的 [已脱敏]
	snapshotSensitivePlaceholder = "[已脱敏]"
	diagnosticPrivatePlaceholder = "[仅在内存 replay fragment 中保留]"
	// diagnosticLargeBinaryThresholdBytes 对齐 LARGE_BINARY_THRESHOLD_BYTES
	diagnosticLargeBinaryThresholdBytes = 64 * 1024
	// diagnosticLargeBinaryThresholdChars 对齐 64*1024*4//3；base64 长度低于该值不视为大体积
	diagnosticLargeBinaryThresholdChars = diagnosticLargeBinaryThresholdBytes * 4 / 3
)

var (
	diagnosticSensitiveKeys = map[string]struct{}{
		"api_key":             {},
		"apikey":              {},
		"authorization":       {},
		"auth_token":          {},
		"access_token":        {},
		"refresh_token":       {},
		"bearer_token":        {},
		"cookie":              {},
		"password":            {},
		"proxy_authorization": {},
		"secret":              {},
		"token":               {},
		"x_api_key":           {},
	}
	diagnosticPrivateKeys = map[string]struct{}{
		"encrypted_content": {},
		"thought_signature": {},
	}
	diagnosticBinaryKeys = map[string]struct{}{
		"audio":        {},
		"audio_base64": {},
		"blob":         {},
		"bytes":        {},
		"data_base64":  {},
		"file_data":    {},
		"image_base64": {},
	}
	diagnosticBase64Pattern = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)
)

// normalizeDiagnosticKey 对齐 _normalize_key：strip + lower + 连字符转下划线
func normalizeDiagnosticKey(key string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "-", "_")
}

// isDiagnosticSensitiveKey 对齐 _is_sensitive_key 的精确键与常见前缀变体
func isDiagnosticSensitiveKey(key string) bool {
	if _, ok := diagnosticSensitiveKeys[key]; ok {
		return true
	}
	for _, suffix := range []string{"_api_key", "_access_token", "_auth_token", "_password", "_secret"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

func isDiagnosticPrivateKey(key string) bool {
	_, ok := diagnosticPrivateKeys[key]
	return ok
}

func isDiagnosticBinaryKey(key string) bool {
	_, ok := diagnosticBinaryKeys[key]
	return ok
}

// binarySummary 对齐 _binary_summary：哈希对象是原始字符串编码后的 UTF-8 字节，不是 base64 解码字节
func binarySummary(raw []byte, mediaType string) map[string]any {
	sum := sha256.Sum256(raw)
	summary := map[string]any{
		"type":       "omitted_binary",
		"size_bytes": len(raw),
		"sha256":     hex.EncodeToString(sum[:]),
	}
	if mediaType != "" {
		summary["media_type"] = mediaType
	}
	return summary
}

func binarySummaryString(value, mediaType string) map[string]any {
	return binarySummary([]byte(value), mediaType)
}

// looksLikeLargeBase64 对齐 _looks_like_large_base64：阈值按字符数、长度整除 4、标准 base64 校验
func looksLikeLargeBase64(value string) bool {
	normalized := strings.TrimSpace(value)
	if len(normalized) < diagnosticLargeBinaryThresholdChars {
		return false
	}
	if len(normalized)%4 != 0 || !diagnosticBase64Pattern.MatchString(normalized) {
		return false
	}
	if _, err := base64.StdEncoding.DecodeString(normalized); err != nil {
		return false
	}
	return true
}

// SanitizeDiagnosticURL 导出入口，便于诊断层复用；语义对齐 sanitize_diagnostic_url
func SanitizeDiagnosticURL(value string) string {
	return sanitizeDiagnosticURL(value)
}

// sanitizeDiagnosticURL 去掉 userinfo，并把疑似鉴权 query 参数值替换为 [REDACTED]
func sanitizeDiagnosticURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Host == "" {
		return value
	}
	hostname := strings.ToLower(parsed.Hostname())
	if port := parsed.Port(); port != "" {
		hostname += ":" + port
	}
	query := sanitizeDiagnosticQuery(parsed.RawQuery)
	var builder strings.Builder
	builder.WriteString(scheme)
	builder.WriteString("://")
	builder.WriteString(hostname)
	builder.WriteString(parsed.EscapedPath())
	if query != "" {
		builder.WriteByte('?')
		builder.WriteString(query)
	}
	if parsed.Fragment != "" {
		builder.WriteByte('#')
		builder.WriteString(parsed.EscapedFragment())
	}
	return builder.String()
}

// sanitizeDiagnosticQuery 复刻 parse_qsl + urlencode：保留顺序/重复/空值，空格编码为 +
func sanitizeDiagnosticQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	parts := strings.Split(rawQuery, "&")
	encoded := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		keyPart, valuePart, _ := strings.Cut(part, "=")
		key, err := url.QueryUnescape(keyPart)
		if err != nil {
			key = keyPart
		}
		itemValue, err := url.QueryUnescape(valuePart)
		if err != nil {
			itemValue = valuePart
		}
		if isDiagnosticAuthQueryKey(key) {
			itemValue = "[REDACTED]"
		}
		encoded = append(encoded, url.QueryEscape(key)+"="+url.QueryEscape(itemValue))
	}
	return strings.Join(encoded, "&")
}

func isDiagnosticAuthQueryKey(key string) bool {
	lower := strings.ToLower(key)
	for _, hint := range []string{"auth", "key", "secret", "signature", "token"} {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

// SanitizeGenerationDiagnostic 导出入口；key 可选，语义对齐 sanitize_generation_diagnostic
func SanitizeGenerationDiagnostic(value any, key ...string) any {
	return sanitizeGenerationDiagnostic(value, key...)
}

// sanitizeGenerationDiagnostic 将任意诊断值转为 JSON-safe 且脱敏的表示
func sanitizeGenerationDiagnostic(value any, key ...string) any {
	normalizedKey := ""
	if len(key) > 0 {
		normalizedKey = normalizeDiagnosticKey(key[0])
	}
	if isDiagnosticSensitiveKey(normalizedKey) {
		return diagnosticSensitivePlaceholder
	}
	if isDiagnosticPrivateKey(normalizedKey) {
		return diagnosticPrivatePlaceholder
	}
	switch typed := value.(type) {
	case nil, bool, float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, json.Number:
		return typed
	case []byte:
		return binarySummary(typed, "")
	case string:
		return sanitizeDiagnosticString(typed, normalizedKey)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for itemKey, item := range typed {
			out[itemKey] = sanitizeGenerationDiagnostic(item, itemKey)
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(typed))
		for itemKey, item := range typed {
			out[itemKey] = sanitizeGenerationDiagnostic(item, itemKey)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = sanitizeGenerationDiagnostic(item, normalizedKey)
		}
		return out
	case []string:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = sanitizeGenerationDiagnostic(item, normalizedKey)
		}
		return out
	case []map[string]any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = sanitizeGenerationDiagnostic(item, normalizedKey)
		}
		return out
	default:
		return sanitizeDiagnosticFallback(typed, normalizedKey)
	}
}

// sanitizeDiagnosticString 处理 data URI、大体积 base64 与 URL；普通字符串原样保留
func sanitizeDiagnosticString(value, normalizedKey string) any {
	normalizedValue := strings.TrimSpace(value)
	if strings.HasPrefix(normalizedValue, "data:") && strings.Contains(normalizedValue, ";base64,") {
		header, encodedValue, found := strings.Cut(normalizedValue, ",")
		if found {
			mediaType := strings.TrimPrefix(header, "data:")
			if index := strings.Index(mediaType, ";"); index >= 0 {
				mediaType = mediaType[:index]
			}
			return binarySummaryString(encodedValue, mediaType)
		}
	}
	if isDiagnosticBinaryKey(normalizedKey) || looksLikeLargeBase64(normalizedValue) {
		return binarySummaryString(value, "")
	}
	if strings.HasPrefix(normalizedValue, "http://") || strings.HasPrefix(normalizedValue, "https://") {
		return sanitizeDiagnosticURL(value)
	}
	return value
}

// sanitizeDiagnosticFallback 对齐 Python 的 JSON 往返；无法转换时记录类型名
func sanitizeDiagnosticFallback(value any, normalizedKey string) any {
	encoded, err := json.Marshal(value)
	if err == nil {
		var converted any
		if json.Unmarshal(encoded, &converted) == nil {
			return sanitizeGenerationDiagnostic(converted, normalizedKey)
		}
	}
	return map[string]any{
		"type":  "diagnostic_object",
		"class": fmt.Sprintf("%T", value),
	}
}
