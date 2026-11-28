package qq_official

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
)

// Adapter 进程内 QQ 官方适配器
type Adapter struct {
	platformio.BaseAdapter
	mu             sync.Mutex
	lifecycle      sync.Mutex
	desc           platformio.DriverDescriptor
	settings       Settings
	runtime        *RuntimeState
	hostHooks      *HostHooks
	client         *Client
	transport      *Transport
	identity       *IdentityBridge
	syncIDs        *identitySync
	started        bool
	dialed         bool
	cancel         context.CancelFunc
	sendMu         sync.Mutex
	references     messageReferences
	mapping        idMapping
	union          unionIdentities
	mappingEnabled atomic.Bool
	groupNames     map[string]groupNameEntry
	profileMu      sync.Mutex
	botProfile     botAvatarEntry
}

// NewAdapter 构造适配器；enabled=false 时 Start 不拨号
func NewAdapter(settings Settings, hooks *HostHooks) *Adapter {
	desc := platformio.BuildNativeAdapterDescriptor(
		PluginID,
		GatewayName,
		PlatformQQ,
		"",
		"",
		map[string]any{
			"route_type": RouteTypeDuplex,
			"protocol":   ProtocolOfficial,
			"connection": ProtocolOfficial,
		},
	)
	var invoker LocalAPIInvoker
	if hooks != nil {
		invoker = hooks.InvokeAPI
	}
	settings.QQOfficial.UnifiedAccountID = strings.TrimSpace(settings.QQOfficial.UnifiedAccountID)
	adapter := &Adapter{
		BaseAdapter: platformio.NewBaseAdapter(desc),
		desc:        desc,
		settings:    settings,
		runtime:     NewRuntimeState(),
		hostHooks:   hooks,
		identity:    NewIdentityBridge(invoker),
		references:  messageReferences{proactiveSequence: rand.IntN(10000)},
	}
	adapter.identity.syncOnce = adapter.syncIdentities
	adapter.mappingEnabled.Store(settings.QQOfficial.EnableMapping)
	return adapter
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

// Start 仅在 enabled=true 时启动 WS
func (a *Adapter) Start(ctx context.Context) error {
	if a == nil {
		return fmt.Errorf("qq official adapter 为空")
	}
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	return a.startLocked(ctx)
}

func (a *Adapter) startLocked(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return nil
	}
	if !a.settings.Plugin.Enabled {
		return nil
	}
	directory := resolveDataDir(a.hostHooks)
	if err := recoverBindingDelete(directory); err != nil {
		return err
	}
	mapping, err := loadIDMapping(directory)
	if err != nil {
		return err
	}
	syncIDs, err := loadIdentitySync(directory)
	if err != nil {
		return err
	}
	union, err := loadUnionIdentities(directory)
	if err != nil {
		return err
	}
	a.syncIDs = syncIDs
	a.mapping = mapping
	a.union = union
	a.groupNames = make(map[string]groupNameEntry)
	if a.hostHooks != nil && a.hostHooks.APIs != nil {
		a.registerAPIs(a.hostHooks.APIs)
	}
	if strings.TrimSpace(a.settings.QQOfficial.AppID) == "" || strings.TrimSpace(a.settings.QQOfficial.AppSecret) == "" {
		zap.L().Error("QQ 官方适配器缺少凭据，请在插件配置 [qq_official] 中填写 app_id 和 app_secret")
		return nil
	}
	client := NewClient(
		a.settings.QQOfficial.APIBaseURL,
		a.settings.QQOfficial.AppID,
		a.settings.QQOfficial.AppSecret,
	)
	runCtx, cancel := context.WithCancel(context.Background())
	transport := NewTransport(
		client,
		a.settings.Advanced.ReconnectDelaySec,
		a.onPayload,
		a.onReady,
		a.onUnready,
	)
	a.client = client
	a.transport = transport
	a.cancel = cancel
	a.started = true
	a.dialed = true
	if a.identity != nil {
		a.identity.Start(runCtx, a.settings, func() bool {
			return a.runtime != nil && a.runtime.Ready()
		})
	}
	return transport.Start(runCtx)
}

