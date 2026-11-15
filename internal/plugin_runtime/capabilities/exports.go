package capabilities

import (
	"MaiGo/internal/common/database"
	filesvc "MaiGo/internal/files"
)

// 能力 handler 的导出入口。
//
// 父包 host_capabilities.go 负责把 deps 注入并注册到 Host，因此需要能调用
// 这些工厂函数。工厂本身留在各自 cap_*.go 里保持与小写命名的内聚，
// 这里统一加一层导出，避免把内部命名全改成大写。

// MakeMessageGetByTimeHandler 按时间范围查询消息
func MakeMessageGetByTimeHandler(mem *memory.Manager) CapabilityHandler {
	return makeMessageGetByTimeHandler(mem)
}

// MakeMessageGetByTimeInChatHandler 按时间范围查询指定会话的消息
func MakeMessageGetByTimeInChatHandler(mem *memory.Manager) CapabilityHandler {
	return makeMessageGetByTimeInChatHandler(mem)
}

// MakeMessageGetByIDHandler 按消息 ID 查询
func MakeMessageGetByIDHandler(mem *memory.Manager) CapabilityHandler {
	return makeMessageGetByIDHandler(mem)
}

// MakeMessageGetRecentHandler 查询最近消息
func MakeMessageGetRecentHandler(mem *memory.Manager) CapabilityHandler {
	return makeMessageGetRecentHandler(mem)
}

// MakeMessageCountNewHandler 统计新消息数
func MakeMessageCountNewHandler(mem *memory.Manager) CapabilityHandler {
	return makeMessageCountNewHandler(mem)
}

// MakeMessageBuildReadableHandler 构造可读文本
func MakeMessageBuildReadableHandler(mem *memory.Manager, instance BotConfigSource) CapabilityHandler {
	return makeMessageBuildReadableHandler(mem, instance)
}

// MakePersonalityListHandler 列出人格档案
func MakePersonalityListHandler(deps HostCapabilityDeps) CapabilityHandler {
	return makePersonalityListHandler(deps)
}

// MakePersonalityGetCurrentHandler 取当前人格档案
func MakePersonalityGetCurrentHandler(deps HostCapabilityDeps) CapabilityHandler {
	return makePersonalityGetCurrentHandler(deps)
}

// MakePersonalitySwitchHandler 切换人格档案
func MakePersonalitySwitchHandler(deps HostCapabilityDeps) CapabilityHandler {
	return makePersonalitySwitchHandler(deps)
}

// MakeDatabaseQueryHandler 数据库查询能力
func MakeDatabaseQueryHandler(port PluginDatabasePort) CapabilityHandler {
	return makeDatabaseQueryHandler(port)
}

// MakeDatabaseSaveHandler 数据库写入能力
func MakeDatabaseSaveHandler(port PluginDatabasePort) CapabilityHandler {
	return makeDatabaseSaveHandler(port)
}

// MakeDatabaseGetHandler 数据库按主键读取能力
func MakeDatabaseGetHandler(port PluginDatabasePort) CapabilityHandler {
	return makeDatabaseGetHandler(port)
}

// MakeDatabaseDeleteHandler 数据库删除能力
func MakeDatabaseDeleteHandler(port PluginDatabasePort) CapabilityHandler {
	return makeDatabaseDeleteHandler(port)
}

// MakeDatabaseCountHandler 数据库计数能力
func MakeDatabaseCountHandler(port PluginDatabasePort) CapabilityHandler {
	return makeDatabaseCountHandler(port)
}

// MakeLLMCapabilityHandler 模型调用能力
func MakeLLMCapabilityHandler(port PluginLLMPort, capability string) CapabilityHandler {
	return makeLLMCapabilityHandler(port, capability)
}

// MakeSendCapabilityHandler 消息发送能力
func MakeSendCapabilityHandler(sender PluginSendPort, capability string) CapabilityHandler {
	return makeSendCapabilityHandler(sender, capability)
}

// MakeInternalChatIdentityHandler 内部聊天身份能力
func MakeInternalChatIdentityHandler() CapabilityHandler {
	return makeInternalChatIdentityHandler()
}

// MakeInternalChatSendHandler 内部聊天发送能力
func MakeInternalChatSendHandler(relay PluginChatRelay) CapabilityHandler {
	return makeInternalChatSendHandler(relay)
}

// MakeChatListStreamsHandler 列出聊天流能力
func MakeChatListStreamsHandler(port PluginSessionPort, capability string) CapabilityHandler {
	return makeChatListStreamsHandler(port, capability)
}

// MakeChatOpenSessionHandler 打开会话能力
func MakeChatOpenSessionHandler(port PluginSessionPort) CapabilityHandler {
	return makeChatOpenSessionHandler(port)
}

// MakeChatStreamLookupHandler 查找聊天流能力
func MakeChatStreamLookupHandler(port PluginSessionPort, capability string) CapabilityHandler {
	return makeChatStreamLookupHandler(port, capability)
}

// MakeChatAvatarHandler 取会话头像能力
func MakeChatAvatarHandler(port PluginSessionPort) CapabilityHandler {
	return makeChatAvatarHandler(port)
}

// MakeEmojiGetByDescriptionHandler 按描述搜表情
func MakeEmojiGetByDescriptionHandler(deps HostCapabilityDeps) CapabilityHandler {
	return makeEmojiGetByDescriptionHandler(deps)
}

// MakeEmojiGetRandomHandler 随机表情
func MakeEmojiGetRandomHandler(deps HostCapabilityDeps) CapabilityHandler {
	return makeEmojiGetRandomHandler(deps)
}

// MakeEmojiGetCountHandler 表情数量
func MakeEmojiGetCountHandler(deps HostCapabilityDeps) CapabilityHandler {
	return makeEmojiGetCountHandler(deps)
}

// MakeEmojiGetEmotionsHandler 表情情绪列表
func MakeEmojiGetEmotionsHandler(deps HostCapabilityDeps) CapabilityHandler {
	return makeEmojiGetEmotionsHandler(deps)
}

// MakeEmojiGetAllHandler 全部表情
func MakeEmojiGetAllHandler(deps HostCapabilityDeps) CapabilityHandler {
	return makeEmojiGetAllHandler(deps)
}

// MakeEmojiGetInfoHandler 表情详情
func MakeEmojiGetInfoHandler(deps HostCapabilityDeps) CapabilityHandler {
	return makeEmojiGetInfoHandler(deps)
}

// MakeEmojiRegisterHandler 注册表情
func MakeEmojiRegisterHandler(deps HostCapabilityDeps) CapabilityHandler {
	return makeEmojiRegisterHandler(deps)
}

// MakeEmojiDeleteHandler 删除表情
func MakeEmojiDeleteHandler(deps HostCapabilityDeps) CapabilityHandler {
	return makeEmojiDeleteHandler(deps)
}

// MakePersonGetIDHandler 取人物 ID
func MakePersonGetIDHandler(port PluginPersonPort) CapabilityHandler {
	return makePersonGetIDHandler(port)
}

// MakePersonGetValueHandler 取人物字段
func MakePersonGetValueHandler(port PluginPersonPort) CapabilityHandler {
	return makePersonGetValueHandler(port)
}

// MakePersonGetIDByNameHandler 按名字取人物 ID
func MakePersonGetIDByNameHandler(port PluginPersonPort) CapabilityHandler {
	return makePersonGetIDByNameHandler(port)
}

// MakeFilesCapabilityHandler 文件能力
func MakeFilesCapabilityHandler(svc *filesvc.Service) CapabilityHandler {
	return makeFilesCapabilityHandler(svc)
}
