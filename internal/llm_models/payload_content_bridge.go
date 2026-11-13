package llm

// Context 项的载荷定义位于 payload_content 子包（对齐 Python
// src/llm_models/payload_content/）。
//
// 这里用类型别名 + 常量 + 薄转发保持父包 API 不变。
// 子包不依赖父包任何符号，属干净的向下依赖。

import (
	payloadcontent "maibot-go/internal/llm_models/payload_content"
)

type (
	ContextContentPart            = payloadcontent.ContextContentPart
	ContextContentPartType        = payloadcontent.ContextContentPartType
	ContextItem                   = payloadcontent.ContextItem
	ContextItemMeta               = payloadcontent.ContextItemMeta
	ContextItemType               = payloadcontent.ContextItemType
	ContextMedia                  = payloadcontent.ContextMedia
	ContextProtocolMode           = payloadcontent.ContextProtocolMode
	ContextProviderReplayFragment = payloadcontent.ContextProviderReplayFragment
	ContextProviderScope          = payloadcontent.ContextProviderScope
	ContextRelationReport         = payloadcontent.ContextRelationReport
	ContextRoleType               = payloadcontent.ContextRoleType
	ContextToolCall               = payloadcontent.ContextToolCall
)

const (
	ContextItemSystemMessage      = payloadcontent.ContextItemSystemMessage
	ContextItemUserMessage        = payloadcontent.ContextItemUserMessage
	ContextItemReasoning          = payloadcontent.ContextItemReasoning
	ContextItemAssistantMessage   = payloadcontent.ContextItemAssistantMessage
	ContextItemFunctionCall       = payloadcontent.ContextItemFunctionCall
	ContextItemFunctionCallOutput = payloadcontent.ContextItemFunctionCallOutput
	ContextItemProviderActivity   = payloadcontent.ContextItemProviderActivity
	ContextItemProviderOpaque     = payloadcontent.ContextItemProviderOpaque
	ContextRoleSystem             = payloadcontent.ContextRoleSystem
	ContextRoleUser               = payloadcontent.ContextRoleUser
	ContextRoleAssistant          = payloadcontent.ContextRoleAssistant
	ContextRoleTool               = payloadcontent.ContextRoleTool
	ContextPartText               = payloadcontent.ContextPartText
	ContextPartImage              = payloadcontent.ContextPartImage
	ContextPartRefusal            = payloadcontent.ContextPartRefusal

	// ContextItemSchemaVersion 对齐 MaiBot CONTEXT_ITEM_SCHEMA_VERSION
	ContextItemSchemaVersion = payloadcontent.ContextItemSchemaVersion

	// ContextProtocol* 不同生命周期边界的关系约束模式
	ContextProtocolRequestContext = payloadcontent.ContextProtocolRequestContext
	ContextProtocolModelOutput    = payloadcontent.ContextProtocolModelOutput
	ContextProtocolHistory        = payloadcontent.ContextProtocolHistory
)

// 以下为薄转发：保持父包函数名可调用
var AnalyzeContextItemRelations = payloadcontent.AnalyzeContextItemRelations
var BindOutputItemsToTurn = payloadcontent.BindOutputItemsToTurn
var BuildPortableOutputItems = payloadcontent.BuildPortableOutputItems
var BuildProviderEndpointFingerprint = payloadcontent.BuildProviderEndpointFingerprint
var DeserializeContextItemSnapshot = payloadcontent.DeserializeContextItemSnapshot
var DeserializeContextItemsSnapshot = payloadcontent.DeserializeContextItemsSnapshot
var GetContextItemReplay = payloadcontent.GetContextItemReplay
var GetContextItemText = payloadcontent.GetContextItemText
var GetContextResponseReasoning = payloadcontent.GetContextResponseReasoning
var GetContextResponseText = payloadcontent.GetContextResponseText
var GetContextResponseToolCalls = payloadcontent.GetContextResponseToolCalls
var NewAssistantMessageItem = payloadcontent.NewAssistantMessageItem
var NewContextItemMeta = payloadcontent.NewContextItemMeta
var NewContextProviderReplayFragment = payloadcontent.NewContextProviderReplayFragment
var NewContextToolCall = payloadcontent.NewContextToolCall
var NewFunctionCallItem = payloadcontent.NewFunctionCallItem
var NewFunctionCallOutputItem = payloadcontent.NewFunctionCallOutputItem
var NewProviderActivityItem = payloadcontent.NewProviderActivityItem
var NewProviderOpaqueItem = payloadcontent.NewProviderOpaqueItem
var NewReasoningItem = payloadcontent.NewReasoningItem
var NewSystemMessageItem = payloadcontent.NewSystemMessageItem
var NewUserMessageItem = payloadcontent.NewUserMessageItem
var PruneContextItemsForHistory = payloadcontent.PruneContextItemsForHistory
var ReplaceOutputProjection = payloadcontent.ReplaceOutputProjection
var SelectContextItemsWithProtocolClosure = payloadcontent.SelectContextItemsWithProtocolClosure
var SerializeContextItemSnapshot = payloadcontent.SerializeContextItemSnapshot
var SerializeContextItemsSnapshot = payloadcontent.SerializeContextItemsSnapshot
var ValidateContextItems = payloadcontent.ValidateContextItems
var WithoutContextItemReplay = payloadcontent.WithoutContextItemReplay

// StripPrivateProviderValue 去掉私有 provider 扩展字段
var StripPrivateProviderValue = payloadcontent.StripPrivateProviderValue

// SupportedContextImageFormats 上下文图片允许的格式白名单
var SupportedContextImageFormats = payloadcontent.SupportedContextImageFormats

// stripPrivateProviderValue 保持父包内原有的小写调用名
func stripPrivateProviderValue(value any) any {
	return payloadcontent.StripPrivateProviderValue(value)
}

// supportedContextImageFormats 保持父包内原有的小写引用名
var supportedContextImageFormats = payloadcontent.SupportedContextImageFormats
