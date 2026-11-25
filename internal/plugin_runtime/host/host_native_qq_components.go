package host

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/qq_official"
	qqofficialassets "MaiGo/plugins/built_in/qq_official_adapter"
)

func (h *Host) registerQQOfficialComponents(adapter *qq_official.Adapter) {
	if h.components != nil {
		for _, command := range []struct{ kind, name, description string }{
			{"group", "assign_group_id", "绑定当前 QQ 官方群的真实群号"},
			{"user", "assign_id", "绑定 QQ 官方用户的真实 QQ 号，可 @ 指定用户"},
		} {
			pattern := `^/` + command.name + `(?:\s|$)`
			compiled, err := compileCommandPattern(pattern)
			if err != nil {
				zap.L().Error("QQ 官方绑定命令注册失败", zap.Error(err))
				continue
			}
			h.components.RegisterLocalComponent(&ComponentEntry{
				Name: command.name, FullName: qq_official.PluginID + "." + command.name,
				PluginID: qq_official.PluginID, ComponentType: ComponentTypeCommand,
				Enabled: true, ChatScope: ComponentChatScopeAll, Description: command.description,
				CommandPattern: pattern, Permission: "operator", InterceptMessageLevel: 1, pattern: compiled,
				localHandler: func(ctx context.Context, execution CommandExecutionContext) CommandExecutionResult {
					success, text, err := adapter.AssignIdentity(ctx, command.kind, execution.Message)
					if err != nil {
						text = err.Error()
					}
					return CommandExecutionResult{Success: success, Response: text, InterceptMessageLevel: 1}
				},
			})
		}
	}
	if h.apis != nil {
		for _, api := range []struct{ action, description string }{
			{"status", "查询 QQ 扫码授权及连接状态"}, {"start", "发起 QQ 官方机器人扫码登录"}, {"cancel", "取消当前 QQ 扫码登录"},
		} {
			h.apis.SetAPILocalHandler("webui_login_"+api.action, qq_official.PluginID, map[string]any{
				"version": "1", "public": true, "enabled": true, "description": api.description,
			}, func(ctx context.Context, _, _ string, _ map[string]any) (any, error) {
				return h.qqOfficialLoginStatus(ctx, adapter, api.action)
			})
		}
	}
	if err := h.loadQQOfficialWebUI(); err != nil {
		zap.L().Warn("QQ 官方映射页面加载失败", zap.Error(err))
	}
	h.registerQQOfficialModeration(adapter)
}

func (h *Host) registerQQOfficialModeration(adapter *qq_official.Adapter) {
	settings := adapter.Settings()
	type tool struct {
		name, description string
		enabled           bool
		properties        map[string]any
		required          []any
	}
	target := map[string]any{"type": "string", "description": "目标消息的 ID"}
	reason := map[string]any{"type": "string", "description": "操作原因"}
	if h.components != nil {
		for _, definition := range []tool{
			{qq_official.MuteToolName, "设置 QQ 群成员的禁言状态，0 表示解除禁言。群和成员从消息解析。", settings.Mute.Enabled,
				map[string]any{"msg_id": target, "duration": map[string]any{"type": "integer", "description": "禁言秒数，0 表示解除禁言"}, "reason": reason}, []any{"msg_id", "duration"}},
			{qq_official.KickToolName, "将目标消息的发送者移出 QQ 群。需要机器人具备相应的群管理权限。", settings.Kick.Enabled,
				map[string]any{"msg_id": target, "reason": reason}, []any{"msg_id"}},
			{qq_official.RecallToolName, "撤回一条 QQ 消息。需要机器人具备相应的撤回权限。", settings.Recall.Enabled,
				map[string]any{"msg_id": target}, []any{"msg_id"}},
		} {
			h.components.RegisterLocalComponent(&ComponentEntry{
				Name: definition.name, FullName: qq_official.PluginID + "." + definition.name, PluginID: qq_official.PluginID,
				ComponentType: ComponentTypeTool, ChatScope: ComponentChatScopeAll, Enabled: definition.enabled,
				Platforms: []string{qq_official.PlatformQQ}, InvokeMethod: "plugin.invoke_tool", Description: definition.description,
				ParametersRaw: map[string]any{"type": "object", "properties": definition.properties, "required": definition.required},
				localTool: func(ctx context.Context, args map[string]any, exec ToolExecutionContext) (any, error) {
					id := coerceToolStringArg(args["msg_id"])
					if id == "" {
						return nil, fmt.Errorf("缺少目标消息 ID")
					}
					session := firstNonEmptyText(exec.SessionID, exec.StreamID)
					if exec.Platform != qq_official.PlatformQQ || session == "" {
						return nil, fmt.Errorf("QQ 官方群管理仅支持当前 QQ 会话")
					}
					message, err := h.qqOfficialModerationMessage(ctx, adapter, session, id)
					if err != nil {
						return nil, err
					}
					duration := 0
					if definition.name == qq_official.MuteToolName {
						duration, err = strconv.Atoi(strings.TrimSpace(fmt.Sprint(args["duration"])))
						if err != nil {
							return nil, fmt.Errorf("禁言时长必须为非负整数（秒），0 表示解除禁言")
						}
					}
					if definition.name != qq_official.RecallToolName && message.GroupID == "" {
						return nil, fmt.Errorf("目标消息不属于群聊")
					}
					reason, _ := args["reason"].(string)
					return adapter.Moderate(ctx, definition.name, message, duration, reason)
				},
			})
		}
	}
	if h.apis == nil {
		return
	}
	register := func(name, description string, invoke func(context.Context, map[string]any) (map[string]any, error)) {
		h.apis.SetAPILocalHandler(name, qq_official.PluginID, map[string]any{
			"version": "1", "public": true, "enabled": true, "description": description,
		}, func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
			result, err := invoke(ctx, args)
			if err != nil {
				return map[string]any{"success": false, "error": err.Error()}, nil
			}
			return result, nil
		})
	}
	register("adapter.qq_official.group.set_group_ban", "设置群成员禁言", func(ctx context.Context, args map[string]any) (map[string]any, error) {
		group, _ := args["group_id"].(string)
		user, _ := args["user_id"].(string)
		if strings.TrimSpace(group) == "" || strings.TrimSpace(user) == "" {
			return nil, fmt.Errorf("group_id 和 user_id 必须非空")
		}
		duration, valid := strictInt(args["duration"], -1)
		if !valid || duration < 0 {
			return nil, fmt.Errorf("禁言时长必须为非负整数（秒），0 表示解除禁言")
		}
		reason, _ := args["reason"].(string)
		return adapter.Moderate(ctx, qq_official.MuteToolName, qq_official.ModerationTarget{GroupID: group, UserID: user}, duration, reason)
	})
	register("adapter.qq_official.message.delete_msg", "撤回消息", func(ctx context.Context, args map[string]any) (map[string]any, error) {
		id, _ := args["message_id"].(string)
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("缺少目标消息 ID")
		}
		stream, _ := args["stream_id"].(string)
		message, err := h.qqOfficialModerationMessage(ctx, adapter, strings.TrimSpace(stream), id)
		if err != nil {
			return nil, err
		}
		return adapter.Moderate(ctx, qq_official.RecallToolName, message, 0, "")
	})
}

