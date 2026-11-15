package runtime

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	messagereceive "maibot-go/internal/chat/message_receive"
	"maibot-go/internal/common/data_models/messagecomponent"
	"maibot-go/internal/common/database"
	"maibot-go/internal/common/media"
	maibotcfg "maibot-go/internal/config/maibot"
	"maibot-go/internal/core/announcement"
	"maibot-go/internal/core/internal_chat"
	"maibot-go/internal/core/localoperator"
	"maibot-go/internal/platform_io/drivers/onebot"
	"maibot-go/internal/plugin_runtime"

	"go.uber.org/zap"
)

// handleCommand 尝试把消息交给插件命令链；返回 true 表示消息已被命令链消费
func (a *Agent) handleCommand(ctx context.Context, msg *onebot.ConversationMessage, mediaRefs []media.Ref, forwards []messagereceive.ForwardDraft) bool {
	if a == nil || a.pluginHost == nil || msg == nil {
		return false
	}
	text := strings.TrimSpace(msg.FinalContent)
	if text == "" {
		text = strings.TrimSpace(msg.Content)
	}
	if text == "" {
		return false
	}
	components := a.pluginHost.Components()
	if components == nil || !components.HasCommands() {
		return false
	}
	command, matchedGroups := components.FindCommandByText(text, "")
	sessionID := ""
	if command == nil {
		if !components.HasSessionScopedCommands() {
			return false
		}
		sessionID = a.ensureChatSession(ctx, msg)
		command, matchedGroups = components.FindCommandByText(text, sessionID)
		if command == nil {
			return false
		}
	}
	if sessionID == "" {
		sessionID = a.resolveMaiBotSessionID(ctx, msg.ConversationKind, msg.TargetID)
		if sessionID == "" {
			sessionID = a.ensureChatSession(ctx, msg)
		}
	}
	commandName := command.Name
	pluginID := command.PluginID
	if commandName == "" || pluginID == "" {
		return false
	}
	if sessionID != "" && slices.Contains(announcement.Default.GetDisabledChatCommands(sessionID), commandName) {
		zap.L().Info("用户禁用的命令，跳过处理", zap.String("command", commandName))
		return false
	}
	config := a.pluginCommandConfig()
	if slices.Contains(config.DisabledCommands, pluginID+"."+commandName) {
		zap.L().Info("命令已在命令管理中停用，按普通消息处理", zap.String("command", pluginID+"."+commandName))
		return false
	}
	platform := messagePlatform(msg)
	userID := strconv.FormatInt(msg.UserID, 10)
	localOperator := localoperator.IsLocalOperator(platform, msg.AdditionalConfig)
	relayed := localoperator.IsPluginRelayed(msg.AdditionalConfig)
	if command.Permission == "operator" && !localoperator.HasCommandPermission(
		pluginID+"."+commandName,
		platform,
		userID,
		sessionID,
		config.Permission,
		commandPermissionRules(config.CommandPermissions),
		localOperator,
		relayed,
	) {
		a.markCommandMessage(msg, 1)
		if !config.SilentPermissionDenied {
			if _, _, err := a.sendConversationMessage(ctx, msg.ConversationKind, msg.TargetID, "你没有权限使用此命令。", 0, nil); err != nil {
				zap.L().Warn("发送命令无权限提示失败", zap.String("command", commandName), zap.Error(err))
			}
		}
		a.persistCommandMessage(ctx, msg, mediaRefs, forwards, sessionID)
		zap.L().Info("命令权限不足，已拦截", zap.String("command", commandName), zap.String("user_id", userID))
		return true
	}

	groupID := ""
	groupName := ""
	if msg.ConversationKind == memory.ConversationKindGroup {
		groupID = msg.GroupKey
		if groupID == "" {
			groupID = strconv.FormatInt(msg.TargetID, 10)
		}
		groupName = msg.GroupName
	}
	accountID := ""
	if selfID := a.SelfID(); selfID > 0 {
		accountID = strconv.FormatInt(selfID, 10)
	}
	message := commandMessageDict(msg, text, sessionID, accountID, groupID, groupName, mediaRefs, forwards)
	hookCommandName, hookPluginID := commandName, pluginID
	hookGroups := copyStringMap(matchedGroups)
	before := a.pluginHost.EmitHook(ctx, "chat.command.before_execute", map[string]any{
		"message":        message,
		"command_name":   commandName,
		"plugin_id":      pluginID,
		"matched_groups": matchedGroupPayload(matchedGroups),
	})
	logHookErrors("chat.command.before_execute", before.Errors)
	if value, ok := hookText(before.Kwargs["command_name"]); ok && value != "" {
		hookCommandName = value
	}
	if value, ok := hookText(before.Kwargs["plugin_id"]); ok && value != "" {
		hookPluginID = value
	}
	if groups, ok := hookStringMap(before.Kwargs["matched_groups"]); ok {
		hookGroups = groups
	}
	message = mergeCommandMessage(message, before.Kwargs["message"])
	text = commandMessageText(message, text)
	sessionID = commandMessageField(message, "session_id", sessionID)
	accountID = commandMessageField(message, "account_id", accountID)
	platform = commandMessageField(message, "platform", platform)
	userID = commandMessageInfoField(message, "user_info", "user_id", userID)
	groupID = commandMessageInfoField(message, "group_info", "group_id", groupID)
	localOperator = localoperator.IsLocalOperator(platform, commandAdditionalConfig(message))
	if before.Aborted {
		msg.FinalContent = commandMessageText(message, msg.FinalContent)
		zap.L().Info("命令被 Hook 中止", zap.String("command", hookCommandName), zap.String("stopped_by", before.StoppedBy))
		a.persistCommandMessage(ctx, msg, mediaRefs, forwards, sessionID)
		return true
	}

	entry := *command
	entry.Name = hookCommandName
	entry.PluginID = hookPluginID
	entry.FullName = hookPluginID + "." + hookCommandName
	pluginConfig := a.pluginConfigForCommand(ctx, hookPluginID)
	execResult := a.pluginHost.ExecuteCommand(ctx, &entry, pluginruntime.CommandExecutionContext{
		Text:            text,
		SessionID:       sessionID,
		GroupID:         groupID,
		Platform:        platform,
		UserID:          userID,
		IsLocalOperator: localOperator,
		MatchedGroups:   hookGroups,
		Message:         message,
		PluginConfig:    pluginConfig,
	})
	intercept := execResult.InterceptMessageLevel
	continueProcess := intercept == 0
	success := execResult.Success
	response := execResult.Response

	after := a.pluginHost.EmitHook(ctx, "chat.command.after_execute", map[string]any{
		"message":                 message,
		"command_name":            hookCommandName,
		"plugin_id":               hookPluginID,
		"matched_groups":          matchedGroupPayload(hookGroups),
		"success":                 success,
		"response":                response,
		"intercept_message_level": intercept,
		"continue_process":        continueProcess,
	})
	logHookErrors("chat.command.after_execute", after.Errors)
	if value, ok := hookBool(after.Kwargs["success"]); ok {
		success = value
	}
	if raw, exists := after.Kwargs["response"]; exists {
		response = ""
		if raw != nil {
			response = fmt.Sprint(raw)
		}
	}
	if value, ok := hookInt(after.Kwargs["intercept_message_level"]); ok {
		intercept = value
	}
	if value, ok := hookBool(after.Kwargs["continue_process"]); ok {
		continueProcess = value
	}
	message = mergeCommandMessage(message, after.Kwargs["message"])
	if continueProcess {
		msg.FinalContent = commandMessageText(message, msg.FinalContent)
	}
	a.markCommandMessage(msg, intercept)
	if success {
		zap.L().Info("命令执行成功", zap.String("command", hookCommandName), zap.Int("intercept_message_level", intercept))
	} else {
		zap.L().Warn("命令执行失败", zap.String("command", hookCommandName), zap.String("response", response))
	}
	if continueProcess {
		return false
	}
	a.persistCommandMessage(ctx, msg, mediaRefs, forwards, sessionID)
	zap.L().Info("命令处理完成，跳过后续消息处理", zap.String("command", hookCommandName), zap.String("response", response))
	return true
}

