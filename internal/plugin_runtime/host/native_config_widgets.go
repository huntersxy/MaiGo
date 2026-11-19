package host

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/qq_official"
	"MaiGo/internal/platform_io/drivers/snowluma"
	"MaiGo/internal/platform_io/drivers/telegram"
	"MaiGo/internal/platform_io/drivers/weixin_clawbot"
)

func (h *Host) nativeConfigWidgets(ctx context.Context, pluginID, actionID string) (map[string]any, error) {
	switch pluginID {
	case telegram.PluginID:
		return h.telegramConfigWidgets(ctx, actionID)
	case snowluma.PluginID:
		return h.snowlumaConfigWidgets(ctx, actionID)
	case qq_official.PluginID:
		return h.qqOfficialConfigWidgets(ctx, actionID)
	case weixin_clawbot.PluginID:
		return h.weixinConfigWidgets(actionID)
	default:
		return nil, fmt.Errorf("未登记的原生适配器: %s", pluginID)
	}
}

func widgetDef(id, kind, label string, confirmation any) map[string]any {
	out := map[string]any{
		"id":    id,
		"kind":  kind,
		"label": label,
	}
	if confirmation != nil {
		out["confirmation"] = confirmation
	} else {
		out["confirmation"] = nil
	}
	return out
}

func widgetState(text, tone string, disabled, visible bool, image any, expiresAt any) map[string]any {
	if tone == "" {
		tone = "neutral"
	}
	return map[string]any{
		"text":       text,
		"tone":       tone,
		"image":      image,
		"expires_at": expiresAt,
		"visible":    visible,
		"disabled":   disabled,
	}
}

func (h *Host) telegramConfigWidgets(ctx context.Context, actionID string) (map[string]any, error) {
	widgets := []any{
		widgetDef("connection", "status", "Telegram 连接状态", nil),
		widgetDef("reconnect", "action", "重新连接", nil),
	}
	snapshot, err := h.inspectNativePluginConfig(telegram.PluginID, nil, false)
	if err != nil {
		return nil, err
	}
	configured := false
	if section, ok := snapshot.NormalizedConfig["telegram"].(map[string]any); ok {
		configured = strings.TrimSpace(fmt.Sprint(section["bot_token"])) != "" && fmt.Sprint(section["bot_token"]) != "<nil>"
		if token, ok := section["bot_token"].(string); ok {
			configured = strings.TrimSpace(token) != ""
		}
	}
	if actionID == "reconnect" {
		if !configured {
			return nil, fmt.Errorf("请先填写 Bot Token")
		}
		if err := h.reconnectNativeAdapter(ctx, telegram.PluginID); err != nil {
			return nil, err
		}
	} else if actionID != "" {
		return nil, fmt.Errorf("未知配置页操作: %s", actionID)
	}
	ready, username, started := false, "", platformio.DefaultNativeRuntime().IsStarted(telegram.PluginID)
	if adapter, ok := platformio.DefaultNativeRuntime().StartedAdapter(telegram.PluginID); ok {
		if typed, ok := adapter.(*telegram.Adapter); ok && typed.Runtime() != nil {
			r := typed.Runtime()
			ready = r.Ready()
			username = r.Username()
		}
	}
	var text, tone string
	switch {
	case ready && username != "":
		text, tone = "已连接：@"+username, "success"
	case ready:
		text, tone = "已连接", "success"
	case started:
		text, tone = "连接中 / 等待重连", "neutral"
	case configured:
		text, tone = "尚未连接", "neutral"
	default:
		text, tone = "请先填写 Bot Token；Telegram Bot API 无需扫码登录", "neutral"
	}
	return map[string]any{
		"widgets": widgets,
		"states": map[string]any{
			"connection": widgetState(text, tone, false, true, nil, nil),
			"reconnect":  widgetState("", "neutral", !configured, true, nil, nil),
		},
	}, nil
}

func (h *Host) snowlumaConfigWidgets(ctx context.Context, actionID string) (map[string]any, error) {
	widgets := []any{
		widgetDef("connection", "status", "QQ 连接器状态", nil),
		widgetDef("reconnect", "action", "重新连接", nil),
	}
	snapshot, err := h.inspectNativePluginConfig(snowluma.PluginID, nil, false)
	if err != nil {
		return nil, err
	}
	enabled := snapshot.Enabled
	if actionID == "reconnect" {
		if !enabled {
			return nil, fmt.Errorf("请先启用适配器")
		}
		if err := h.reconnectNativeAdapter(ctx, snowluma.PluginID); err != nil {
			return nil, err
		}
	} else if actionID != "" {
		return nil, fmt.Errorf("未知配置页操作: %s", actionID)
	}
	started := platformio.DefaultNativeRuntime().IsStarted(snowluma.PluginID)
	connected, accountID := false, ""
	if adapter, ok := platformio.DefaultNativeRuntime().StartedAdapter(snowluma.PluginID); ok {
		if typed, ok := adapter.(*snowluma.Adapter); ok && typed.Runtime() != nil {
			r := typed.Runtime()
			connected = r.Connected()
			accountID = r.AccountID()
		}
	}
	var text, tone string
	switch {
	case connected && accountID != "":
		text, tone = "已连接："+accountID, "success"
	case connected:
		text, tone = "已连接", "success"
	case started:
		text, tone = "连接中 / 等待重连", "neutral"
	case enabled:
		text, tone = "尚未连接", "neutral"
	default:
		text, tone = "请先启用适配器并填写客户端连接", "neutral"
	}
	return map[string]any{
		"widgets": widgets,
		"states": map[string]any{
			"connection": widgetState(text, tone, false, true, nil, nil),
			"reconnect":  widgetState("", "neutral", !enabled, true, nil, nil),
		},
	}, nil
}

