package messagereceive

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"MaiGo/internal/core/chatpause"
	"MaiGo/internal/core/localoperator"

	"go.uber.org/zap"
)

const (
	clearContextCommand = "/clear"
	clearMarkerKey      = "maisaka_context_cleared"
	clearInterceptLevel = 1
)

// IsCommandCandidate 判断消息是否可能进入命令链
func (b *ChatBot) IsCommandCandidate(message *SessionMessage) bool {
	if b == nil || message == nil {
		return false
	}
	text := strings.TrimSpace(message.PlainText())
	if text == "" {
		return false
	}
	if text == "/offline" || text == "/online" {
		return true
	}
	parts := strings.Fields(text)
	if len(parts) > 0 && (parts[0] == "/offline" || parts[0] == "/online") {
		return true
	}
	if len(parts) == 2 && parts[0] == "/list" && parts[1] == "chat" {
		return true
	}
	local := localoperator.IsLocalOperator(message.Platform, message.MessageInfo.AdditionalConfig)
	if local {
		if text == clearContextCommand || strings.HasPrefix(text, clearContextCommand+" ") {
			return true
		}
	} else if text == clearContextCommand {
		if b.commands != nil && b.commands.IsCoreCommandDisabled("clear") {
			return false
		}
		return true
	}
	if b.commands == nil {
		return false
	}
	match := b.commands.FindCommandByText(text)
	if match == nil {
		return false
	}
	commandID := match.PluginID + "." + match.Name
	if b.commands.IsCommandDisabled(commandID) {
		return false
	}
	if message.SessionID != "" && b.commands.IsCommandDisabledInSession(message.SessionID, match.Name) {
		return false
	}
	return true
}

// ProcessCommands 对齐 bot._process_commands，返回 handled、response、continueProcess
func (b *ChatBot) ProcessCommands(ctx context.Context, message *SessionMessage) (bool, string, bool) {
	if b == nil || message == nil {
		return false, "", true
	}
	// 对齐 Python：匹配文本用 processed_plain_text 原样（不 strip）；空串直接继续
	text := message.PlainText()
	if text == "" || b.commands == nil {
		return false, "", true
	}
	match := b.commands.FindCommandByText(text)
	if match == nil {
		return false, "", true
	}
	if message.SessionID != "" && b.commands.IsCommandDisabledInSession(message.SessionID, match.Name) {
		zap.L().Info("用户禁用的命令，跳过处理")
		return false, "", true
	}
	commandID := match.PluginID + "." + match.Name
	if b.commands.IsCommandDisabled(commandID) {
		zap.L().Info("命令 " + commandID + " 已在命令管理中停用，按普通消息处理")
		return false, "", true
	}
	if match.Permission == "operator" && b.policy != nil && !b.policy.HasCommandPermission(ctx, commandID, message) {
		b.markCommandMessage(message, 1)
		if !b.silentOnPermissionDenied(match) {
			b.sendCommandText(ctx, message, "你没有权限使用此命令。")
		}
		return true, "没有权限", false
	}
	message.IsCommand = true

	matchedGroups := match.MatchedGroups
	before := b.invokeCommandHook(ctx, "chat.command.before_execute", message, match, matchedGroups, nil)
	if before.Aborted {
		zap.L().Info("命令 " + match.Name + " 被 Hook 中止，跳过命令执行")
		return true, "", false
	}
	match = applyCommandHookMatch(match, before.Kwargs)
	matchedGroups = mapStringAny(before.Kwargs["matched_groups"], matchedGroups)

	var pluginConfig map[string]any
	if b.pluginConfigs != nil {
		config, err := b.pluginConfigs.PluginConfig(ctx, match.PluginID)
		if err != nil {
			// 对齐 Python：get_plugin_config 在 executor try 外抛错 → 外层按普通消息继续
			zap.L().Error("处理命令时出错", zap.String("plugin_id", match.PluginID), zap.Error(err))
			return false, "", true
		}
		pluginConfig = config
	}
	if b.executor == nil {
		return true, "", false
	}
	execution, err := b.executor.Execute(ctx, message, match, pluginConfig)
	if err != nil {
		zap.L().Error("执行命令时出错: " + match.Name + " - " + err.Error())
		execution = CommandExecution{Success: false, Response: err.Error(), Intercept: 1, Continue: false}
	}
	// Python 在命令执行后按拦截等级推导是否继续，不信任执行器自行给出的 continue
	execution.Continue = execution.Intercept == 0
	after := b.invokeCommandHook(ctx, "chat.command.after_execute", message, match, matchedGroups, &execution)
	applyCommandHookExecution(&execution, after.Kwargs)
	b.markCommandMessage(message, execution.Intercept)
	if execution.Success {
		zap.L().Info(fmt.Sprintf("命令执行成功: %s (拦截等级: %d)", match.Name, execution.Intercept))
	} else {
		zap.L().Warn("命令执行失败: " + match.Name + " - " + execution.Response)
	}
	return true, execution.Response, execution.Continue
}