func (a *Agent) pluginCommandConfig() maibotcfg.PluginConfig {
	if a == nil || a.instanceHolder == nil {
		return maibotcfg.PluginConfig{}
	}
	a.instanceMu.RLock()
	holder := a.instanceHolder
	a.instanceMu.RUnlock()
	if holder == nil {
		return maibotcfg.PluginConfig{}
	}
	cfg := holder.Config()
	if cfg == nil {
		return maibotcfg.PluginConfig{}
	}
	return cfg.Bot.Plugin
}

func (a *Agent) pluginConfigForCommand(ctx context.Context, pluginID string) map[string]any {
	if a == nil || a.pluginHost == nil || strings.TrimSpace(pluginID) == "" {
		return nil
	}
	result, err := a.pluginHost.InspectPluginConfig(ctx, pluginID, nil, false)
	if err != nil || !result.Success || result.NormalizedConfig == nil {
		return nil
	}
	return result.NormalizedConfig
}

func (a *Agent) markCommandMessage(msg *onebot.ConversationMessage, intercept int) {
	if msg == nil {
		return
	}
	if msg.AdditionalConfig == nil {
		msg.AdditionalConfig = map[string]any{}
	}
	msg.AdditionalConfig["is_command"] = true
	msg.AdditionalConfig["intercept_message_level"] = intercept
}

