package model_client

import "github.com/cloudwego/eino/components/model"

// 供父包（llm）及其测试使用的导出层。
//
// 迁移自父包的实现里，部分构造函数与常量仍是包内可见。父包的嵌入服务、
// 音频路由与测试需要它们，这里统一包一层导出，避免把内部命名细节扩散出去。

// NewOpenAIEmbeddingClient 构造 OpenAI 兼容协议族的嵌入客户端
func NewOpenAIEmbeddingClient(cfg ModelConfig) (EmbeddingClient, error) {
	return newOpenAIEmbeddingClient(cfg)
}

// NewGeminiEmbeddingClient 构造 Gemini 嵌入客户端
func NewGeminiEmbeddingClient(cfg ModelConfig) (EmbeddingClient, error) {
	return newGeminiEmbeddingClient(cfg)
}

// EmbeddingLimiterKey 嵌入速率限制器的键（provider + baseURL + model）
func EmbeddingLimiterKey(cfg ModelConfig) string {
	return embeddingLimiterKey(cfg)
}

// GeminiSafetySettings 对齐 MaiBot GEMINI_SAFE_SETTINGS 的安全设置
var GeminiSafetySettings = geminiSafetySettings

const (
	// ResponsesOperation Responses API 的快照操作名
	ResponsesOperation = responsesOperation
	// ResponsesEndpoint Responses API 的端点路径
	ResponsesEndpoint = responsesEndpoint
)

// NewOpenAIResponsesClient 构造 OpenAI Responses 客户端
func NewOpenAIResponsesClient(cfg ModelConfig) (model.AgenticModel, error) {
	return newOpenAIResponsesClient(cfg)
}
