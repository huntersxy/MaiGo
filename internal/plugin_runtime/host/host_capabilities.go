package host

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"maibot-go/internal/chat/session"
	"maibot-go/internal/common/database"
	"maibot-go/internal/platform_io"
	maibotcfg "maibot-go/internal/config/maibot"

	capnames "maibot-go/internal/plugin_runtime/capabilities"
)

// BotConfigSource 提供只读 bot_config 切片（通常为 *maibotcfg.InstanceHolder）
type BotConfigSource interface {
	BotConfigSlice() map[string]any
}

// InstanceBotConfig 把实例 TOML 中的 bot_config 暴露为 map，供 config.get 点分路径读取
type InstanceBotConfig struct {
	Holder *maibotcfg.InstanceHolder
}

func (i InstanceBotConfig) BotConfigSlice() map[string]any {
	if i.Holder == nil {
		return nil
	}
	cfg := i.Holder.Config()
	if cfg == nil {
		return nil
	}
	m, err := valueAsStringKeyedMap(cfg.Bot)
	if err != nil {
		return nil
	}
	return m
}

// RegisterHostCapabilities 注册全部主程序能力，对齐 MaiBot capabilities/registry.py
//
// External 能力（files.*、desktop_pet.*）不在本函数注册：files.* 在 deps.Files 就绪时注册，
// desktop_pet.* 由 internal/desktoppet 自行注册
func RegisterHostCapabilities(host *Host, deps HostCapabilityDeps) ([]string, error) {
	if host == nil {
		return nil, fmt.Errorf("Host 未初始化")
	}
	host.SetUploadStore(deps.Uploads)
	host.SetPluginDataDirResolver(deps.PluginDataDir)
	type registration struct {
		name    string
		handler CapabilityHandler
	}
	reg := []registration{
		{capnames.CapConfigGet, makeConfigGetHandler(deps.Instance)},
		{capnames.CapConfigGetPlugin, makeConfigGetPluginHandler(host)},
		{capnames.CapConfigGetAll, makeConfigGetAllHandler(host)},
		{capnames.CapMessageGetByTime, capnames.MakeMessageGetByTimeHandler(deps.Memory)},
		{capnames.CapMessageGetByTimeInChat, capnames.MakeMessageGetByTimeInChatHandler(deps.Memory)},
		{capnames.CapMessageGetByID, capnames.MakeMessageGetByIDHandler(deps.Memory)},
		{capnames.CapMessageGetRecent, capnames.MakeMessageGetRecentHandler(deps.Memory)},
		{capnames.CapMessageCountNew, capnames.MakeMessageCountNewHandler(deps.Memory)},
		{capnames.CapMessageBuildReadable, capnames.MakeMessageBuildReadableHandler(deps.Memory, deps.Instance)},
		{capnames.CapPersonalityList, capnames.MakePersonalityListHandler(deps)},
		{capnames.CapPersonalityCurrent, capnames.MakePersonalityGetCurrentHandler(deps)},
		{capnames.CapPersonalitySwitch, capnames.MakePersonalitySwitchHandler(deps)},
		{capnames.CapInternalChatIdentity, capnames.MakeInternalChatIdentityHandler()},
		{capnames.CapInternalChatSend, capnames.MakeInternalChatSendHandler(deps.InternalChat)},
	}
	for _, capability := range []string{
		capnames.CapSendText, capnames.CapSendEmoji, capnames.CapSendImage,
		capnames.CapSendForward, capnames.CapSendHybrid, capnames.CapSendCommand, capnames.CapSendCustom,
	} {
		reg = append(reg, registration{capability, capnames.MakeSendCapabilityHandler(deps.Sender, capability)})
	}
	for _, capability := range []string{
		capnames.CapLLMGenerate, capnames.CapLLMGenerateWithTools, capnames.CapLLMEmbed,
		capnames.CapLLMTranscribeAudio, capnames.CapLLMGetAvailableModels,
	} {
		reg = append(reg, registration{capability, capnames.MakeLLMCapabilityHandler(deps.LLM, capability)})
	}
	for _, item := range []registration{
		{capnames.CapDatabaseQuery, capnames.MakeDatabaseQueryHandler(deps.Database)},
		{capnames.CapDatabaseSave, capnames.MakeDatabaseSaveHandler(deps.Database)},
		{capnames.CapDatabaseGet, capnames.MakeDatabaseGetHandler(deps.Database)},
		{capnames.CapDatabaseDelete, capnames.MakeDatabaseDeleteHandler(deps.Database)},
		{capnames.CapDatabaseCount, capnames.MakeDatabaseCountHandler(deps.Database)},
	} {
		reg = append(reg, item)
	}
	for _, item := range []registration{
		{capnames.CapChatGetAllStreams, capnames.MakeChatListStreamsHandler(deps.Sessions, capnames.CapChatGetAllStreams)},
		{capnames.CapChatGetGroupStreams, capnames.MakeChatListStreamsHandler(deps.Sessions, capnames.CapChatGetGroupStreams)},
		{capnames.CapChatGetPrivateStreams, capnames.MakeChatListStreamsHandler(deps.Sessions, capnames.CapChatGetPrivateStreams)},
		{capnames.CapChatOpenSession, capnames.MakeChatOpenSessionHandler(deps.Sessions)},
		{capnames.CapChatGetStreamByGroupID, capnames.MakeChatStreamLookupHandler(deps.Sessions, capnames.CapChatGetStreamByGroupID)},
		{capnames.CapChatGetStreamByUserID, capnames.MakeChatStreamLookupHandler(deps.Sessions, capnames.CapChatGetStreamByUserID)},
		{capnames.CapChatGetAvatar, capnames.MakeChatAvatarHandler(deps.Sessions)},
	} {
		reg = append(reg, item)
	}
	for _, item := range []registration{
		{capnames.CapPersonGetID, capnames.MakePersonGetIDHandler(deps.Persons)},
		{capnames.CapPersonGetValue, capnames.MakePersonGetValueHandler(deps.Persons)},
		{capnames.CapPersonGetIDByName, capnames.MakePersonGetIDByNameHandler(deps.Persons)},
	} {
		reg = append(reg, item)
	}
	for _, item := range []registration{
		{capnames.CapEmojiGetByDescription, capnames.MakeEmojiGetByDescriptionHandler(deps)},
		{capnames.CapEmojiGetRandom, capnames.MakeEmojiGetRandomHandler(deps)},
		{capnames.CapEmojiGetCount, capnames.MakeEmojiGetCountHandler(deps)},
		{capnames.CapEmojiGetEmotions, capnames.MakeEmojiGetEmotionsHandler(deps)},
		{capnames.CapEmojiGetAll, capnames.MakeEmojiGetAllHandler(deps)},
		{capnames.CapEmojiGetInfo, capnames.MakeEmojiGetInfoHandler(deps)},
		{capnames.CapEmojiRegister, capnames.MakeEmojiRegisterHandler(deps)},
		{capnames.CapEmojiDelete, capnames.MakeEmojiDeleteHandler(deps)},
	} {
		reg = append(reg, item)
	}
	for _, item := range []registration{
		{capnames.CapFrequencyGetCurrentTalkValue, makeFrequencyHandler(deps.Frequency, capnames.CapFrequencyGetCurrentTalkValue)},
		{capnames.CapFrequencySetAdjust, makeFrequencyHandler(deps.Frequency, capnames.CapFrequencySetAdjust)},
		{capnames.CapFrequencyGetAdjust, makeFrequencyHandler(deps.Frequency, capnames.CapFrequencyGetAdjust)},
	} {
		reg = append(reg, item)
	}
	reg = append(reg,
		registration{capnames.CapToolGetDefinitions, makeToolDefinitionsHandler(host)},
		registration{capnames.CapComponentGetAllPlugins, makeComponentGetAllPluginsHandler(host)},
		registration{capnames.CapComponentGetPluginInfo, makeComponentGetPluginInfoHandler(host)},
		registration{capnames.CapComponentGetPluginConfigSchema, makeComponentGetPluginConfigSchemaHandler(host)},
		registration{capnames.CapComponentUpdatePluginConfig, makeComponentUpdatePluginConfigHandler(host, deps)},
		registration{capnames.CapComponentListLoadedPlugins, makeComponentListPluginsHandler(host)},
		registration{capnames.CapComponentListRegisteredPlugins, makeComponentListPluginsHandler(host)},
		registration{capnames.CapComponentEnable, makeComponentToggleHandler(host, true)},
		registration{capnames.CapComponentDisable, makeComponentToggleHandler(host, false)},
		registration{capnames.CapComponentLoadPlugin, makeComponentLoadPluginHandler(host, deps)},
		registration{capnames.CapComponentUnloadPlugin, makeComponentUnloadPluginHandler()},
		registration{capnames.CapComponentReloadPlugin, makeComponentReloadPluginHandler(host, deps)},
		registration{capnames.CapAPICall, makeAPICallHandler(host)},
		registration{capnames.CapAPIGet, makeAPIGetHandler(host)},
		registration{capnames.CapAPIList, makeAPIListHandler(host)},
		registration{capnames.CapAPIReplaceDynamic, makeAPIReplaceDynamicHandler(host)},
	)
	reg = append(reg, registration{capnames.CapKnowledgeSearch, makeKnowledgeSearchHandler(deps.Knowledge)})
	for _, capability := range []string{capnames.CapMarketSearch, capnames.CapMarketGetDetail, capnames.CapMarketGetReadme} {
		reg = append(reg, registration{capability, makeMarketHandler(deps.Market, capability)})
	}
	reg = append(reg,
		registration{capnames.CapWebSearch, makeWebHandler(deps.Web, capnames.CapWebSearch)},
		registration{capnames.CapWebFetch, makeWebHandler(deps.Web, capnames.CapWebFetch)},
	)
	for _, capability := range []string{
		capnames.CapStatisticsLocalModels, capnames.CapStatisticsLocalModelTrend,
		capnames.CapStatisticsLocalTokenTrend, capnames.CapStatisticsLocalTokenDistribution,
		capnames.CapStatisticsLocalMessageTrend, capnames.CapStatisticsLocalToolTrend,
		capnames.CapStatisticsLocalOnlineTimeTrend,
	} {
		reg = append(reg, registration{capability, makeStatisticsHandler(deps.Statistics)})
	}
	reg = append(reg, registration{capnames.CapRenderHTML2PNG, makeRenderHandler(deps.Render)})
	for _, capability := range []string{
		capnames.CapMaisakaContextAppend, capnames.CapMaisakaContextResolveImage,
		capnames.CapMaisakaProactiveTrigger, capnames.CapMaisakaReplyGenerate,
	} {
		reg = append(reg, registration{capability, makeMaisakaHandler(host, deps.Maisaka)})
	}
	if deps.Uploads != nil && deps.PluginDataDir != nil {
		reg = append(reg, registration{capnames.CapWebUIClaimUpload, makeWebUIClaimUploadHandler(deps)})
	}
	if deps.Files != nil {
		fileHandler := capnames.MakeFilesCapabilityHandler(deps.Files)
		for _, capability := range []string{
			capnames.CapFilesList, capnames.CapFilesReadText, capnames.CapFilesDownload,
			capnames.CapFilesUpload, capnames.CapFilesSend,
		} {
			reg = append(reg, registration{capability, fileHandler})
		}
	}
	names := make([]string, 0, len(reg))
	for _, item := range reg {
		if err := host.RegisterCapability(item.name, item.handler); err != nil {
			return names, err
		}
		names = append(names, item.name)
	}
	return names, nil
}

