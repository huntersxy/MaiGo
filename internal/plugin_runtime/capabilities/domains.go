package capabilities

// CoreCapability 对齐 src/plugin_runtime/capabilities/core.py（RuntimeCoreCapabilityMixin）
//
// 典型能力名：send.text、send.emoji、llm.generate、llm.embed、config.get、config.get_plugin 等
type CoreCapability interface {
	coreCapability()
}

// DataCapability 对齐 src/plugin_runtime/capabilities/data.py（RuntimeDataCapabilityMixin）
//
// 典型能力名：chat.*、message.*、database.*、emoji.*、person.*、maisaka.context.*、knowledge.search 等
type DataCapability interface {
	dataCapability()
}

// WebCapability 对齐 src/plugin_runtime/capabilities/web.py
//
// 典型能力名：web.search、web.fetch
type WebCapability interface {
	webCapability()
}

// PersonalityCapability 对齐 src/plugin_runtime/capabilities/personality.py
//
// 典型能力名：personality.list、personality.get_current、personality.switch
type PersonalityCapability interface {
	personalityCapability()
}

// 能力名常量，逐条对应 src/plugin_runtime/capabilities/registry.py 的注册表
const (
	CapWebUIClaimUpload = "webui.claim_upload"

	CapInternalChatIdentity = "internal_chat.identity"
	CapInternalChatSend     = "internal_chat.send"

	CapPersonalityList    = "personality.list"
	CapPersonalityCurrent = "personality.get_current"
	CapPersonalitySwitch  = "personality.switch"

	CapDesktopPetStatus         = "desktop_pet.status"
	CapDesktopPetShow           = "desktop_pet.show"
	CapDesktopPetHide           = "desktop_pet.hide"
	CapDesktopPetSay            = "desktop_pet.say"
	CapDesktopPetSetThinking    = "desktop_pet.set_thinking"
	CapDesktopPetRefreshButtons = "desktop_pet.refresh_buttons"
	CapDesktopPetGetSkin        = "desktop_pet.get_skin"
	CapDesktopPetListAnimations = "desktop_pet.list_animations"
	CapDesktopPetPlayAnimation  = "desktop_pet.play_animation"
	CapDesktopPetPlayEvent      = "desktop_pet.play_event"
	CapDesktopPetStopAnimation  = "desktop_pet.stop_animation"
	CapDesktopPetAnimationState = "desktop_pet.animation_status"

	CapFilesList     = "files.list"
	CapFilesReadText = "files.read_text"
	CapFilesDownload = "files.download"
	CapFilesUpload   = "files.upload"
	CapFilesSend     = "files.send"

	CapSendText    = "send.text"
	CapSendEmoji   = "send.emoji"
	CapSendImage   = "send.image"
	CapSendForward = "send.forward"
	CapSendHybrid  = "send.hybrid"
	CapSendCommand = "send.command"
	CapSendCustom  = "send.custom"

	CapLLMGenerate           = "llm.generate"
	CapLLMGenerateWithTools  = "llm.generate_with_tools"
	CapLLMEmbed              = "llm.embed"
	CapLLMTranscribeAudio    = "llm.transcribe_audio"
	CapLLMGetAvailableModels = "llm.get_available_models"

	CapConfigGet       = "config.get"
	CapConfigGetPlugin = "config.get_plugin"
	CapConfigGetAll    = "config.get_all"

	CapDatabaseQuery  = "database.query"
	CapDatabaseSave   = "database.save"
	CapDatabaseGet    = "database.get"
	CapDatabaseDelete = "database.delete"
	CapDatabaseCount  = "database.count"

	CapChatGetAllStreams      = "chat.get_all_streams"
	CapChatGetAvatar          = "chat.get_avatar"
	CapChatGetGroupStreams    = "chat.get_group_streams"
	CapChatGetPrivateStreams  = "chat.get_private_streams"
	CapChatOpenSession        = "chat.open_session"
	CapChatGetStreamByGroupID = "chat.get_stream_by_group_id"
	CapChatGetStreamByUserID  = "chat.get_stream_by_user_id"

	CapMessageGetByTime       = "message.get_by_time"
	CapMessageGetByTimeInChat = "message.get_by_time_in_chat"
	CapMessageGetByID         = "message.get_by_id"
	CapMessageGetRecent       = "message.get_recent"
	CapMessageCountNew        = "message.count_new"
	CapMessageBuildReadable   = "message.build_readable"

	CapMaisakaContextAppend       = "maisaka.context.append"
	CapMaisakaContextResolveImage = "maisaka.context.resolve_image"
	CapMaisakaProactiveTrigger    = "maisaka.proactive.trigger"
	CapMaisakaReplyGenerate       = "maisaka.reply.generate"

	CapPersonGetID       = "person.get_id"
	CapPersonGetValue    = "person.get_value"
	CapPersonGetIDByName = "person.get_id_by_name"

	CapEmojiGetByDescription = "emoji.get_by_description"
	CapEmojiGetRandom        = "emoji.get_random"
	CapEmojiGetCount         = "emoji.get_count"
	CapEmojiGetEmotions      = "emoji.get_emotions"
	CapEmojiGetAll           = "emoji.get_all"
	CapEmojiGetInfo          = "emoji.get_info"
	CapEmojiRegister         = "emoji.register"
	CapEmojiDelete           = "emoji.delete"

	CapFrequencyGetCurrentTalkValue = "frequency.get_current_talk_value"
	CapFrequencySetAdjust           = "frequency.set_adjust"
	CapFrequencyGetAdjust           = "frequency.get_adjust"

	CapToolGetDefinitions = "tool.get_definitions"

	CapAPICall           = "api.call"
	CapAPIGet            = "api.get"
	CapAPIList           = "api.list"
	CapAPIReplaceDynamic = "api.replace_dynamic"
	CapRuntimeSetStatusBadges = "runtime.set_status_badges"

	CapComponentGetAllPlugins         = "component.get_all_plugins"
	CapComponentGetPluginInfo         = "component.get_plugin_info"
	CapComponentGetPluginConfigSchema = "component.get_plugin_config_schema"
	CapComponentUpdatePluginConfig    = "component.update_plugin_config"
	CapComponentListLoadedPlugins     = "component.list_loaded_plugins"
	CapComponentListRegisteredPlugins = "component.list_registered_plugins"
	CapComponentEnable                = "component.enable"
	CapComponentDisable               = "component.disable"
	CapComponentLoadPlugin            = "component.load_plugin"
	CapComponentUnloadPlugin          = "component.unload_plugin"
	CapComponentReloadPlugin          = "component.reload_plugin"

	CapKnowledgeSearch = "knowledge.search"
	CapMarketSearch    = "market.search"
	CapMarketGetDetail = "market.get_detail"
	CapMarketGetReadme = "market.get_readme"
	CapWebSearch       = "web.search"
	CapWebFetch        = "web.fetch"

	CapStatisticsLocalModels            = "statistics.local.models"
	CapStatisticsLocalModelTrend        = "statistics.local.model_trend"
	CapStatisticsLocalTokenTrend        = "statistics.local.token_trend"
	CapStatisticsLocalTokenDistribution = "statistics.local.token_distribution"
	CapStatisticsLocalMessageTrend      = "statistics.local.message_trend"
	CapStatisticsLocalToolTrend         = "statistics.local.tool_trend"
	CapStatisticsLocalOnlineTimeTrend   = "statistics.local.online_time_trend"

	CapRenderHTML2PNG = "render.html2png"
)

// CoreDomain 标记类型，供文档与后续 Host 接线使用
type CoreDomain struct{}

func (CoreDomain) coreCapability() {}

// DataDomain 标记类型
type DataDomain struct{}

func (DataDomain) dataCapability() {}

// WebDomain 标记类型
type WebDomain struct{}

func (WebDomain) webCapability() {}

// PersonalityDomain 标记类型
type PersonalityDomain struct{}

func (PersonalityDomain) personalityCapability() {}
