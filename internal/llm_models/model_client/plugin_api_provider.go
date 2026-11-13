package model_client

// 插件 Provider 的 api_provider 快照
// 对齐 MaiBot PluginLLMClient._build_api_provider_snapshot：APIProvider.model_dump(mode="json")
// 会带出全部字段（含 max_retry、reasoning_parse_mode、tool_argument_parse_mode），
// 空 organization/project 序列化为 null，default_headers/default_query 为空对象。

import (
	"strings"
	"time"
)

// apiProviderSnapshot Provider 配置快照；includeKey 仅用于 RPC 顶层（可信 Runner）
func (m *pluginModel) apiProviderSnapshot(includeKey bool) map[string]any {
	name := trimTask(m.cfg.ProviderName)
	if name == "" {
		name = m.cfg.ClientType
	}
	snapshot := map[string]any{
		"auth_header_name":         ProviderAuthHeaderName(m.cfg.AuthHeaderName),
		"auth_header_prefix":       ProviderAuthHeaderPrefixWithSet(m.cfg.AuthHeaderPrefix, m.cfg.AuthHeaderPrefixSet),
		"auth_query_name":          ProviderAuthQueryName(m.cfg.AuthQueryName),
		"auth_type":                ProviderAuthType(m.cfg.AuthType),
		"base_url":                 m.cfg.BaseURL,
		"client_type":              m.cfg.ClientType,
		"default_headers":          ProviderStringMapOrEmpty(m.cfg.DefaultHeaders),
		"default_query":            ProviderStringMapOrEmpty(m.cfg.DefaultQuery),
		"max_retry":                providerMaxRetry(m.cfg),
		"model_list_endpoint":      ProviderModelListEndpoint(m.cfg.ModelListEndpoint),
		"name":                     name,
		"organization":             ProviderOptionalString(m.cfg.Organization),
		"project":                  ProviderOptionalString(m.cfg.Project),
		"reasoning_parse_mode":     ProviderParseMode(m.cfg.ReasoningParseMode),
		"retry_interval":           ProviderDurationSeconds(m.cfg.RetryInterval),
		"timeout":                  ProviderDurationSeconds(m.cfg.Timeout),
		"tool_argument_parse_mode": ProviderParseMode(m.cfg.ToolArgumentParseMode),
	}
	if includeKey {
		snapshot["api_key"] = m.cfg.APIKey
	}
	return snapshot
}

// ProviderAuthType 对齐 APIProvider.auth_type 默认 bearer
func ProviderAuthType(value string) string {
	authType := strings.ToLower(strings.TrimSpace(value))
	if authType == "" {
		return authTypeBearer
	}
	return authType
}

// ProviderAuthHeaderName 对齐 APIProvider.auth_header_name 默认 Authorization
func ProviderAuthHeaderName(value string) string {
	if name := strings.TrimSpace(value); name != "" {
		return name
	}
	return "Authorization"
}

// ProviderAuthHeaderPrefix 对齐 APIProvider.auth_header_prefix 默认 Bearer
func ProviderAuthHeaderPrefix(value string) string {
	if prefix := strings.TrimSpace(value); prefix != "" {
		return prefix
	}
	return "Bearer"
}

// ProviderAuthHeaderPrefixWithSet 区分未配置（默认 Bearer）与显式空串（裸 key）
func ProviderAuthHeaderPrefixWithSet(value string, set bool) string {
	if set {
		return strings.TrimSpace(value)
	}
	return ProviderAuthHeaderPrefix(value)
}

// ProviderAuthQueryName 对齐 APIProvider.auth_query_name 默认 api_key
func ProviderAuthQueryName(value string) string {
	if name := strings.TrimSpace(value); name != "" {
		return name
	}
	return "api_key"
}

// ProviderModelListEndpoint 对齐 APIProvider.model_list_endpoint 默认 /models
func ProviderModelListEndpoint(value string) string {
	if endpoint := strings.TrimSpace(value); endpoint != "" {
		return endpoint
	}
	return "/models"
}

// ProviderParseMode 对齐 reasoning_parse_mode/tool_argument_parse_mode 默认 auto
func ProviderParseMode(value string) string {
	if mode := strings.TrimSpace(value); mode != "" {
		return mode
	}
	return "auto"
}

// ProviderStringMapOrEmpty 复制字典；nil 输出空对象，避免 JSON null 与 Python {} 不等价
func ProviderStringMapOrEmpty(value map[string]string) map[string]string {
	out := make(map[string]string, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

// ProviderOptionalString 空字符串输出 null，对齐 APIProvider.organization/project 的 str|None
func ProviderOptionalString(value string) any {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return nil
}

// ProviderDurationSeconds 输出整数秒，对齐 Python APIProvider.timeout/retry_interval 的 int
func ProviderDurationSeconds(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	return int64(value / time.Second)
}
