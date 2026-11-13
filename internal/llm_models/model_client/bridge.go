package model_client

// 契约与上下文项的类型别名。
//
// 从父包（package llm）迁移过来的客户端实现保持原样，靠这里的别名把
// contract / payload_content 的导出符号接进本包命名空间，避免逐个改调用点。
// 依赖方向始终是 model_client -> contract / payload_content，不会成环。

import (
	contract "maibot-go/internal/llm_models/contract"
	payloadcontent "maibot-go/internal/llm_models/payload_content"
)

type (
	// ModelConfig 模型档案配置
	ModelConfig = contract.ModelConfig
	// TaskName 模型任务名
	TaskName = contract.TaskName
	// ResponseFormatSpec 结构化输出描述
	ResponseFormatSpec = contract.ResponseFormatSpec
	// RequestSnapshotSpec 失败请求快照描述
	RequestSnapshotSpec = contract.RequestSnapshotSpec
	// AttemptRecorder 单次请求的尝试记录器
	AttemptRecorder = contract.AttemptRecorder
	// RetryAction 重试决策
	RetryAction = contract.RetryAction

	// HTTPStatusError Provider 返回非 2xx
	HTTPStatusError = contract.HTTPStatusError
	// EmptyResponseError 响应体为空
	EmptyResponseError = contract.EmptyResponseError
	// ParseError 响应解析失败
	ParseError = contract.ParseError
	// NetworkError 网络层失败
	NetworkError = contract.NetworkError
	// AbortError 请求被上下文取消
	AbortError = contract.AbortError
	// TaskTimeoutError 任务级超时
	TaskTimeoutError = contract.TaskTimeoutError

	// ContextItem 上下文项
	ContextItem = payloadcontent.ContextItem
	// ContextContentPart 内容片段
	ContextContentPart = payloadcontent.ContextContentPart
	// ContextMedia 非文本媒体
	ContextMedia = payloadcontent.ContextMedia
	// ContextToolCall 工具调用
	ContextToolCall = payloadcontent.ContextToolCall
	// ContextItemMeta 上下文项元信息
	ContextItemMeta = payloadcontent.ContextItemMeta
	// ContextProviderReplayFragment Provider 重放片段
	ContextProviderReplayFragment = payloadcontent.ContextProviderReplayFragment
	// ContextProtocolMode 上下文关系约束模式
	ContextProtocolMode = payloadcontent.ContextProtocolMode

	// snapshotAttemptRecorder 单次请求的尝试记录器（迁移前的小写写法）
	snapshotAttemptRecorder = contract.AttemptRecorder
	// requestSnapshotSpec 失败请求快照描述（迁移前的小写写法）
	requestSnapshotSpec = contract.RequestSnapshotSpec

	// StreamPartialOutput 流式中断未完成输出快照
	StreamPartialOutput = contract.StreamPartialOutput
	// StreamPartialToolCall 流式工具调用片段
	StreamPartialToolCall = contract.StreamPartialToolCall
	// StreamPartialSink 流式中断未完成输出接收器
	StreamPartialSink = contract.StreamPartialSink
)

const (
	// ContextItemSchemaVersion 上下文项 schema 版本
	ContextItemSchemaVersion = payloadcontent.ContextItemSchemaVersion

	// ContextItemSystemMessage 系统消息项
	ContextItemSystemMessage = payloadcontent.ContextItemSystemMessage
	// ContextItemUserMessage 用户消息项
	ContextItemUserMessage = payloadcontent.ContextItemUserMessage
	// ContextItemReasoning 推理项
	ContextItemReasoning = payloadcontent.ContextItemReasoning
	// ContextItemAssistantMessage 助手消息项
	ContextItemAssistantMessage = payloadcontent.ContextItemAssistantMessage
	// ContextItemFunctionCall 工具调用项
	ContextItemFunctionCall = payloadcontent.ContextItemFunctionCall
	// ContextItemFunctionCallOutput 工具结果项
	ContextItemFunctionCallOutput = payloadcontent.ContextItemFunctionCallOutput
	// ContextItemProviderActivity Provider 活动项
	ContextItemProviderActivity = payloadcontent.ContextItemProviderActivity
	// ContextItemProviderOpaque Provider 不透明项
	ContextItemProviderOpaque = payloadcontent.ContextItemProviderOpaque

	// ContextPartText 文本片段
	ContextPartText = payloadcontent.ContextPartText
	// ContextPartImage 图片片段
	ContextPartImage = payloadcontent.ContextPartImage
	// ContextPartRefusal 拒答片段
	ContextPartRefusal = payloadcontent.ContextPartRefusal

	// ContextProtocolRequestContext 请求上下文模式
	ContextProtocolRequestContext = payloadcontent.ContextProtocolRequestContext
	// ContextProtocolModelOutput 模型输出模式
	ContextProtocolModelOutput = payloadcontent.ContextProtocolModelOutput
	// ContextProtocolHistory 历史模式
	ContextProtocolHistory = payloadcontent.ContextProtocolHistory

	// RequestKindResponse 普通响应请求
	RequestKindResponse = contract.RequestKindResponse
	// RequestKindEmbedding 文本嵌入请求
	RequestKindEmbedding = contract.RequestKindEmbedding
	// RequestKindImageEmbedding 图片嵌入请求
	RequestKindImageEmbedding = contract.RequestKindImageEmbedding
	// RequestKindAudio 音频转写请求
	RequestKindAudio = contract.RequestKindAudio

	// requestKindResponse 普通响应请求（迁移前的小写写法）
	requestKindResponse = contract.RequestKindResponse
	// requestKindEmbedding 文本嵌入请求（迁移前的小写写法）
	requestKindEmbedding = contract.RequestKindEmbedding
	// requestKindImageEmbedding 图片嵌入请求（迁移前的小写写法）
	requestKindImageEmbedding = contract.RequestKindImageEmbedding

	// authTypeBearer Bearer 鉴权
	authTypeBearer = contract.AuthTypeBearer
	// authTypeHeader 自定义头鉴权
	authTypeHeader = contract.AuthTypeHeader
	// authTypeQuery 查询参数鉴权
	authTypeQuery = contract.AuthTypeQuery
	// authTypeNone 无需鉴权
	authTypeNone = contract.AuthTypeNone
	// authorizationHeader 标准鉴权头名
	authorizationHeader = contract.AuthorizationHeader
)