func (b *ChatBot) silentOnPermissionDenied(match *CommandMatch) bool {
	if match != nil && match.SilentWhenDenied {
		return true
	}
	if b == nil || b.silentPermission == nil {
		return false
	}
	return b.silentPermission()
}

func (b *ChatBot) silentPermissionDenied() bool {
	if b == nil || b.silentPermission == nil {
		return false
	}
	return b.silentPermission()
}

// HandleCommandProcessingResult 对齐 _handle_command_processing_result，返回是否终止主链
func (b *ChatBot) HandleCommandProcessingResult(ctx context.Context, message *SessionMessage, cmdResult string, continueProcess bool) bool {
	if continueProcess {
		return false
	}
	if b.store != nil && message != nil {
		if err := b.store.StoreMessage(ctx, message); err != nil {
			zap.L().Warn("保存命令消息失败", zap.Error(err))
		}
	}
	// 对齐 Python f"{cmd_result}"：None 渲染为 None
	rendered := cmdResult
	if rendered == "" {
		rendered = "None"
	}
	zap.L().Info("命令处理完成，跳过后续消息处理: " + rendered)
	return true
}

// MarkClearContextCommand 标记清理上下文命令
func MarkClearContextCommand(message *SessionMessage) {
	if message == nil {
		return
	}
	message.IsCommand = true
	config := message.AdditionalConfigSafe()
	config[clearMarkerKey] = true
	config["intercept_message_level"] = clearInterceptLevel
}

// MarkCommandMessage 标记命令消息
func (b *ChatBot) markCommandMessage(message *SessionMessage, intercept int) {
	if message == nil {
		return
	}
	message.IsCommand = true
	config := message.AdditionalConfigSafe()
	config["intercept_message_level"] = intercept
}

// ProcessClearContextCommand 对齐 bot._process_clear_context_command
func (b *ChatBot) ProcessClearContextCommand(ctx context.Context, message *SessionMessage) bool {
	if b == nil || message == nil || b.manager == nil {
		return false
	}
	local := localoperator.IsLocalOperator(message.Platform, message.MessageInfo.AdditionalConfig)
	if !local && b.commands != nil && b.commands.IsCoreCommandDisabled("clear") {
		return false
	}
	text := strings.TrimSpace(message.PlainText())
	if local {
		if text != clearContextCommand && !strings.HasPrefix(text, clearContextCommand+" ") {
			return false
		}
	} else if text != clearContextCommand {
		return false
	}
	if b.policy != nil && !b.policy.HasCommandPermission(ctx, "core.clear", message) {
		b.markCommandMessage(message, clearInterceptLevel)
		b.storeCommandMessage(ctx, message)
		b.sendCommandText(ctx, message, "你没有权限使用此命令。")
		return true
	}
	if local {
		target := strings.TrimSpace(strings.TrimPrefix(text, clearContextCommand))
		if target == "" {
			b.markCommandMessage(message, clearInterceptLevel)
			b.storeCommandMessage(ctx, message)
			b.sendCommandText(ctx, message, "控制台中的 /clear 必须指定聊天名，请输入 /clear 后按 Tab 选择。")
			return true
		}
		option := b.findNamedSession(message.Platform, target)
		if option == nil {
			b.markCommandMessage(message, clearInterceptLevel)
			b.storeCommandMessage(ctx, message)
			b.sendCommandText(ctx, message, "未找到聊天“"+target+"”，请输入 /clear 后按 Tab 选择。")
			return true
		}
		cleared := message.Clone()
		cleared.SessionID = option.SessionID
		cleared.SetPlainText(clearContextCommand)
		MarkClearContextCommand(cleared)
		if err := b.storeMessage(ctx, cleared); err != nil {
			zap.L().Warn("保存清理上下文命令失败", zap.Error(err))
		}
		if b.clear != nil {
			if err := b.clear.ClearChatHistoryContext(ctx, option.SessionID); err != nil {
				zap.L().Warn("清理 Maisaka 历史上下文失败", zap.String("session_id", option.SessionID), zap.Error(err))
			}
		}
		// 确认文案发回控制台会话，对齐 Python text_to_stream(..., message.session_id)
		if err := b.sendText(ctx, message, "已清空“"+target+"”的 Maisaka 历史上下文。"); err != nil {
			zap.L().Warn("发送清理上下文回复失败", zap.Error(err))
		}
		return true
	}
	MarkClearContextCommand(message)
	b.storeCommandMessage(ctx, message)
	if b.clear != nil {
		if err := b.clear.ClearChatHistoryContext(ctx, message.SessionID); err != nil {
			zap.L().Warn("清理 Maisaka 历史上下文失败", zap.Error(err))
		}
	}
	if err := b.sendText(ctx, message, "我暂时失忆了"); err != nil {
		zap.L().Warn("发送清理上下文回复失败", zap.Error(err))
	}
	return true
}

