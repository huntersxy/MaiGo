package llm

import "strings"

// normalizeClientType 对齐 MaiBot api_providers.client_type（小写、去空白）
func normalizeClientType(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func isGeminiClientType(raw string) bool {
	return normalizeClientType(raw) == "gemini"
}

// isChatCompletionsClientType 判断是否走 OpenAI 兼容 Chat Completions
// 对齐 MaiBot APIProvider.client_type 默认值 "openai"（src/config/model_configs.py）
func isChatCompletionsClientType(raw string) bool {
	switch normalizeClientType(raw) {
	case "", "openai":
		return true
	default:
		return false
	}
}

// isOpenAIResponsesClientType 判断是否走 OpenAI Responses API
func isOpenAIResponsesClientType(raw string) bool {
	return normalizeClientType(raw) == "openai_responses"
}

// isOpenAICompatClientType 判断是否属于 OpenAI 兼容协议族（Chat Completions / Responses）
func isOpenAICompatClientType(raw string) bool {
	switch normalizeClientType(raw) {
	case "", "openai", "openai_responses":
		return true
	default:
		return false
	}
}