func makeConfigGetHandler(instance BotConfigSource) CapabilityHandler {
	return func(_ context.Context, _, _ string, args map[string]any) (any, error) {
		key, _ := args["key"].(string)
		defaultValue := args["default"]
		value, err := lookupConfigValue(instance, key, defaultValue)
		if err != nil {
			return map[string]any{"success": false, "value": nil, "error": err.Error()}, nil
		}
		return map[string]any{"success": true, "value": value}, nil
	}
}

func resolvePluginChatSessionID(ctx context.Context, mem *memory.Manager, chatID string) (string, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return "", fmt.Errorf("chat_id 不能为空")
	}
	mgr := chatsession.NewManager(mem.GetDB())
	if rec, err := mgr.GetExistingBySessionID(ctx, chatID); err == nil && rec != nil {
		return rec.SessionID, nil
	}
	if rec, err := mgr.ResolveSessionByConversationKey(ctx, platformio.PlatformQQ, chatID); err == nil && rec != nil {
		return rec.SessionID, nil
	}
	if targetID, kind, ok := parseConversationKey(chatID); ok {
		if rec, err := mgr.ResolveSession(ctx, platformio.PlatformQQ, strconv.FormatInt(targetID, 10), kind); err == nil && rec != nil {
			return rec.SessionID, nil
		}
	}
	return chatID, nil
}