func (a *Agent) persistCommandMessage(ctx context.Context, msg *onebot.ConversationMessage, mediaRefs []media.Ref, forwards []messagereceive.ForwardDraft, sessionID string) {
	if msg == nil {
		return
	}
	if msg.AdditionalConfig == nil {
		msg.AdditionalConfig = map[string]any{}
	}
	msg.AdditionalConfig["is_command"] = true
	if sessionID == "" {
		sessionID = a.ensureChatSession(ctx, msg)
	}
	if _, _, err := a.persistConversationMessage(ctx, msg, mediaRefs, forwards, messagePlatform(msg), sessionID); err != nil {
		zap.L().Warn("命令消息落库失败", zap.String("conversation_kind", msg.ConversationKind), zap.Int64("target_id", msg.TargetID), zap.Error(err))
	}
}

func commandMessageDict(msg *onebot.ConversationMessage, text, sessionID, accountID, groupID, groupName string, mediaRefs []media.Ref, forwards []messagereceive.ForwardDraft) map[string]any {
	if msg == nil {
		return map[string]any{}
	}
	additional := make(map[string]any, len(msg.AdditionalConfig)+1)
	for key, value := range msg.AdditionalConfig {
		additional[key] = value
	}
	additional["is_command"] = true
	groupInfo := any(nil)
	if groupID != "" {
		groupInfo = map[string]any{
			"group_id":   groupID,
			"group_name": groupName,
		}
	}
	rawMessage := []map[string]any{}
	if raw := messagereceive.BuildRawContent(msg, mediaRefs, forwards); len(raw) > 0 {
		if sequence, err := messagecomponent.DecodeDBRecord(raw); err == nil {
			rawMessage = internalchat.SerializeSequence(sequence)
		}
	}
	timestamp := "0"
	if !msg.Time.IsZero() {
		timestamp = strconv.FormatFloat(float64(msg.Time.UnixNano())/1e9, 'f', -1, 64)
	}
	out := map[string]any{
		"message_id": msg.MessageID,
		"timestamp":  timestamp,
		"platform":   messagePlatform(msg),
		"account_id": accountID,
		"scope":      msg.ConversationKind,
		"message_info": map[string]any{
			"user_info": map[string]any{
				"user_id":       strconv.FormatInt(msg.UserID, 10),
				"user_nickname": msg.Nickname,
				"user_cardname": nullableString(msg.GroupCard),
			},
			"group_info":        groupInfo,
			"additional_config": additional,
		},
		"raw_message":          rawMessage,
		"is_mentioned":         msg.IsMentioned,
		"is_at":                len(msg.AtList) > 0,
		"is_emoji":             messageHasSticker(msg),
		"is_picture":           len(msg.Images) > 0,
		"is_command":           true,
		"is_notify":            msg.IsNotify,
		"session_id":           sessionID,
		"processed_plain_text": text,
	}
	if !msg.ReceivedAt.IsZero() {
		out["received_at"] = msg.ReceivedAt.UTC().Format(time.RFC3339Nano)
	}
	if msg.Reply != nil && msg.Reply.MessageID != 0 {
		out["reply_to"] = strconv.FormatInt(msg.Reply.MessageID, 10)
	}
	return out
}