// ProcessAdapterLifecycleCommand 对齐 bot._process_adapter_lifecycle_command
func (b *ChatBot) ProcessAdapterLifecycleCommand(ctx context.Context, message *SessionMessage) bool {
	if b == nil || message == nil {
		return false
	}
	text := strings.TrimSpace(message.PlainText())
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return false
	}
	isAdapterCommand := parts[0] == "/offline" || parts[0] == "/online"
	isListCommand := len(parts) == 2 && parts[0] == "/list" && parts[1] == "chat"
	if !isAdapterCommand && !isListCommand {
		return false
	}
	if b.policy != nil && !b.policy.HasPluginManagementPermission(ctx, message) {
		b.markCommandMessage(message, 1)
		b.storeCommandMessage(ctx, message)
		if !b.silentPermissionDenied() {
			b.sendCommandText(ctx, message, "你没有权限使用聊天与适配器管理命令。")
		}
		zap.L().Warn("已拒绝未授权的管理指令: platform=" + message.Platform + " user_id=" + message.UserID() + " command=" + text)
		return true
	}
	b.markCommandMessage(message, 1)
	b.storeCommandMessage(ctx, message)
	if isListCommand {
		b.handleChatListCommand(ctx, message)
		return true
	}
	if len(parts) > 1 {
		b.handleChatPauseCommand(ctx, message, parts)
		return true
	}
	if b.lifecycle == nil {
		return true
	}
	command := parts[0]
	// Python 以本地操作员身份区分远端离线提示，而不是适配器运行模式
	local := localoperator.IsLocalOperator(message.Platform, message.MessageInfo.AdditionalConfig)
	remoteOffline := !local && command == "/offline"
	if remoteOffline {
		if err := b.sendText(ctx, message, "正在关闭全部适配器插件；操作结果将记录在主程序日志中。恢复适配器请使用本地控制台 /online。"); err != nil {
			zap.L().Warn("适配器关闭前确认消息发送失败，已取消执行: session_id=" + message.SessionID)
			return true
		}
	}
	result, err := b.runAdapterLifecycle(ctx, text)
	if err != nil {
		zap.L().Warn("适配器生命周期操作失败", zap.String("command", text), zap.Error(err))
	}
	var response string
	if text == "/offline" {
		response = adapterOfflineResponse(result)
	} else {
		response = adapterOnlineResponse(result)
	}
	if !remoteOffline {
		if err := b.sendText(ctx, message, response); err != nil {
			zap.L().Warn("适配器运行状态已变更，但确认消息发送失败: session_id=" + message.SessionID)
		}
	}
	source := message.Platform
	if local {
		source = "local_console"
	}
	zap.L().Info("已执行适配器管理指令 " + text + ": source=" + source + " result=" + response)
	return true
}

func (b *ChatBot) runAdapterLifecycle(ctx context.Context, command string) (AdapterLifecycleResult, error) {
	if command == "/online" {
		return b.lifecycle.BringAdaptersOnline(ctx)
	}
	return b.lifecycle.TakeAdaptersOffline(ctx)
}

