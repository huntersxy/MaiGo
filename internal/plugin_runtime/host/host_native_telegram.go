package host

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/telegram"
	"MaiGo/internal/plugin_runtime/capabilities"
	"MaiGo/internal/plugin_runtime/hooks"
	"MaiGo/internal/plugin_runtime/protocol"
)

func (h *Host) buildTelegramHostHooks() *telegram.HostHooks {
	if h == nil {
		return nil
	}
	var inbound telegram.InboundSink
	if port, ok := h.MessageGatewayPort().(PluginGatewayInboundPort); ok && port != nil {
		inbound = port
	}
	return &telegram.HostHooks{
		Inbound: inbound,
		APIs:    h.APIRegistry(),
		ReportGatewayState: func(ctx context.Context, state *telegram.RuntimeState) error {
			return h.reportTelegramGatewayState(ctx, state)
		},
		RegisterDriver: func(ctx context.Context, adapter platformio.Adapter) error {
			if adapter == nil {
				return fmt.Errorf("telegram adapter 为空")
			}
			desc := adapter.Descriptor()
			return h.RegisterNativeGatewayDriver(ctx, NativeGatewayDriverSpec{
				PluginID:        telegram.PluginID,
				GatewayName:     telegram.GatewayName,
				ComponentName:   telegram.GatewayName,
				Platform:        telegram.PlatformTelegram,
				AccountID:       desc.AccountID,
				Scope:           desc.Scope,
				RouteType:       telegram.RouteTypeDuplex,
				Protocol:        telegramProtocol(desc.Metadata),
				SupportsSend:    true,
				SupportsReceive: true,
				Metadata: map[string]any{
					"plugin_type": "adapter",
					"connection":  telegram.ProtocolBotAPI,
				},
				Adapter: adapter,
			})
		},
		DataDir: func() string {
			root := ResolveInstanceRoot()
			if root == "" {
				return filepath.Join("data", "plugins", telegram.PluginID)
			}
			return filepath.Join(root, "data", "plugins", telegram.PluginID)
		},
	}
}

func (h *Host) reportTelegramGatewayState(ctx context.Context, state *telegram.RuntimeState) error {
	if h == nil || state == nil {
		return nil
	}
	ready, accountID, _, connected := state.SnapshotForReport()
	if !connected {
		_, _, err := h.applyNativeMessageGatewayState(ctx, telegram.PluginID, protocol.MessageGatewayStateUpdatePayload{
			GatewayName: telegram.GatewayName,
			Ready:       false,
			Platform:    telegram.PlatformTelegram,
		})
		return err
	}
	_, _, err := h.applyNativeMessageGatewayState(ctx, telegram.PluginID, protocol.MessageGatewayStateUpdatePayload{
		GatewayName: telegram.GatewayName,
		Ready:       ready,
		Platform:    telegram.PlatformTelegram,
		AccountID:   accountID,
		Metadata: map[string]any{
			"connection": telegram.ProtocolBotAPI,
		},
	})
	return err
}

func (h *Host) ensureTelegramFactoryWithHooks() error {
	reg, ok := platformio.DefaultNativeRuntime().Get(telegram.PluginID)
	if !ok {
		return fmt.Errorf("未登记的原生适配器: %s", telegram.PluginID)
	}
	reg.Factory = telegram.NewFactory(h.buildTelegramHostHooks())
	reg.DefaultConfig = telegram.DefaultConfigMap()
	return platformio.DefaultNativeRuntime().Register(reg)
}

func (h *Host) startTelegramIfEnabled(ctx context.Context, config map[string]any) error {
	if err := h.ensureTelegramFactoryWithHooks(); err != nil {
		return err
	}
	adapter, err := platformio.DefaultNativeRuntime().StartRuntime(ctx, telegram.PluginID, config)
	if err != nil {
		return err
	}
	if err := adapter.Start(ctx); err != nil {
		platformio.DefaultNativeRuntime().StopRuntime(telegram.PluginID)
		return err
	}
	if native, ok := adapter.(*telegram.Adapter); ok && h.hooks != nil {
		h.registerTelegramReactionTool(native)
		if h.components != nil && h.components.GetComponent(telegram.PluginID+".telegram_reply_typing") == nil {
			h.components.RegisterLocalComponent(&ComponentEntry{
				Name: "telegram_reply_typing", FullName: telegram.PluginID + ".telegram_reply_typing",
				PluginID: telegram.PluginID, ComponentType: ComponentTypeHookHandler, Enabled: true,
				HookName: "maisaka.replyer.before_request", HookMode: "blocking", TimeoutMS: 3000,
			})
		}
		h.hooks.ReplacePlugin(telegram.PluginID, []hooks.Subscription{{
			HookName: "maisaka.replyer.before_request", PluginID: telegram.PluginID,
			HandlerName: "telegram_reply_typing", Mode: hooks.HandlerModeBlocking, TimeoutMS: 3000,
			Handler: func(ctx context.Context, args map[string]any) (hooks.HandlerResult, error) {
				result := hooks.HandlerResult{Success: true, Action: "continue"}
				sessionID, _ := args["session_id"].(string)
				if sessionID == "" || !native.Runtime().Ready() {
					return result, nil
				}
				stream, err := h.telegramStream(ctx, sessionID)
				if err != nil || stream == nil {
					return result, err
				}
				account, scope, target := telegramStreamRoute(stream)
				return result, native.StartTyping(account, scope, target)
			},
		}})
	}
	zap.L().Info("原生适配器运行时已启动", zap.String("plugin_id", telegram.PluginID))
	return nil
}

