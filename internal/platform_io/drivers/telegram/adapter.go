package telegram

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
)

// Adapter 进程内 Telegram 适配器
type Adapter struct {
	platformio.BaseAdapter
	mu        sync.Mutex
	desc      platformio.DriverDescriptor
	settings  Settings
	runtime   *RuntimeState
	hostHooks *HostHooks
	client    *Client
	store     *Store
	started   bool
	polling   bool
	cancel    context.CancelFunc
	sendMu    sync.Mutex
}

// NewAdapter 构造适配器；enabled=false 时 Start 不轮询
func NewAdapter(settings Settings, hooks *HostHooks) *Adapter {
	desc := platformio.BuildNativeAdapterDescriptor(
		PluginID,
		GatewayName,
		PlatformTelegram,
		"",
		"",
		map[string]any{
			"route_type": RouteTypeDuplex,
			"protocol":   ProtocolBotAPI,
			"connection": ProtocolBotAPI,
		},
	)
	return &Adapter{
		BaseAdapter: platformio.NewBaseAdapter(desc),
		desc:        desc,
		settings:    settings,
		runtime:     NewRuntimeState(),
		hostHooks:   hooks,
	}
}

// Descriptor 返回可变驱动描述
func (a *Adapter) Descriptor() platformio.DriverDescriptor {
	if a == nil {
		return platformio.DriverDescriptor{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.desc
}

// Start 仅在 enabled=true 时启动长轮询
func (a *Adapter) Start(ctx context.Context) error {
	if a == nil {
		return fmt.Errorf("telegram adapter 为空")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return nil
	}
	if !a.settings.Plugin.Enabled {
		return nil
	}
	if strings.TrimSpace(a.settings.Telegram.BotToken) == "" {
		zap.L().Error("Telegram 适配器缺少 bot_token，请在插件配置中填写")
		return nil
	}
	client, err := NewClient(
		a.settings.Telegram.BotToken,
		a.settings.Telegram.APIBaseURL,
		a.settings.Telegram.ProxyURL,
		a.settings.Telegram.PollTimeoutSec,
		a.settings.Telegram.MaxDownloadMB*1024*1024,
	)
	if err != nil {
		return err
	}
	store := NewStore(filepath.Join(resolveDataDir(a.hostHooks), "telegram.sqlite3"))
	if err := store.Initialize(); err != nil {
		client.Close()
		return err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	a.client = client
	a.store = store
	a.cancel = cancel
	a.started = true
	a.polling = true
	poller := NewPoller(
		a.settings.Telegram,
		client,
		store,
		a.runtime,
		a.onReady,
		a.onUpdate,
		a.onUnready,
	)
	go func() {
			defer func() {
				a.mu.Lock()
				a.polling = false
				a.mu.Unlock()
			}()
			poller.Run(runCtx)
		}()
	return nil
}

// Stop 停止轮询
func (a *Adapter) Stop(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	if a.client != nil {
		a.client.Close()
		a.client = nil
	}
	if a.store != nil {
		_ = a.store.Close()
		a.store = nil
	}
	if a.runtime != nil {
		a.runtime.MarkNotReady()
	}
	if a.hostHooks != nil && a.hostHooks.ReportGatewayState != nil {
		_ = a.hostHooks.ReportGatewayState(ctx, a.runtime)
	}
	if a.hostHooks != nil && a.hostHooks.APIs != nil {
		a.hostHooks.APIs.RemoveAPIsByPlugin(PluginID)
	}
	a.polling = false
	a.started = false
	return nil
}

// Send 独立出站，不走 PluginPlatformDriver
func (a *Adapter) Send(ctx context.Context, outbound platformio.OutboundMessageEnvelope, routeKey platformio.RouteKey) (platformio.DeliveryReceipt, error) {
	receipt := platformio.DeliveryReceipt{
		InternalMessageID: outbound.InternalMessageID,
		RouteKey:          routeKey,
		Status:            platformio.DeliveryStatusPending,
		DriverID:          a.Descriptor().DriverID,
		DriverKind:        platformio.DriverKindPlugin,
	}
	if a == nil {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "适配器未启动"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.mu.Lock()
	client := a.client
	ready := a.runtime != nil && a.runtime.Ready()
	accountID := ""
	if a.runtime != nil {
		accountID = a.runtime.AccountID()
	}
	store := a.store
	a.mu.Unlock()
	if client == nil || !ready {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "Telegram 网关尚未就绪"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	if routeKey.Platform != "" && routeKey.Platform != PlatformTelegram {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "出站消息的路由身份与 Telegram 网关不一致"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	if routeKey.AccountID != "" && accountID != "" && routeKey.AccountID != accountID {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "出站消息的路由身份与 Telegram 网关不一致"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	chatID, threadID, err := OutboundTargetChatID(outbound)
	if err != nil {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = err.Error()
		return receipt, err
	}
	parts, err := EncodeOutbound(outbound)
	if err != nil {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = err.Error()
		return receipt, err
	}
	sent := make([]string, 0, len(parts))
	for index, part := range parts {
		method := asString(part["method"])
		body, _ := asMap(part["body"])
		if body == nil {
			body = map[string]any{}
		}
		body["chat_id"] = chatID
		if threadID != nil {
			body["message_thread_id"] = *threadID
		}
		if index == 0 && outbound.ExternalReplyToMessageID != "" {
			if replyChat, replyMsg, parseErr := ParseMessageID(outbound.ExternalReplyToMessageID); parseErr == nil && replyChat == chatID {
				body["reply_parameters"] = map[string]any{"message_id": replyMsg}
			}
		}
		result, callErr := client.Call(ctx, method, body, nil)
		if callErr != nil {
			receipt.Status = platformio.DeliveryStatusFailed
			if len(sent) > 0 {
				receipt.Error = fmt.Sprintf("已发送 %d 条后失败，不能整体重发: %v", len(sent), callErr)
				receipt.ExternalMessageID = sent[0]
			} else {
				receipt.Error = callErr.Error()
			}
			receipt.Metadata = map[string]any{"telegram_message_ids": sent}
			return receipt, callErr
		}
		item, _ := asMap(result)
		if item == nil || item["message_id"] == nil {
			receipt.Status = platformio.DeliveryStatusFailed
			receipt.Error = "Telegram 发送结果缺少 message_id"
			return receipt, fmt.Errorf("%s", receipt.Error)
		}
		msgID, _ := asInt64(item["message_id"])
		sentChat := chatID
		if chat, ok := asMap(item["chat"]); ok {
			if n, ok := asInt64(chat["id"]); ok {
				sentChat = n
			}
		}
		sentID := ExternalID(sentChat, msgID)
		sent = append(sent, sentID)
		if chatID < 0 && store != nil {
			group := map[string]string{"group_id": fmt.Sprint(chatID), "group_name": fmt.Sprint(chatID)}
			if threadID != nil {
				group["group_id"] = fmt.Sprintf("%d:%d", chatID, *threadID)
			}
			if outbound.MessageInfo.GroupInfo != nil {
				if outbound.MessageInfo.GroupInfo.GroupID != "" {
					group["group_id"] = outbound.MessageInfo.GroupInfo.GroupID
				}
				if outbound.MessageInfo.GroupInfo.GroupName != "" {
					group["group_name"] = outbound.MessageInfo.GroupInfo.GroupName
				}
			}
			_ = store.SaveMessageGroup(accountID, sentID, group)
		}
	}
	receipt.Status = platformio.DeliveryStatusSent
	receipt.ExternalMessageID = sent[0]
	receipt.Metadata = map[string]any{"telegram_message_ids": sent}
	return receipt, nil
}

// Polled 是否已进入轮询（enabled=false 时为 false）
func (a *Adapter) Polled() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.polling
}

func (a *Adapter) Runtime() *RuntimeState {
	if a == nil {
		return nil
	}
	return a.runtime
}

func (a *Adapter) Settings() Settings {
	if a == nil {
		return DefaultSettings()
	}
	return a.settings
}

func (a *Adapter) onReady(ctx context.Context, accountID, username string) error {
	a.mu.Lock()
	a.runtime.MarkReady(accountID, username)
	a.desc = platformio.BuildNativeAdapterDescriptor(
		PluginID, GatewayName, PlatformTelegram, accountID, "",
		map[string]any{
			"route_type": RouteTypeDuplex,
			"protocol":   ProtocolBotAPI,
			"connection": ProtocolBotAPI,
		},
	)
	a.mu.Unlock()
	if a.hostHooks != nil && a.hostHooks.RegisterDriver != nil {
		if err := a.hostHooks.RegisterDriver(ctx, a); err != nil {
			return err
		}
	}
	if a.hostHooks != nil && a.hostHooks.ReportGatewayState != nil {
		return a.hostHooks.ReportGatewayState(ctx, a.runtime)
	}
	return nil
}

func (a *Adapter) onUnready(ctx context.Context) error {
	if a.runtime != nil {
		a.runtime.MarkNotReady()
	}
	if a.hostHooks != nil && a.hostHooks.ReportGatewayState != nil {
		return a.hostHooks.ReportGatewayState(ctx, a.runtime)
	}
	return nil
}

func (a *Adapter) onUpdate(ctx context.Context, update map[string]any) error {
	if a == nil {
		return nil
	}
	payload, ok := asMap(update["message"])
	if !ok {
		return nil
	}
	media, err := PrepareInboundMedia(payload)
	if err != nil {
		return err
	}
	topicName := ""
	accountID := a.runtime.AccountID()
	if asBool(payload["is_topic_message"]) && a.store != nil {
		chat, _ := asMap(payload["chat"])
		threadID, _ := asInt64(payload["message_thread_id"])
		topic, _ := asMap(payload["forum_topic_created"])
		if topic == nil {
			topic, _ = asMap(payload["forum_topic_edited"])
		}
		name := ""
		if topic != nil {
			name = asString(topic["name"])
		}
		topicName, _ = a.store.TopicName(accountID, asString(chat["id"]), threadID, name)
	}
	message, err := DecodeMessage(payload, accountID, a.runtime.Username(), media, topicName)
	if err != nil {
		return err
	}
	if message == nil {
		return nil
	}
	if info, ok := asMap(message["message_info"]); ok && a.store != nil {
		if groupAny, ok := asMap(info["group_info"]); ok {
			_ = a.store.SaveMessageGroup(accountID, asString(message["message_id"]), map[string]string{
				"group_id":   asString(groupAny["group_id"]),
				"group_name": asString(groupAny["group_name"]),
			})
		}
	}
	var sink InboundSink
	if a.hostHooks != nil {
		sink = a.hostHooks.Inbound
	}
	return deliverInbound(ctx, sink, accountID, message)
}
