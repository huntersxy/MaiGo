package contract

// 客户端与快照层共用的零散工具。
//
// 「客户端实现」与「快照/统计层」两侧都要用这些函数；放在任一侧都会让
// 另一侧反向依赖，因此统一放在契约层。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// DefaultProviderMaxRetry APIProvider.max_retry 未显式配置时的默认值
const DefaultProviderMaxRetry = 3

// CloneAnyMap 浅拷贝 map；空输入返回 nil，与 Python dict(...) if x else None 一致
func CloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// FloatFromAny 把标量转成 float64
func FloatFromAny(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	default:
		return 0, false
	}
}

// IntFromAny 把标量转成 int
func IntFromAny(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case float32:
		return int(x), true
	default:
		return 0, false
	}
}

// NonEmptyStrings 去掉空白项并 trim
func NonEmptyStrings(list []string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		s = TrimTask(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// AgenticMessageTokens 取一次响应的总 token 数
func AgenticMessageTokens(message *schema.AgenticMessage) int {
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.TokenUsage == nil {
		return 0
	}
	return message.ResponseMeta.TokenUsage.TotalTokens
}

// ProviderMaxRetry 返回 APIProvider.max_retry 的有效值
func ProviderMaxRetry(cfg ModelConfig) int {
	if cfg.MaxRetrySet {
		return cfg.MaxRetry
	}
	if cfg.MaxRetry > 0 {
		return cfg.MaxRetry
	}
	return DefaultProviderMaxRetry
}

// ProviderRetryAttempts 把 max_retry 转换成含首次请求在内的尝试次数
func ProviderRetryAttempts(cfg ModelConfig) int {
	attempts := ProviderMaxRetry(cfg)
	if attempts < 1 {
		return 1
	}
	return attempts
}

// ReplayString 读取快照里的字符串字段
func ReplayString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

// ReplayStringMap 读取快照里的字符串字典字段
func ReplayStringMap(payload map[string]any, key string) map[string]string {
	out := map[string]string{}
	raw, _ := payload[key].(map[string]any)
	for name, value := range raw {
		if text, ok := value.(string); ok {
			out[name] = text
		}
	}
	return out
}

// FirstReplayString 返回第一个非空字符串
func FirstReplayString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// ReplayBody 深拷贝一份请求体
func ReplayBody(raw any) map[string]any {
	payload, ok := raw.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{}
	}
	var clone map[string]any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return map[string]any{}
	}
	if clone == nil {
		return map[string]any{}
	}
	return clone
}

// ReplayToolDefinitions 读取快照里的工具定义
func ReplayToolDefinitions(snapshot map[string]any) []any {
	definitions, _ := snapshot["tool_definitions"].([]any)
	return definitions
}

// MetadataString 读取 metadata 下的字符串字段
func MetadataString(snapshot map[string]any, key string) string {
	metadata, _ := snapshot["metadata"].(map[string]any)
	return ReplayString(metadata, key)
}

// ApplyReplayHeaders 把快照中记录的 Provider 鉴权与默认参数还原到请求上
func ApplyReplayHeaders(req *http.Request, apiProvider map[string]any, apiKey string) {
	for key, value := range ReplayStringMap(apiProvider, "default_headers") {
		req.Header.Set(key, value)
	}
	query := req.URL.Query()
	for key, value := range ReplayStringMap(apiProvider, "default_query") {
		query.Set(key, value)
	}
	authType := strings.TrimSpace(ReplayString(apiProvider, "auth_type"))
	if authType == "" {
		authType = AuthTypeBearer
	}
	headerName := ReplayString(apiProvider, "auth_header_name")
	if headerName == "" {
		headerName = AuthorizationHeader
	}
	prefix := ReplayString(apiProvider, "auth_header_prefix")
	// MaiBot APIProvider 默认 auth_header_prefix=Bearer；显式空串表示裸 key
	if _, present := apiProvider["auth_header_prefix"]; !present && (authType == AuthTypeBearer || authType == AuthTypeHeader) {
		prefix = "Bearer"
	}
	switch authType {
	case AuthTypeBearer, AuthTypeHeader:
		if apiKey != "" {
			req.Header.Set(headerName, BuildAuthHeaderValue(prefix, apiKey))
		}
	case AuthTypeQuery:
		name := ReplayString(apiProvider, "auth_query_name")
		if name == "" {
			name = "api_key"
		}
		if apiKey != "" {
			query.Set(name, apiKey)
		}
	case AuthTypeNone:
	default:
		if apiKey != "" {
			req.Header.Set(AuthorizationHeader, "Bearer "+apiKey)
		}
	}
	if len(query) > 0 {
		req.URL.RawQuery = query.Encode()
	}
}

// StringFromAny 对齐 Python str(value)
func StringFromAny(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

// SnapshotImageExtension MIME 类型对应的图片扩展名，用于快照资产命名
func SnapshotImageExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	case "image/bmp":
		return "bmp"
	case "image/tiff":
		return "tiff"
	default:
		return "png"
	}
}