func (h *Host) registerTelegramReactionTool(adapter *telegram.Adapter) {
	if h.components == nil {
		return
	}
	name := telegram.ReactionToolName
	h.components.RegisterLocalComponent(&ComponentEntry{
		Name: name, FullName: telegram.PluginID + "." + name, PluginID: telegram.PluginID,
		ComponentType: ComponentTypeTool, ChatScope: ComponentChatScopeAll, Enabled: true,
		TimeoutMS: 30000, Platforms: []string{telegram.PlatformTelegram}, InvokeMethod: "plugin.invoke_tool",
		Description: "仅用于 Telegram：自主选择当前聊天的一条消息添加表态，表达赞同、感谢或情绪，也可以只表态而不发文字。每条消息最多一个普通 emoji，再次调用替换自己的表态；emoji 为空字符串时撤销自己的表态。具体受聊天允许的表情限制，不能使用付费表态。",
		ParametersRaw: map[string]any{
			"type": "object", "additionalProperties": false, "required": []any{"msg_id", "emoji"},
			"properties": map[string]any{
				"msg_id": map[string]any{"type": "string", "description": "当前聊天上下文中的目标消息编号"},
				"emoji":  map[string]any{"type": "string", "description": "一个普通 emoji；空字符串撤销表态"},
			},
		},
		localTool: func(ctx context.Context, args map[string]any, exec ToolExecutionContext) (any, error) {
			sessionID := firstNonEmptyText(exec.SessionID, exec.StreamID)
			if exec.Platform != telegram.PlatformTelegram || sessionID == "" {
				return nil, fmt.Errorf("表态工具仅支持当前 Telegram 聊天")
			}
			id, idOK := args["msg_id"].(string)
			emoji, emojiOK := args["emoji"].(string)
			if !idOK || strings.TrimSpace(id) == "" || !emojiOK {
				return nil, fmt.Errorf("必须提供有效的 msg_id 和字符串 emoji")
			}
			stream, err := h.telegramStream(ctx, sessionID)
			if err != nil {
				return nil, err
			}
			if stream == nil {
				return nil, fmt.Errorf("当前真实 Telegram 聊天流不存在或不唯一")
			}
			account, scope, target := telegramStreamRoute(stream)
			h.mu.RLock()
			lookup := h.nativeMessageLookup
			h.mu.RUnlock()
			if lookup == nil {
				return nil, fmt.Errorf("消息存储未就绪")
			}
			message, err := lookup(ctx, sessionID, strings.TrimSpace(id))
			if err != nil {
				return nil, err
			}
			if message == nil || message.SessionID != sessionID {
				return nil, fmt.Errorf("目标消息不属于当前真实聊天流")
			}
			if message.Platform != telegram.PlatformTelegram || message.AccountID != account || message.Scope != scope {
				return nil, fmt.Errorf("目标消息的账号与 Telegram 网关不一致")
			}
			return adapter.SetReaction(ctx, account, scope, target, message.ExternalMessageID, emoji)
		},
	})
}

func (h *Host) telegramStream(ctx context.Context, sessionID string) (map[string]any, error) {
	response, err := h.InvokeCapability(ctx, telegram.PluginID, capabilities.CapChatGetAllStreams, map[string]any{"platform": telegram.PlatformTelegram})
	if err != nil {
		return nil, err
	}
	payload, _ := response.(map[string]any)
	if payload["success"] != true {
		return nil, fmt.Errorf("查询 Telegram 聊天流失败: %v", payload["error"])
	}
	streams, _ := payload["streams"].([]map[string]any)
	var match map[string]any
	for _, stream := range streams {
		if stream["session_id"] != sessionID || stream["platform"] != telegram.PlatformTelegram {
			continue
		}
		if match != nil {
			return nil, nil
		}
		match = stream
	}
	return match, nil
}

func telegramStreamRoute(stream map[string]any) (account, scope, target string) {
	account, _ = stream["account_id"].(string)
	scope, _ = stream["scope"].(string)
	target, _ = stream["user_id"].(string)
	if stream["is_group_session"] == true {
		target, _ = stream["group_id"].(string)
	}
	return account, scope, target
}

func telegramProtocol(metadata map[string]any) string {
	if v := asMetadataString(metadata, "protocol"); v != "" {
		return v
	}
	return telegram.ProtocolBotAPI
}