func (h *Host) qqOfficialModerationMessage(ctx context.Context, adapter *qq_official.Adapter, session, id string) (qq_official.ModerationTarget, error) {
	h.mu.RLock()
	lookup := h.nativeMessageLookup
	h.mu.RUnlock()
	if lookup == nil {
		return qq_official.ModerationTarget{}, fmt.Errorf("消息存储未就绪")
	}
	row, err := lookup(ctx, session, strings.TrimSpace(id))
	if err != nil {
		return qq_official.ModerationTarget{}, err
	}
	if row == nil || row.ExternalMessageID == "" || row.AccountID != adapter.Runtime().RouteAccountID() || row.Platform != qq_official.PlatformQQ {
		return qq_official.ModerationTarget{}, fmt.Errorf("未找到当前 QQ 官方连接中的消息 %s", id)
	}
	additional := func(key string) string {
		value, _ := row.AdditionalConfig[key].(string)
		return strings.TrimSpace(value)
	}
	return qq_official.ModerationTarget{
		MessageID: row.ExternalMessageID, GroupID: firstNonEmptyText(row.GroupID, additional("platform_io_target_group_id")),
		TargetID: firstNonEmptyText(additional("platform_io_target_user_id"), row.ExternalTargetID),
		UserID:   firstNonEmptyText(row.ExternalUserID, strconv.FormatInt(row.UserID, 10)), UserName: firstNonEmptyText(row.UserCardname, row.UserNickname),
		GroupOpenID: additional("qq_official_group_openid"), UserOpenID: additional("qq_official_user_openid"),
	}, nil
}

func (h *Host) qqOfficialLoginStatus(ctx context.Context, adapter *qq_official.Adapter, action string) (map[string]any, error) {
	login := h.ensureQQOfficialQRLogin()
	var report qq_official.QRLoginSnapshot
	switch action {
	case "start":
		var err error
		report, err = login.Start(ctx)
		if err != nil {
			return nil, err
		}
	case "cancel":
		if err := login.Cancel(ctx); err != nil {
			return nil, err
		}
		report = login.Snapshot()
	default:
		report = login.Snapshot()
	}
	result := map[string]any{
		"state": report.State, "message": report.Message, "image": report.Image,
		"app_id": report.AppID, "expires_at": nil,
	}
	if report.ExpiresAt != nil {
		result["expires_at"] = *report.ExpiresAt
	}
	if action == "status" {
		settings := adapter.Settings()
		ready, _, name, _ := adapter.Runtime().SnapshotForReport()
		result["connected"], result["bot_name"] = ready, name
		result["enabled"], result["configured_app_id"] = settings.Plugin.Enabled, settings.QQOfficial.AppID
	}
	return result, nil
}

func (h *Host) loadQQOfficialWebUI() error {
	raw, err := qqofficialassets.WebUI.ReadFile("webui.json")
	if err != nil {
		return err
	}
	definition, err := platformio.DecodeWireObject(raw)
	if err != nil {
		return err
	}
	pages, _ := definition["pages"].([]any)
	for _, value := range pages {
		page, _ := value.(map[string]any)
		if page["mode"] != "custom" {
			continue
		}
		entry, _ := page["entry"].(string)
		document, err := qqofficialassets.WebUI.ReadFile(entry)
		if err != nil {
			return err
		}
		page["document"] = string(document)
	}
	return h.ApplyWebUIExtension(qq_official.PluginID, definition)
}

// coerceToolStringArg 对齐 Python str(value or "").strip()，兼容数值型消息 ID。
func coerceToolStringArg(raw any) string {
	switch value := raw.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(value)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case uint64:
		return strconv.FormatUint(value, 10)
	case float64:
		return strconv.FormatInt(int64(value), 10)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}