var (
	// snapshotAttemptRecorderFromContext 从上下文取尝试记录器
	snapshotAttemptRecorderFromContext = contract.AttemptRecorderFromContext
	// snapshotLogicalTurnID 本轮固定标识
	snapshotLogicalTurnID = contract.SnapshotLogicalTurnID
	// newRequestSnapshotSpec 构造失败快照描述
	newRequestSnapshotSpec = contract.NewRequestSnapshotSpec

	// 上下文项构造与校验
	NewSystemMessageItem           = payloadcontent.NewSystemMessageItem
	NewUserMessageItem             = payloadcontent.NewUserMessageItem
	NewAssistantMessageItem        = payloadcontent.NewAssistantMessageItem
	NewReasoningItem               = payloadcontent.NewReasoningItem
	NewFunctionCallItem            = payloadcontent.NewFunctionCallItem
	NewFunctionCallOutputItem      = payloadcontent.NewFunctionCallOutputItem
	NewProviderActivityItem        = payloadcontent.NewProviderActivityItem
	NewProviderOpaqueItem          = payloadcontent.NewProviderOpaqueItem
	NewContextItemMeta             = payloadcontent.NewContextItemMeta
	NewContextToolCall             = payloadcontent.NewContextToolCall
	GetContextItemText             = payloadcontent.GetContextItemText
	BuildPortableOutputItems       = payloadcontent.BuildPortableOutputItems
	ValidateContextItems           = payloadcontent.ValidateContextItems
	SerializeContextItemsSnapshot  = payloadcontent.SerializeContextItemsSnapshot
	DeserializeContextItemSnapshot = payloadcontent.DeserializeContextItemSnapshot
	SupportedContextImageFormats   = payloadcontent.SupportedContextImageFormats

	// supportedContextImageFormats 上下文图片白名单（迁移前的小写写法）
	supportedContextImageFormats = payloadcontent.SupportedContextImageFormats

	// 鉴权与传输
	buildOpenAICompatAuth           = contract.BuildOpenAICompatAuth
	canonicalizeAuthorizationHeader = contract.CanonicalizeAuthorizationHeader
	normalizeOpenAIBaseURL          = contract.NormalizeOpenAIBaseURL
	newProviderTransport            = contract.NewProviderTransport
	newLLMHTTPClient                = contract.NewLLMHTTPClient
	applyReplayHeaders              = contract.ApplyReplayHeaders
	buildAuthHeaderValue            = contract.BuildAuthHeaderValue

	// 客户端类型判定
	normalizeClientType         = contract.NormalizeClientType
	isGeminiClientType          = contract.IsGeminiClientType
	isChatCompletionsClientType = contract.IsChatCompletionsClientType
	isOpenAIResponsesClientType = contract.IsOpenAIResponsesClientType
	isOpenAICompatClientType    = contract.IsOpenAICompatClientType

	// 生效参数与结构化输出
	resolveEffectiveParams    = contract.ResolveEffectiveParams
	effectiveSendTemperature  = contract.EffectiveSendTemperature
	parseResponseFormat       = contract.ParseResponseFormat
	chatResponseFormat        = contract.ChatResponseFormat
	applyGeminiResponseFormat = contract.ApplyGeminiResponseFormat
	chatScalarString          = contract.ChatScalarString
	responseFormatFromContext = contract.ResponseFormatFromContext
	effectiveResponseFormat   = contract.EffectiveResponseFormat
	responseFormatSnapshot    = contract.ResponseFormatSnapshot

	// splitResponsesExtraParams 拆分 Responses 的 extra_params
	splitResponsesExtraParams = SplitResponsesExtraParams

	// 零散工具
	trimTask               = contract.TrimTask
	cloneAnyMap            = contract.CloneAnyMap
	floatFromAny           = contract.FloatFromAny
	nonEmptyStrings        = contract.NonEmptyStrings
	providerMaxRetry       = contract.ProviderMaxRetry
	providerRetryAttempts  = contract.ProviderRetryAttempts
	replayString           = contract.ReplayString
	replayStringMap        = contract.ReplayStringMap
	firstReplayString      = contract.FirstReplayString
	replayBody             = contract.ReplayBody
	replayToolDefinitions  = contract.ReplayToolDefinitions
	metadataString         = contract.MetadataString
	snapshotImageExtension = contract.SnapshotImageExtension
	abortErrorFromContext  = contract.AbortErrorFromContext
)

var (
	// WithResponseFormat 在请求上下文注入任务级 response_format
	WithResponseFormat = contract.WithResponseFormat
	// StreamPartialSinkFromContext 读取请求上下文里的中断快照接收器
	StreamPartialSinkFromContext = contract.StreamPartialSinkFromContext
)
