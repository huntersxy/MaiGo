package contract

import "strings"

// normalizeClientType 对齐 MaiBot api_providers.client_type（小写、去空白）
func NormalizeClientType(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func IsGeminiClientType(raw string) bool {
	return NormalizeClientType(raw) == "gemini"
}

// isChatCompletionsClientType 判断是否走 OpenAI 兼容 Chat Completions
// 对齐 MaiBot APIProvider.client_type 默认值 "openai"（src/config/model_configs.py）
func IsChatCompletionsClientType(raw string) bool {
	switch NormalizeClientType(raw) {
	case "", "openai":
		return true
	default:
		return false
	}
}

// isOpenAIResponsesClientType 判断是否走 OpenAI Responses API
func IsOpenAIResponsesClientType(raw string) bool {
	return NormalizeClientType(raw) == "openai_responses"
}

// isOpenAICompatClientType 判断是否属于 OpenAI 兼容协议族（Chat Completions / Responses）
func IsOpenAICompatClientType(raw string) bool {
	switch NormalizeClientType(raw) {
	case "", "openai", "openai_responses":
		return true
	default:
		return false
	}
}