// handleChatListCommand 列出当前活跃及本次运行已暂停的群聊
func (b *ChatBot) handleChatListCommand(ctx context.Context, message *SessionMessage) {
	active := map[string]struct{}{}
	if b.chatControl != nil {
		for _, sessionID := range b.chatControl.ActiveSessionIDs() {
			active[sessionID] = struct{}{}
		}
	}
	sessions := make([]*BotChatSession, 0)
	if b.manager != nil {
		for _, session := range b.manager.AllSessions() {
			if session == nil || !session.IsGroup() {
				continue
			}
			sessionID := session.SessionID()
			if _, ok := active[sessionID]; ok || chatpause.Default().IsPaused(sessionID, true) {
				sessions = append(sessions, session)
			}
		}
	}
	sort.SliceStable(sessions, func(left, right int) bool {
		if sessions[left].Record.Platform != sessions[right].Record.Platform {
			return sessions[left].Record.Platform < sessions[right].Record.Platform
		}
		return sessions[left].SessionID() < sessions[right].SessionID()
	})
	lines := []string{fmt.Sprintf("活跃及本次运行已暂停的群聊共 %d 个：", len(sessions))}
	for _, session := range sessions {
		name := strings.TrimSpace(b.manager.GetSessionName(session))
		if name == "" {
			name = strings.TrimSpace(pointerText(session.Record.GroupID))
		}
		if name == "" {
			name = strings.TrimSpace(pointerText(session.Record.UserID))
		}
		status := "未暂停"
		if chatpause.Default().IsPaused(session.SessionID(), true) {
			status = "已暂停"
		}
		lines = append(lines, fmt.Sprintf("%s [%s / 群聊 / %s]\nID: %s", name, session.Record.Platform, status, session.SessionID()))
	}
	// 分批发送，避免聊天流较多时超出平台单条消息限制
	for start := 0; start < len(lines); start += 10 {
		end := start + 10
		if end > len(lines) {
			end = len(lines)
		}
		if err := b.sendText(ctx, message, strings.Join(lines[start:end], "\n\n")); err != nil {
			zap.L().Warn("聊天流列表发送失败: session_id=" + message.SessionID, zap.Error(err))
			break
		}
	}
}

// handleChatPauseCommand 暂停或恢复指定聊天类型/聊天流的回复
func (b *ChatBot) handleChatPauseCommand(ctx context.Context, message *SessionMessage, parts []string) {
	if len(parts) != 2 {
		b.sendCommandText(ctx, message, "用法：/offline group|private|<聊天流ID>；/online group|private|<聊天流ID>；/list chat")
		return
	}
	target := parts[1]
	paused := parts[0] == "/offline"
	var response string
	if target == "group" || target == "private" {
		chatpause.Default().SetType(target == "group", paused)
		name := "全部群聊"
		if target == "private" {
			name = "全部私聊"
		}
		if paused && b.chatControl != nil {
			_ = b.chatControl.StopPausedChats(ctx)
		}
		response = fmt.Sprintf("已%s %s 的聊天回复。管理命令仍可用；重启后暂停状态解除。", pausedWord(paused), name)
	} else {
		var session *BotChatSession
		if b.manager != nil {
			session, _ = b.manager.GetSessionBySessionID(target)
		}
		if session == nil {
			b.sendCommandText(ctx, message, "未找到该聊天流，请通过 /list chat 获取完整聊天流 ID（不是群号或用户号）。")
			return
		}
		chatpause.Default().SetSession(session.SessionID(), session.IsGroup(), paused)
		if paused && b.chatControl != nil {
			_ = b.chatControl.StopPausedChats(ctx)
		}
		name := strings.TrimSpace(b.manager.GetSessionName(session))
		if name == "" {
			name = session.SessionID()
		}
		response = fmt.Sprintf("已%s %s 的聊天回复。管理命令仍可用；重启后暂停状态解除。", pausedWord(paused), name)
	}
	if !paused {
		response += "实际回复仍遵循原有配置。"
	}
	b.sendCommandText(ctx, message, response)
}

func pausedWord(paused bool) string {
	if paused {
		return "暂停"
	}
	return "解除暂停"
}

func adapterOfflineResponse(result AdapterLifecycleResult) string {
	var builder strings.Builder
	switch {
	case len(result.Changed) > 0:
		builder.WriteString("已关闭适配器插件：")
		builder.WriteString(strings.Join(result.Changed, ", "))
		builder.WriteString("。")
	case len(result.Failed) > 0:
		builder.WriteString("适配器插件关闭失败。")
	case len(result.Pending) > 0:
		builder.WriteString("适配器插件已处于离线状态。")
	default:
		builder.WriteString("当前没有已加载的适配器插件。")
	}
	if len(result.Failed) > 0 {
		builder.WriteString(" 失败详情：")
		builder.WriteString(formatAdapterFailures(result.Failed))
	}
	return builder.String()
}