// Stop 停止 WS 与 identity 循环
func (a *Adapter) Stop(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	return a.stopLocked(ctx)
}

func (a *Adapter) stopLocked(ctx context.Context) error {
	a.runtime.MarkNotReady()
	a.mu.Lock()
	cancel, transport := a.cancel, a.transport
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if transport != nil {
		if err := transport.Stop(ctx); err != nil {
			return err
		}
	}
	if a.identity != nil {
		a.identity.Stop()
	}
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.mu.Lock()
	client := a.client
	a.cancel, a.transport, a.client = nil, nil, nil
	a.started = false
	a.mu.Unlock()
	if client != nil {
		client.Close()
	}
	if a.hostHooks != nil && a.hostHooks.APIs != nil {
		a.hostHooks.APIs.RemoveAPIsByPlugin(PluginID)
	}
	return a.onUnready(ctx)
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
	routeAccount := ""
	if a.runtime != nil {
		routeAccount = a.runtime.RouteAccountID()
	}
	mapping := a.mapping
	union := a.union
	a.mu.Unlock()
	if client == nil || !ready {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "QQ 官方消息网关未就绪"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	if routeKey.Platform != "" && routeKey.Platform != PlatformQQ {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "出站消息的路由身份与 QQ 官方网关不一致"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	if routeKey.AccountID != "" && routeAccount != "" && routeKey.AccountID != routeAccount {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "出站消息的路由身份与 QQ 官方网关不一致"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	scene, target := outboundSceneTarget(outbound)
	config := outbound.MessageInfo.AdditionalConfig
	var openID string
	var err error
	app := strings.TrimSpace(a.settings.QQOfficial.AppID)
	groupOpenID := ""
	if scene == "group" {
		openID, err = mapping.resolveGroup(target, asString(config["qq_official_group_openid"]))
		groupOpenID = openID
	} else if openID = strings.TrimSpace(asString(config["qq_official_user_openid"])); openID == "" {
		openID, err = union.resolveUser(app, "", target, mapping.User)
	}
	if err != nil {
		receipt.Status, receipt.Error = platformio.DeliveryStatusFailed, err.Error()
		return receipt, err
	}
	legacy, _ := a.groupUserMapping(mapping, groupOpenID)
	parts, quote, err := encodeOutbound(ctx, outbound, func(user string) (string, error) {
		return union.resolveUser(app, groupOpenID, user, legacy.User)
	})
	if err != nil {
		receipt.Status, receipt.Error = platformio.DeliveryStatusFailed, err.Error()
		return receipt, err
	}
	scope := messageScope{account: a.runtime.AccountID(), scene: scene, target: openID}
	basePath := "/v2/users/" + url.PathEscape(openID)
	if scene == "group" {
		basePath = "/v2/groups/" + url.PathEscape(openID)
	}
	sent := 0
	fail := func(err error) (platformio.DeliveryReceipt, error) {
		if sent > 0 {
			err = fmt.Errorf("已发送 %d 条后失败: %w", sent, err)
		}
		receipt.Status, receipt.Error = platformio.DeliveryStatusFailed, err.Error()
		return receipt, err
	}
	for i, part := range parts {
		body := part.body
		if part.fileType != 0 {
			if part.fileRef != "" {
				if a.hostHooks == nil || a.hostHooks.ResolveFile == nil {
					return fail(fmt.Errorf("出站附件缺少宿主下载票据"))
				}
				address, err := a.hostHooks.ResolveFile(ctx, part.fileRef, outbound.SessionID)
				if err != nil {
					return fail(err)
				}
				part.binary, err = client.downloadFile(ctx, address, part.size)
				if err != nil {
					return fail(err)
				}
			}
			info, err := client.uploadMedia(ctx, basePath, part)
			if err != nil {
				return fail(err)
			}
			body = map[string]any{"msg_type": 7, "media": map[string]any{"file_info": info}}
		}
		if i == 0 && quote != "" && body["msg_type"] != 2 {
			if index := a.references.reference(scopedMessage{scope, quote}); index != "" {
				body["message_reference"] = map[string]any{"message_id": index}
			}
		}
		for key, value := range a.references.sendFields(scopedMessage{scope, outbound.ExternalReplyToMessageID}) {
			body[key] = value
		}
		result, err := client.APIPost(ctx, basePath+"/messages", body)
		if err != nil {
			return fail(err)
		}
		id := asString(result["id"])
		if id == "" {
			return fail(fmt.Errorf("QQ 发送失败: 响应缺少消息 ID"))
		}
		ext, _ := asMap(result["ext_info"])
		a.references.recordSent(scopedMessage{scope, id}, asString(ext["ref_idx"]))
		if sent == 0 {
			receipt.ExternalMessageID = id
		}
		sent++
	}
	receipt.Status = platformio.DeliveryStatusSent
	return receipt, nil
}

// Dialed 是否已进入拨号（enabled=false 时为 false）
func (a *Adapter) Dialed() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.transport != nil && a.transport.Dialed() {
		return true
	}
	return a.dialed && a.settings.Plugin.Enabled
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
	settings := a.settings
	settings.QQOfficial.EnableMapping = a.mappingEnabled.Load()
	return settings
}

func (a *Adapter) onReady(ctx context.Context, accountID, botName, sessionID string) error {
	_ = sessionID
	a.mu.Lock()
	unified := a.settings.QQOfficial.UnifiedAccountID
	if accountID == "" && a.runtime != nil {
		accountID = a.runtime.AccountID()
	}
	if botName == "" && a.runtime != nil {
		botName = a.runtime.BotName()
	}
	routeAccount := unified
	if routeAccount == "" {
		routeAccount = accountID
	}
	a.desc = platformio.BuildNativeAdapterDescriptor(
		PluginID, GatewayName, PlatformQQ, routeAccount, "",
		map[string]any{
			"route_type": RouteTypeDuplex,
			"protocol":   ProtocolOfficial,
			"connection": ProtocolOfficial,
		},
	)
	identity := a.identity
	settings := a.settings
	hooks := a.hostHooks
	adapter := a
	a.mu.Unlock()

	if settings.QQOfficial.SnowlumaIdentityOnly && identity != nil {
		if err := identity.Renew(ctx, settings, true, true); err != nil {
			return fmt.Errorf("QQ 官方网关等待 SL 身份查询模式就绪: %w", err)
		}
	}
	if hooks != nil && hooks.RegisterDriver != nil {
		if err := hooks.RegisterDriver(ctx, adapter); err != nil {
			return err
		}
	}
	a.runtime.MarkReady(accountID, botName, unified)
	if hooks != nil && hooks.ReportGatewayState != nil {
		if err := hooks.ReportGatewayState(ctx, adapter.runtime); err != nil {
			a.runtime.MarkNotReady()
			return err
		}
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

func (a *Adapter) onPayload(ctx context.Context, op int, event string, data map[string]any, seq *int) error {
	_ = op
	_ = seq
	if a == nil || !isSupportedEvent(event) || data == nil {
		return nil
	}
	if !a.runtime.InboundReady() {
		return nil
	}
	if a.settings.Filter.IgnoreOtherBots && IsBotAuthor(data) {
		return nil
	}
	accountID := a.runtime.AccountID()
	botName := a.runtime.BotName()
	unified := a.settings.QQOfficial.UnifiedAccountID
	a.mu.Lock()
	client := a.client
	mapping := a.mapping
	syncIDs := a.syncIDs
	a.mu.Unlock()
	var attachmentSegments []map[string]any
	var attachmentLabels []string
	var picture, emoji bool
	var groupName string
	var groupOpenID string
	if isGroupEvent(event) {
		groupOpenID = strings.TrimSpace(asString(data["group_openid"]))
		groupName = strings.TrimSpace(asString(data["group_name"]))
		if client != nil && groupName == "" && groupOpenID != "" {
			groupName = a.fetchGroupName(ctx, client, groupOpenID)
		}
	}
	if client != nil {
		attachmentSegments, attachmentLabels, picture, emoji = client.inboundAttachments(ctx, data)
	}
	legacy, profiles := a.groupUserMapping(mapping, groupOpenID)
	effective := idMapping{}
	if a.mappingEnabled.Load() {
		effective = legacy
	}
	message, scene, _, err := decodeMessage(event, data, accountID, botName, unified, groupName, attachmentSegments, attachmentLabels, effective, picture, emoji)
	if err != nil {
		zap.L().Info("忽略 QQ 官方消息", zap.Error(err))
		return nil
	}
	info, _ := asMap(message["message_info"])
	config, _ := asMap(info["additional_config"])
	author, _ := asMap(data["author"])
	openid := asString(config["qq_official_user_openid"])
	if err := a.observeUnionIdentity(groupOpenID, openid, author, legacy.User[openid], mapping.User[openid] != ""); err != nil {
		zap.L().Warn("QQ 官方消息身份错误，未投递", zap.Error(err))
		return nil
	}
	if binding := mapping.Group[groupOpenID]; a.settings.QQOfficial.SilentUserSync && groupOpenID != "" && isDecimal(unified) && isDecimal(binding) {
		scope := identityScope{strings.TrimSpace(a.settings.QQOfficial.AppID), unified, groupOpenID, binding, openid}
		syncIDs.observe(scope, message)
		profile, exists := profiles[openid]
		userInfo, _ := asMap(info["user_info"])
		if exists && !profile.Conflict && (mapping.User[openid] == "" || mapping.User[openid] == profile.UserID) {
			if profile.Nickname != "" && strings.TrimSpace(asString(author["username"])) == "" {
				userInfo["user_nickname"] = profile.Nickname
			}
			if _, exists := author["member_nickname"]; !exists {
				userInfo["user_cardname"] = nilIfEmpty(profile.Cardname)
			}
		}
	}
	var sink InboundSink
	if a.hostHooks != nil {
		sink = a.hostHooks.Inbound
	}
	message["is_picture"], message["is_emoji"] = picture, emoji
	key := scopedMessage{messageScope{account: accountID, scene: scene, target: firstNonEmpty(groupOpenID, openid)}, asString(message["message_id"])}
	stamp, _ := strconv.ParseFloat(asString(message["timestamp"]), 64)
	idx, replyIdx := extractMessageSceneFields(data)
	quotedID, inserted := a.references.recordInbound(key, time.UnixMicro(int64(stamp*1e6)), idx, replyIdx)
	if quotedID != "" {
		body, _ := message["raw_message"].([]map[string]any)
		message["raw_message"] = append([]map[string]any{{"type": "reply", "data": map[string]any{"target_message_id": quotedID}}}, body...)
	}
	accepted, err := deliverInbound(ctx, sink, a.runtime.RouteAccountID(), message)
	if inserted && (!accepted || err != nil) {
		a.references.discardPassive(key)
	}
	return err
}

type groupNameEntry struct {
	name string
	time time.Time
}

func (a *Adapter) fetchGroupName(ctx context.Context, client *Client, openID string) string {
	if cached, ok := a.groupNames[openID]; ok && time.Since(cached.time) < time.Hour {
		return cached.name
	}
	query, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	info, err := client.apiRequest(query, http.MethodGet, "/v2/groups/"+url.PathEscape(openID)+"/info", nil, 0)
	name := strings.TrimSpace(asString(info["group_name"]))
	if err != nil {
		zap.L().Info("获取 QQ 群名称失败，保留已有群名", zap.String("group_openid", openID), zap.Error(err))
	}
	now := time.Now()
	var oldestID string
	var oldest time.Time
	for id, cached := range a.groupNames {
		if now.Sub(cached.time) >= time.Hour {
			delete(a.groupNames, id)
		} else if oldest.IsZero() || cached.time.Before(oldest) {
			oldestID, oldest = id, cached.time
		}
	}
	if len(a.groupNames) >= 2000 {
		delete(a.groupNames, oldestID)
	}
	a.groupNames[openID] = groupNameEntry{name: name, time: now}
	return name
}