func parseConversationKey(raw string) (targetID int64, kind string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, "", false
	}
	if strings.HasPrefix(raw, "group:") {
		id, err := strconv.ParseInt(strings.TrimPrefix(raw, "group:"), 10, 64)
		return id, memory.ConversationKindGroup, err == nil && id > 0
	}
	if strings.HasPrefix(raw, "private:") {
		id, err := strconv.ParseInt(strings.TrimPrefix(raw, "private:"), 10, 64)
		return id, memory.ConversationKindPrivate, err == nil && id > 0
	}
	return 0, "", false
}

func serializePluginMessages(rows []memory.MessageLog) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		additional := map[string]any{}
		for key, value := range row.AdditionalConfig {
			additional[key] = value
		}
		item := map[string]any{
			"message_id":           strconv.FormatInt(row.MessageID, 10),
			"chat_id":              row.SessionID,
			"session_id":           row.SessionID,
			"platform":             row.Platform,
			"account_id":           row.AccountID,
			"scope":                row.Scope,
			"chat_type":            row.ConversationKind,
			"target_id":            row.TargetID,
			"user_id":              row.UserID,
			"user_nickname":        row.UserNickname,
			"user_cardname":        row.UserCardname,
			"group_id":             row.GroupID,
			"group_name":           row.GroupName,
			"processed_plain_text": row.ProcessedPlainText,
			"is_mentioned":         row.IsMentioned,
			"is_at":                row.IsAt,
			"is_emoji":             row.IsEmoji,
			"is_picture":           row.IsPicture,
			"is_command":           row.IsCommand,
			"is_notify":            row.IsNotify,
			"additional_config":    additional,
			"timestamp":            row.Timestamp.Unix(),
			"message_time":         row.Timestamp.UTC().Format(time.RFC3339Nano),
		}
		out = append(out, item)
	}
	return out
}

func coalesceString(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := args[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func coalesceFloat(args map[string]any, key string, def float64) float64 {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err == nil {
			return f
		}
	}
	return def
}

func uintFromAny(v any) (uint, error) {
	switch n := v.(type) {
	case uint:
		return n, nil
	case int:
		if n < 0 {
			return 0, fmt.Errorf("negative")
		}
		return uint(n), nil
	case int64:
		if n < 0 {
			return 0, fmt.Errorf("negative")
		}
		return uint(n), nil
	case float64:
		if n < 0 {
			return 0, fmt.Errorf("negative")
		}
		return uint(n), nil
	case string:
		u, err := strconv.ParseUint(strings.TrimSpace(n), 10, 64)
		return uint(u), err
	default:
		return 0, fmt.Errorf("unsupported")
	}
}