func (h *Host) weixinConfigWidgets(actionID string) (map[string]any, error) {
	if actionID != "" {
		return nil, fmt.Errorf("未知配置页操作: %s", actionID)
	}
	return map[string]any{
		"widgets": []any{
			widgetDef("status", "status", "微信账号", nil),
		},
		"states": map[string]any{
			"status": widgetState("请在「插件扩展 / 微信账号管理」中添加账号并扫码", "neutral", false, true, nil, nil),
		},
	}, nil
}

func (h *Host) qqOfficialConfigWidgets(ctx context.Context, actionID string) (map[string]any, error) {
	widgets := []any{
		widgetDef("connection", "status", "连接状态", nil),
		widgetDef("login", "qrcode", "QQ 扫码授权", nil),
		widgetDef("login_start", "action", "生成 / 刷新二维码", "扫码授权成功后将替换 AppID / AppSecret 并重新连接，是否继续？"),
		widgetDef("login_cancel", "action", "取消扫码", nil),
		widgetDef("reconnect", "action", "重新连接", nil),
	}
	snapshot, err := h.inspectNativePluginConfig(qq_official.PluginID, nil, false)
	if err != nil {
		return nil, err
	}
	configured := false
	if section, ok := snapshot.NormalizedConfig["qq_official"].(map[string]any); ok {
		appID, _ := section["app_id"].(string)
		secret, _ := section["app_secret"].(string)
		configured = strings.TrimSpace(appID) != "" && strings.TrimSpace(secret) != ""
	}
	login := h.ensureQQOfficialQRLogin()
	switch actionID {
	case "":
	case "login_start":
		if _, err := login.Start(ctx); err != nil {
			return nil, err
		}
	case "login_cancel":
		_ = login.Cancel(ctx)
	case "reconnect":
		if !configured {
			return nil, fmt.Errorf("请扫码授权或填写 AppID / AppSecret")
		}
		if err := h.reconnectNativeAdapter(ctx, qq_official.PluginID); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("未知配置页操作: %s", actionID)
	}
	loginSnap := login.Snapshot()
	active := loginSnap.State == "creating" || loginSnap.State == "waiting" || loginSnap.State == "saving"
	started := platformio.DefaultNativeRuntime().IsStarted(qq_official.PluginID)
	ready, botName, accountID := false, "", ""
	if adapter, ok := platformio.DefaultNativeRuntime().StartedAdapter(qq_official.PluginID); ok {
		if typed, ok := adapter.(*qq_official.Adapter); ok && typed.Runtime() != nil {
			r := typed.Runtime()
			ready, accountID, botName, _ = r.SnapshotForReport()
		}
	}
	var connectionText, tone string
	switch {
	case ready:
		name := botName
		if name == "" {
			name = accountID
		}
		if name != "" {
			connectionText = "已连接：" + name
		} else {
			connectionText = "已连接"
		}
		tone = "success"
	case started:
		connectionText, tone = "连接中 / 等待重连", "neutral"
	case configured:
		connectionText, tone = "尚未连接", "neutral"
	default:
		connectionText, tone = "请扫码授权或填写 AppID / AppSecret", "neutral"
	}
	var image any
	if loginSnap.Image != "" {
		image = loginSnap.Image
	}
	var expires any
	if loginSnap.ExpiresAt != nil {
		expires = *loginSnap.ExpiresAt
	}
	loginVisible := active || !ready || loginSnap.State == "error" || loginSnap.State == "expired"
	return map[string]any{
		"widgets": widgets,
		"states": map[string]any{
			"connection":  widgetState(connectionText, tone, false, true, nil, nil),
			"login":       widgetState(loginSnap.Message, "neutral", false, loginVisible, image, expires),
			"login_start": widgetState("", "neutral", loginSnap.State == "creating" || loginSnap.State == "saving", true, nil, nil),
			"login_cancel": widgetState("", "neutral", !active, true, nil, nil),
			"reconnect":   widgetState("", "neutral", !configured || active, true, nil, nil),
		},
	}, nil
}

func (h *Host) reconnectNativeAdapter(ctx context.Context, pluginID string) error {
	if platformio.DefaultNativeRuntime().IsStarted(pluginID) {
		if err := h.NativeTeardown(ctx, pluginID); err != nil {
			return err
		}
	}
	h.SetNativeInProcessReason(pluginID, "已由进程内 Go 适配器承接")
	if err := h.StartNativeRuntimeIfEnabled(ctx, pluginID); err != nil {
		h.rememberNativeLoadStatus(pluginID, "failed")
		return err
	}
	if platformio.DefaultNativeRuntime().IsStarted(pluginID) {
		h.rememberNativeLoadStatus(pluginID, "success")
	} else {
		h.rememberNativeLoadStatus(pluginID, "inactive")
	}
	return nil
}