func messageHasSticker(msg *onebot.ConversationMessage) bool {
	for _, image := range msg.Images {
		if image.SubType == 1 {
			return true
		}
	}
	return false
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func matchedGroupPayload(groups map[string]string) map[string]any {
	out := make(map[string]any, len(groups))
	for key, value := range groups {
		out[key] = value
	}
	return out
}

func copyStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func logHookErrors(name string, errors []string) {
	for _, message := range errors {
		zap.L().Warn("命令 Hook 错误", zap.String("hook", name), zap.String("error", message))
	}
}

func hookText(raw any) (string, bool) {
	switch value := raw.(type) {
	case nil:
		return "", false
	case string:
		return strings.TrimSpace(value), true
	default:
		return strings.TrimSpace(fmt.Sprint(value)), true
	}
}

func hookStringMap(raw any) (map[string]string, bool) {
	switch value := raw.(type) {
	case map[string]string:
		return copyStringMap(value), true
	case map[string]any:
		out := make(map[string]string, len(value))
		for key, item := range value {
			if item == nil {
				out[key] = ""
				continue
			}
			out[key] = fmt.Sprint(item)
		}
		return out, true
	default:
		return nil, false
	}
}

func hookBool(raw any) (bool, bool) {
	value, ok := raw.(bool)
	return value, ok
}

func hookInt(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	case bool:
		if value {
			return 1, true
		}
		return 0, true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func mergeCommandMessage(message map[string]any, raw any) map[string]any {
	patch, ok := raw.(map[string]any)
	if !ok || patch == nil {
		return message
	}
	out := make(map[string]any, len(message)+len(patch))
	for key, value := range message {
		out[key] = value
	}
	for key, value := range patch {
		out[key] = value
	}
	return out
}

func commandMessageText(message map[string]any, fallback string) string {
	value := commandMessageField(message, "processed_plain_text", fallback)
	return strings.TrimSpace(value)
}

func commandMessageField(message map[string]any, key, fallback string) string {
	value, ok := message[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func commandMessageInfoField(message map[string]any, table, key, fallback string) string {
	info, _ := message["message_info"].(map[string]any)
	rows, _ := info[table].(map[string]any)
	value, ok := rows[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func commandAdditionalConfig(message map[string]any) map[string]any {
	info, _ := message["message_info"].(map[string]any)
	config, _ := info["additional_config"].(map[string]any)
	return config
}

func commandPermissionRules(raw any) map[string]localoperator.CommandPermissionRule {
	rules := map[string]localoperator.CommandPermissionRule{}
	switch table := raw.(type) {
	case map[string]maibotcfg.CommandPermissionConfig:
		for key, rule := range table {
			rules[key] = localoperator.CommandPermissionRule{AllowUsers: rule.AllowUsers, AllowChats: rule.AllowChats}
		}
	case map[string]any:
		for key, value := range table {
			rule := localoperator.CommandPermissionRule{}
			switch typed := value.(type) {
			case maibotcfg.CommandPermissionConfig:
				rule.AllowUsers = typed.AllowUsers
				rule.AllowChats = typed.AllowChats
			case map[string]any:
				rule.AllowUsers = stringListValue(typed["allow_users"])
				rule.AllowChats = stringListValue(typed["allow_chats"])
			}
			rules[key] = rule
		}
	}
	return rules
}

func stringListValue(raw any) []string {
	switch value := raw.(type) {
	case []string:
		return append([]string(nil), value...)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if item == nil {
				continue
			}
			out = append(out, fmt.Sprint(item))
		}
		return out
	case string:
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return []string{value}
	default:
		return nil
	}
}
