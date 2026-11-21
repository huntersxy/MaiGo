package telegram

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vmihailenco/msgpack/v5"
	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
)

// Adapter 进程内 Telegram 适配器
type Adapter struct {
	platformio.BaseAdapter
	mu        sync.Mutex
	lifecycle sync.Mutex
	desc      platformio.DriverDescriptor
	settings  Settings
	runtime   *RuntimeState
	hostHooks *HostHooks
	client    *Client
	store     *Store
	started   bool
	polling   bool
	cancel    context.CancelFunc
	done      chan struct{}
	sendMu    sync.Mutex
	avatarMu  sync.Mutex
	avatars   avatarRelay
	runCtx    context.Context
	typing    map[typingTarget]*typingTask
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
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
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
	a.runCtx = runCtx
	a.started = true
	a.polling = true
	if a.hostHooks != nil && a.hostHooks.APIs != nil {
		a.hostHooks.APIs.SetAPILocalHandler("adapter.avatar.get", PluginID, map[string]any{
			"version": "1", "public": true, "enabled": true, "description": "获取 Telegram 用户或群头像资源",
		}, a.avatar)
	}
	done := make(chan struct{})
	a.done = done
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
		defer close(done)
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
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	a.mu.Lock()
	cancel, done := a.cancel, a.done
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.mu.Lock()
	client, store := a.client, a.store
	a.client, a.store, a.cancel, a.done = nil, nil, nil, nil
	a.runCtx = nil
	a.started, a.polling = false, false
	a.mu.Unlock()
	a.avatarMu.Lock()
	a.avatars.close()
	a.avatarMu.Unlock()
	if client != nil {
		client.Close()
	}
	if store != nil {
		_ = store.Close()
	}
	_ = a.onUnready(ctx)
	if a.hostHooks != nil && a.hostHooks.APIs != nil {
		a.hostHooks.APIs.RemoveAPIsByPlugin(PluginID)
	}
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
	reply, err := replyParameters(outbound, chatID)
	if err != nil {
		receipt.Status, receipt.Error = platformio.DeliveryStatusFailed, err.Error()
		return receipt, err
	}
	parts, err := encodeOutbound(ctx, outbound.Segments, 0)
	if err == nil && len(parts) == 0 {
		err = fmt.Errorf("出站消息为空")
	}
	if err != nil {
		receipt.Status, receipt.Error = platformio.DeliveryStatusFailed, err.Error()
		return receipt, err
	}
	requests, err := prepareRequests(ctx, parts, store, accountID, time.Duration(a.settings.Telegram.MediaTimeoutSec)*time.Second)
	if err != nil {
		receipt.Status, receipt.Error = platformio.DeliveryStatusFailed, err.Error()
		return receipt, err
	}
	if reply != nil && requests[0].method == "forwardMessage" {
		requests = append([]telegramRequest{{method: "sendMessage", body: map[string]any{"text": "转发消息："}}}, requests...)
	}
	sent := make([]string, 0, len(requests))
	fail := func(err error) (platformio.DeliveryReceipt, error) {
		if len(sent) > 0 {
			err = fmt.Errorf("已发送 %d 条后失败，不能整体重发: %w", len(sent), err)
			receipt.ExternalMessageID = sent[0]
		}
		receipt.Status, receipt.Error = platformio.DeliveryStatusFailed, err.Error()
		receipt.Metadata = map[string]any{"telegram_message_ids": sent}
		return receipt, err
	}
	a.stopTyping(newTypingTarget(chatID, threadID))
	for index, request := range requests {
		body := request.body
		body["chat_id"] = chatID
		if threadID != nil {
			body["message_thread_id"] = *threadID
		}
		if index == 0 && reply != nil {
			body["reply_parameters"] = reply
		}
		result, err := client.Call(ctx, request.method, body, request.uploads)
		if err != nil {
			return fail(err)
		}
		items := []any{result}
		if request.method == "sendMediaGroup" {
			items, _ = result.([]any)
			if len(items) == 0 {
				return fail(fmt.Errorf("Telegram 相册发送结果为空或格式无效"))
			}
		}
		for _, raw := range items {
			item, _ := asMap(raw)
			msgID, valid := asInt64(item["message_id"])
			chat, _ := asMap(item["chat"])
			sentChat, validChat := asInt64(chat["id"])
			if !valid || msgID <= 0 || !validChat {
				return fail(fmt.Errorf("Telegram 发送结果缺少 message_id 或 chat.id"))
			}
			sentID := ExternalID(sentChat, msgID)
			sent = append(sent, sentID)
			if chatID < 0 && store != nil {
				group := map[string]string{"group_id": fmt.Sprint(chatID), "group_name": fmt.Sprint(chatID)}
				if threadID != nil {
					group["group_id"] = ExternalID(chatID, *threadID)
				}
				if info := outbound.MessageInfo.GroupInfo; info != nil {
					group["group_id"], group["group_name"] = info.GroupID, info.GroupName
				}
				if err := store.SaveMessageGroup(accountID, sentID, group); err != nil {
					return fail(err)
				}
			}
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
	a.mu.Lock()
	for target, task := range a.typing {
		delete(a.typing, target)
		task.cancel()
	}
	a.mu.Unlock()
	if a.hostHooks != nil && a.hostHooks.ReportGatewayState != nil {
		return a.hostHooks.ReportGatewayState(ctx, a.runtime)
	}
	return nil
}

func (a *Adapter) onUpdate(ctx context.Context, update map[string]any) error {
	if a == nil {
		return nil
	}
	accountID := a.runtime.AccountID()
	_, individual := update["message_reaction"]
	_, counts := update["message_reaction_count"]
	if individual || counts {
		message, err := DecodeReaction(update, accountID)
		if err != nil || message == nil {
			return err
		}
		var group map[string]string
		if a.store != nil {
			group, err = a.store.MessageGroup(accountID, asString(message["reply_to"]))
			if err != nil {
				return err
			}
		}
		info, _ := asMap(message["message_info"])
		if group != nil {
			info["group_info"] = map[string]any{"group_id": group["group_id"], "group_name": group["group_name"]}
		} else {
			extra, _ := asMap(info["additional_config"])
			event, _ := asMap(extra["telegram_event_payload"])
			chat, _ := asMap(event["chat"])
			if asBool(chat["is_forum"]) {
				return nil
			}
		}
		return a.routeInbound(ctx, accountID, message)
	}
	payload, ok := asMap(update["message"])
	if !ok {
		return nil
	}
	chat, _ := asMap(payload["chat"])
	chatType := asString(chat["type"])
	if chatType != "private" && chatType != "group" && chatType != "supergroup" {
		return nil
	}
	sender, _ := asMap(payload["from"])
	senderChat, _ := asMap(payload["sender_chat"])
	if asString(sender["id"]) == accountID && len(senderChat) == 0 {
		return nil
	}
	topicName := ""
	if asBool(payload["is_topic_message"]) && a.store != nil {
		chat, _ := asMap(payload["chat"])
		threadID, valid := asInt64(payload["message_thread_id"])
		chatID, validChat := asInt64(chat["id"])
		if !valid || !validChat || chatID >= 0 || threadID <= 0 {
			return fmt.Errorf("Telegram 话题目标必须为负群 ID 和正话题 ID")
		}
		topic, _ := asMap(payload["forum_topic_created"])
		if topic == nil {
			topic, _ = asMap(payload["forum_topic_edited"])
		}
		name := ""
		if topic != nil {
			name = asString(topic["name"])
		}
		var err error
		topicName, err = a.store.TopicName(accountID, asString(chat["id"]), threadID, name)
		if err != nil {
			return err
		}
	}
	media, err := a.prepareInboundMedia(ctx, payload, accountID)
	if err != nil {
		return err
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
			if err := a.store.SaveMessageGroup(accountID, asString(message["message_id"]), map[string]string{
				"group_id":   asString(groupAny["group_id"]),
				"group_name": asString(groupAny["group_name"]),
			}); err != nil {
				return err
			}
		}
	}
	return a.routeInbound(ctx, accountID, message)
}

func (a *Adapter) routeInbound(ctx context.Context, accountID string, message map[string]any) error {
	binary, err := msgpack.Marshal(message)
	if err != nil {
		return err
	}
	if len(binary) > 15<<20 {
		return fmt.Errorf("Telegram 消息超过麦麦 IPC 大小限制")
	}
	var sink InboundSink
	if a.hostHooks != nil {
		sink = a.hostHooks.Inbound
	}
	return deliverInbound(ctx, sink, accountID, message)
}