func adapterOnlineResponse(result AdapterLifecycleResult) string {
	var builder strings.Builder
	switch {
	case len(result.Changed) > 0:
		builder.WriteString("已恢复适配器插件：")
		builder.WriteString(strings.Join(result.Changed, ", "))
		builder.WriteString("。")
	case len(result.Failed) > 0:
		builder.WriteString("适配器插件恢复失败。")
	case len(result.Pending) > 0:
		builder.WriteString("当前没有等待恢复的适配器插件。")
	default:
		builder.WriteString("当前没有等待恢复的适配器插件。")
	}
	if len(result.Failed) > 0 {
		builder.WriteString(" 失败详情：")
		builder.WriteString(formatAdapterFailures(result.Failed))
	}
	return builder.String()
}

func formatAdapterFailures(failures map[string]string) string {
	names := make([]string, 0, len(failures))
	for name := range failures {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+": "+failures[name])
	}
	return strings.Join(parts, "；")
}

func (b *ChatBot) findNamedSession(platform, label string) *NamedSessionOption {
	options := b.manager.NamedSessionOptions(map[string]struct{}{platform: {}})
	for index := range options {
		if options[index].Label == label {
			return &options[index]
		}
	}
	return nil
}

func (b *ChatBot) invokeCommandHook(ctx context.Context, hookName string, message *SessionMessage, match *CommandMatch, matchedGroups map[string]any, execution *CommandExecution) HookResult {
	if b.hooks == nil {
		return HookResult{}
	}
	kwargs := map[string]any{
		"message":        message.ToHookMap(),
		"command_name":   match.Name,
		"plugin_id":      match.PluginID,
		"matched_groups": matchedGroups,
	}
	if execution != nil {
		kwargs["success"] = execution.Success
		kwargs["response"] = execution.Response
		kwargs["intercept_message_level"] = execution.Intercept
		kwargs["continue_process"] = execution.Continue
	}
	return b.hooks.Invoke(ctx, hookName, kwargs)
}

func applyCommandHookMatch(match *CommandMatch, kwargs map[string]any) *CommandMatch {
	if match == nil {
		return match
	}
	copy := *match
	if value, ok := kwargs["command_name"].(string); ok && strings.TrimSpace(value) != "" {
		copy.Name = strings.TrimSpace(value)
	}
	if value, ok := kwargs["plugin_id"].(string); ok && strings.TrimSpace(value) != "" {
		copy.PluginID = strings.TrimSpace(value)
	}
	copy.MatchedGroups = mapStringAny(kwargs["matched_groups"], copy.MatchedGroups)
	return &copy
}

func applyCommandHookExecution(execution *CommandExecution, kwargs map[string]any) {
	if execution == nil {
		return
	}
	if value, ok := kwargs["success"].(bool); ok {
		execution.Success = value
	}
	// 对齐 Python：raw_response=None → response=None（Go 空串）；显式出现 response 键才覆盖
	if raw, exists := kwargs["response"]; exists {
		if raw == nil {
			execution.Response = ""
		} else {
			execution.Response = anyString(raw)
		}
	}
	if value, ok := kwargs["intercept_message_level"].(int); ok {
		execution.Intercept = value
	} else if value, ok := kwargs["intercept_message_level"].(float64); ok {
		execution.Intercept = int(value)
	}
	if value, ok := kwargs["continue_process"].(bool); ok {
		execution.Continue = value
	}
}

func (b *ChatBot) storeCommandMessage(ctx context.Context, message *SessionMessage) {
	if err := b.storeMessage(ctx, message); err != nil {
		zap.L().Warn("保存命令消息失败", zap.Error(err))
	}
}

func (b *ChatBot) storeMessage(ctx context.Context, message *SessionMessage) error {
	if b.store == nil {
		return nil
	}
	return b.store.StoreMessage(ctx, message)
}

func (b *ChatBot) sendCommandText(ctx context.Context, message *SessionMessage, text string) {
	if err := b.sendText(ctx, message, text); err != nil {
		zap.L().Warn("发送命令回复失败", zap.Error(err))
	}
}

func (b *ChatBot) sendText(ctx context.Context, message *SessionMessage, text string) error {
	if b.textSender == nil {
		return fmt.Errorf("未配置文本发送端口")
	}
	return b.textSender.SendText(ctx, message, text)
}

func mapStringAny(value any, fallback map[string]any) map[string]any {
	if mapping, ok := value.(map[string]any); ok {
		return mapping
	}
	return fallback
}
