package llm

// 客户端实现子包的转发层。
//
// 目录对齐 MaiBot src/llm_models/model_client/：OpenAI Chat Completions、
// Gemini、OpenAI Responses、Plugin 四类客户端与嵌入/音频客户端的实现全部
// 位于 model_client 子包。父包保留路由、任务配置、快照、统计与用量记账，
// 这里把子包导出符号接回父包命名空间，调用点无需改动。
//
// 依赖方向：llm -> model_client -> contract / payload_content，单向不成环。
// 子包反向需要的快照/统计/图片处理能力，由 model_client_bridge_ports.go 注入。

import (
	"MaiGo/internal/llm_models/contract"
	"MaiGo/internal/llm_models/model_client"
)

type (
	// ModelConfig 一次模型客户端构建所需的非敏感配置
	//
	// 定义在 contract 包：客户端实现与快照/统计层共用这份纯数据契约，
	// 放在任一侧都会让另一侧反向依赖。别名使包外调用点（llm.ModelConfig）不变。
	ModelConfig = contract.ModelConfig
	// ModelPricePeriod 按时段覆盖的价格档
	ModelPricePeriod = contract.ModelPricePeriod

	// EmbeddingClient 文本/图片嵌入客户端
	EmbeddingClient = model_client.EmbeddingClient
	// ConcurrentEmbeddingClient 支持并发生成的嵌入客户端
	ConcurrentEmbeddingClient = model_client.ConcurrentEmbeddingClient
	// EmbeddingResult 一次嵌入结果
	EmbeddingResult = model_client.EmbeddingResult
	// ImageEmbeddingInput 图片嵌入输入
	ImageEmbeddingInput = model_client.ImageEmbeddingInput
	// AudioTranscriber 音频转写客户端
	AudioTranscriber = model_client.AudioTranscriber
	// AudioTranscriptionRequest 音频转写请求
	AudioTranscriptionRequest = model_client.AudioTranscriptionRequest
	// AudioTranscriptionResult 音频转写结果
	AudioTranscriptionResult = model_client.AudioTranscriptionResult
	// ResponsesResponseFormatModel Responses 的结构化输出包装模型
	ResponsesResponseFormatModel = model_client.ResponsesResponseFormatModel
)

var (
	// 客户端工厂：按 client_type 分流
	NewClient = model_client.NewClient
	// Test 用极短请求验证模型连接
	Test = model_client.Test

	// 客户端构造
	NewChatCompletionsClient = model_client.NewChatCompletionsClient
	NewGeminiClient          = model_client.NewGeminiClient
	NewEmbeddingClient       = model_client.NewEmbeddingClient
	NewAudioTranscriber      = model_client.NewAudioTranscriber
	newPluginModel           = model_client.NewPluginModel

	// 上下文项投影与请求构造
	ContextItemsFromMessages            = model_client.ContextItemsFromMessages
	ContextItemsFromMessagesWithLimits  = model_client.ContextItemsFromMessagesWithLimits
	AgenticMessageFromContextItems      = model_client.AgenticMessageFromContextItems
	buildChatMessagesFromContextItems   = model_client.BuildChatMessagesFromContextItems
	buildChatToolDefinitions            = model_client.BuildChatToolDefinitions
	buildGeminiContentsFromContextItems = model_client.BuildGeminiContentsFromContextItems
	buildResponsesReplayRequest         = model_client.BuildResponsesReplayRequest
	chatImageFormat                     = model_client.ChatImageFormat
	chatToolChoiceValue                 = model_client.ChatToolChoiceValue
	chatToolResultText                  = model_client.ChatToolResultText
	contextImageMIME                    = model_client.ContextImageMIME
	newContextUUID                      = model_client.NewContextUUID
	splitResponsesExtraParams           = model_client.SplitResponsesExtraParams
	embedTextsConcurrently              = model_client.EmbedTextsConcurrently
	intFromAny                          = model_client.IntFromAny
	modelInfoSnapshotForConfig          = model_client.ModelInfoSnapshotForConfig
	pluginProviderAvailable             = model_client.PluginProviderAvailable
	pluginToolOptions                   = model_client.PluginToolOptions
	providerAuthHeaderName              = model_client.ProviderAuthHeaderName
	providerAuthHeaderPrefix            = model_client.ProviderAuthHeaderPrefix
	providerAuthHeaderPrefixWithSet     = model_client.ProviderAuthHeaderPrefixWithSet
	providerAuthQueryName               = model_client.ProviderAuthQueryName
	providerAuthType                    = model_client.ProviderAuthType
	providerDurationSeconds             = model_client.ProviderDurationSeconds
	providerModelListEndpoint           = model_client.ProviderModelListEndpoint
	providerOptionalString              = model_client.ProviderOptionalString
	providerParseMode                   = model_client.ProviderParseMode
	providerStringMapOrEmpty            = model_client.ProviderStringMapOrEmpty
)

var (
	// NewResponsesResponseFormatModel Responses 结构化输出包装模型构造器
	NewResponsesResponseFormatModel = model_client.NewResponsesResponseFormatModel
)

type (
	// PluginLLMResult 插件 LLM 调用结果
	PluginLLMResult = model_client.PluginLLMResult
	// PluginLLMInvoker 插件侧 LLM 调用器
	PluginLLMInvoker = model_client.PluginLLMInvoker
)

var (
	// RegisterPluginProvider 注册插件 LLM Provider
	RegisterPluginProvider            = model_client.RegisterPluginProvider
	RegisterPluginProviderFormats     = model_client.RegisterPluginProviderFormats
	// UnregisterPluginProvider 注销插件 LLM Provider
	UnregisterPluginProvider = model_client.UnregisterPluginProvider

	// WithResponseFormat 在上下文注入任务级 response_format
	WithResponseFormat = contract.WithResponseFormat

	// WithStreamPartialSink 在上下文注入流式中断快照接收器
	WithStreamPartialSink = contract.WithStreamPartialSink
	// StreamPartialSinkFromContext 读取上下文里的流式中断快照接收器
	StreamPartialSinkFromContext = contract.StreamPartialSinkFromContext
)

type (
	// StreamPartialSink 流式中断未完成输出接收器
	StreamPartialSink = contract.StreamPartialSink
	// StreamPartialOutput 流式中断未完成输出快照
	StreamPartialOutput = contract.StreamPartialOutput
	// StreamPartialToolCall 流式工具调用片段
	StreamPartialToolCall = contract.StreamPartialToolCall
)

const (
	// responsesOperation Responses API 的快照操作名
	responsesOperation = model_client.ResponsesOperation
	// responsesEndpoint Responses API 的端点路径
	responsesEndpoint = model_client.ResponsesEndpoint
)

var (
	// newOpenAIResponsesClient Responses 客户端构造（保留迁移前的小写写法）
	newOpenAIResponsesClient = model_client.NewOpenAIResponsesClient
)
