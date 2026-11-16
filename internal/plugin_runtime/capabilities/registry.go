package capabilities

import "strings"

// CapabilitySpec 一项能力的静态描述，供注册与文档生成使用
type CapabilitySpec struct {
	Name string
	// Domain 对应 Python 侧 mixin 文件，取值 core/data/component/web/personality 等
	Domain string
	// External 为 true 表示能力由模块外实现（如桌宠控制由 internal/desktoppet 注册）
	External bool
}

var capabilitySpecs = []CapabilitySpec{
	{Name: CapWebUIClaimUpload, Domain: "webui"},
	{Name: CapInternalChatIdentity, Domain: "internal_chat"},
	{Name: CapInternalChatSend, Domain: "internal_chat"},
	{Name: CapPersonalityList, Domain: "personality"},
	{Name: CapPersonalityCurrent, Domain: "personality"},
	{Name: CapPersonalitySwitch, Domain: "personality"},
	{Name: CapDesktopPetStatus, Domain: "desktop_pet", External: true},
	{Name: CapDesktopPetShow, Domain: "desktop_pet", External: true},
	{Name: CapDesktopPetHide, Domain: "desktop_pet", External: true},
	{Name: CapDesktopPetSay, Domain: "desktop_pet", External: true},
	{Name: CapDesktopPetSetThinking, Domain: "desktop_pet", External: true},
	{Name: CapDesktopPetRefreshButtons, Domain: "desktop_pet", External: true},
	{Name: CapDesktopPetGetSkin, Domain: "desktop_pet", External: true},
	{Name: CapDesktopPetListAnimations, Domain: "desktop_pet", External: true},
	{Name: CapDesktopPetPlayAnimation, Domain: "desktop_pet", External: true},
	{Name: CapDesktopPetPlayEvent, Domain: "desktop_pet", External: true},
	{Name: CapDesktopPetStopAnimation, Domain: "desktop_pet", External: true},
	{Name: CapDesktopPetAnimationState, Domain: "desktop_pet", External: true},
	{Name: CapFilesList, Domain: "files", External: true},
	{Name: CapFilesReadText, Domain: "files", External: true},
	{Name: CapFilesDownload, Domain: "files", External: true},
	{Name: CapFilesUpload, Domain: "files", External: true},
	{Name: CapFilesSend, Domain: "files", External: true},
	{Name: CapSendText, Domain: "core"},
	{Name: CapSendEmoji, Domain: "core"},
	{Name: CapSendImage, Domain: "core"},
	{Name: CapSendForward, Domain: "core"},
	{Name: CapSendHybrid, Domain: "core"},
	{Name: CapSendCommand, Domain: "core"},
	{Name: CapSendCustom, Domain: "core"},
	{Name: CapLLMGenerate, Domain: "core"},
	{Name: CapLLMGenerateWithTools, Domain: "core"},
	{Name: CapLLMEmbed, Domain: "core"},
	{Name: CapLLMTranscribeAudio, Domain: "core"},
	{Name: CapLLMGetAvailableModels, Domain: "core"},
	{Name: CapConfigGet, Domain: "core"},
	{Name: CapConfigGetPlugin, Domain: "core"},
	{Name: CapConfigGetAll, Domain: "core"},
	{Name: CapDatabaseQuery, Domain: "data"},
	{Name: CapDatabaseSave, Domain: "data"},
	{Name: CapDatabaseGet, Domain: "data"},
	{Name: CapDatabaseDelete, Domain: "data"},
	{Name: CapDatabaseCount, Domain: "data"},
	{Name: CapChatGetAllStreams, Domain: "data"},
	{Name: CapChatGetAvatar, Domain: "data"},
	{Name: CapChatGetGroupStreams, Domain: "data"},
	{Name: CapChatGetPrivateStreams, Domain: "data"},
	{Name: CapChatOpenSession, Domain: "data"},
	{Name: CapChatGetStreamByGroupID, Domain: "data"},
	{Name: CapChatGetStreamByUserID, Domain: "data"},
	{Name: CapMessageGetByTime, Domain: "data"},
	{Name: CapMessageGetByTimeInChat, Domain: "data"},
	{Name: CapMessageGetByID, Domain: "data"},
	{Name: CapMessageGetRecent, Domain: "data"},
	{Name: CapMessageCountNew, Domain: "data"},
	{Name: CapMessageBuildReadable, Domain: "data"},
	{Name: CapMaisakaContextAppend, Domain: "maisaka"},
	{Name: CapMaisakaContextResolveImage, Domain: "maisaka"},
	{Name: CapMaisakaProactiveTrigger, Domain: "maisaka"},
	{Name: CapMaisakaReplyGenerate, Domain: "maisaka"},
	{Name: CapPersonGetID, Domain: "data"},
	{Name: CapPersonGetValue, Domain: "data"},
	{Name: CapPersonGetIDByName, Domain: "data"},
	{Name: CapEmojiGetByDescription, Domain: "data"},
	{Name: CapEmojiGetRandom, Domain: "data"},
	{Name: CapEmojiGetCount, Domain: "data"},
	{Name: CapEmojiGetEmotions, Domain: "data"},
	{Name: CapEmojiGetAll, Domain: "data"},
	{Name: CapEmojiGetInfo, Domain: "data"},
	{Name: CapEmojiRegister, Domain: "data"},
	{Name: CapEmojiDelete, Domain: "data"},
	{Name: CapFrequencyGetCurrentTalkValue, Domain: "data"},
	{Name: CapFrequencySetAdjust, Domain: "data"},
	{Name: CapFrequencyGetAdjust, Domain: "data"},
	{Name: CapToolGetDefinitions, Domain: "data"},
	{Name: CapAPICall, Domain: "components"},
	{Name: CapAPIGet, Domain: "components"},
	{Name: CapAPIList, Domain: "components"},
	{Name: CapAPIReplaceDynamic, Domain: "components"},
	{Name: CapRuntimeSetStatusBadges, Domain: "components"},
	{Name: CapComponentGetAllPlugins, Domain: "components"},
	{Name: CapComponentGetPluginInfo, Domain: "components"},
	{Name: CapComponentGetPluginConfigSchema, Domain: "components"},
	{Name: CapComponentUpdatePluginConfig, Domain: "components"},
	{Name: CapComponentListLoadedPlugins, Domain: "components"},
	{Name: CapComponentListRegisteredPlugins, Domain: "components"},
	{Name: CapComponentEnable, Domain: "components"},
	{Name: CapComponentDisable, Domain: "components"},
	{Name: CapComponentLoadPlugin, Domain: "components"},
	{Name: CapComponentUnloadPlugin, Domain: "components"},
	{Name: CapComponentReloadPlugin, Domain: "components"},
	{Name: CapKnowledgeSearch, Domain: "data"},
	{Name: CapMarketSearch, Domain: "market"},
	{Name: CapMarketGetDetail, Domain: "market"},
	{Name: CapMarketGetReadme, Domain: "market"},
	{Name: CapWebSearch, Domain: "web"},
	{Name: CapWebFetch, Domain: "web"},
	{Name: CapStatisticsLocalModels, Domain: "statistics"},
	{Name: CapStatisticsLocalModelTrend, Domain: "statistics"},
	{Name: CapStatisticsLocalTokenTrend, Domain: "statistics"},
	{Name: CapStatisticsLocalTokenDistribution, Domain: "statistics"},
	{Name: CapStatisticsLocalMessageTrend, Domain: "statistics"},
	{Name: CapStatisticsLocalToolTrend, Domain: "statistics"},
	{Name: CapStatisticsLocalOnlineTimeTrend, Domain: "statistics"},
	{Name: CapRenderHTML2PNG, Domain: "render"},
}

var capabilitySet = func() map[string]CapabilitySpec {
	out := make(map[string]CapabilitySpec, len(capabilitySpecs))
	for _, spec := range capabilitySpecs {
		out[spec.Name] = spec
	}
	return out
}()

// All 返回能力全表（注册顺序与 Python 注册表一致）
func All() []CapabilitySpec {
	out := make([]CapabilitySpec, len(capabilitySpecs))
	copy(out, capabilitySpecs)
	return out
}

// Names 返回能力名全表
func Names() []string {
	out := make([]string, 0, len(capabilitySpecs))
	for _, spec := range capabilitySpecs {
		out = append(out, spec.Name)
	}
	return out
}

// Lookup 查询能力静态描述
func Lookup(name string) (CapabilitySpec, bool) {
	spec, ok := capabilitySet[strings.TrimSpace(name)]
	return spec, ok
}

// IsKnown 判断能力名是否在 MaiBot 注册表内
func IsKnown(name string) bool {
	_, ok := Lookup(name)
	return ok
}

// IsAlwaysAllowed 对齐 _ALWAYS_ALLOWED_CAPABILITIES，永远放行
func IsAlwaysAllowed(name string) bool {
	return strings.TrimSpace(name) == CapAPIReplaceDynamic
}
