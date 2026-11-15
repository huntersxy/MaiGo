package hostports

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"MaiGo/internal/platform_io"
	"MaiGo/internal/plugin_runtime"
)

// Gateway 消息网关端口，出站走 Platform IO
type Gateway struct {
	manager *platformio.Manager
	sender  *Sender

	mu           sync.Mutex
	inbound      func(ctx context.Context, message map[string]any) error
	fileResolver platformio.PluginFileResolver
}

// NewGateway 构造消息网关端口
func NewGateway(manager *platformio.Manager, sender *Sender) *Gateway {
	return &Gateway{manager: manager, sender: sender}
}

// SetFileResolver 注入附件解析器，出站 file 段的 file_ref 会解析为可下载 URL
// 对齐 Python plugin_driver.py:113-126 的 get_file_service().download_url
func (g *Gateway) SetFileResolver(resolver platformio.PluginFileResolver) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.fileResolver = resolver
	g.mu.Unlock()
}

// SetInboundHandler 注入外部消息入站处理器（由 Agent 在启动后接线）
func (g *Gateway) SetInboundHandler(handler func(ctx context.Context, message map[string]any) error) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.inbound = handler
	g.mu.Unlock()
}

// ReceiveGatewayMessage 插件消息网关口入站注入
// 先执行适配器名单策略与去重，再交给入站处理器，对齐 Python _handle_route_message
func (g *Gateway) ReceiveGatewayMessage(ctx context.Context, request pluginruntime.PluginGatewayInboundRequest, message map[string]any) (bool, map[string]any, error) {
	routeKey := map[string]any{
		"platform":   request.Platform,
		"account_id": request.AccountID,
		"scope":      request.Scope,
	}
	if g == nil {
		return false, routeKey, fmt.Errorf("消息网关未接线")
	}
	chatType, targetID := policyTarget(message)
	if policyManager, err := platformio.GetAdapterPolicyManager(); err == nil && policyManager != nil {
		result, evalErr := policyManager.Evaluate(platformio.AdapterIdentity{
			AdapterID:   request.DriverID,
			PluginID:    request.PluginID,
			GatewayName: request.GatewayName,
			Platform:    request.Platform,
			AccountID:   request.AccountID,
			Scope:       request.Scope,
		}, chatType, targetID)
		if evalErr != nil {
			return false, routeKey, fmt.Errorf("适配器策略检查失败: %w", evalErr)
		}
		if result.Configured && !result.Allowed {
			routeKey["policy"] = result.ToMap()
			return false, routeKey, nil
		}
	}
	if g.manager != nil && g.manager.Dedupe() != nil {
		rawKey := firstNonEmpty(request.DedupeKey, request.ExternalMessageID, asString(message["message_id"]))
		if rawKey != "" && !g.manager.Dedupe().MarkSeen(request.DriverID+":"+rawKey) {
			return false, routeKey, nil
		}
	}
	mergeRouteMetadata(message, request.RouteMetadata)
	if err := g.ReceiveExternalMessage(ctx, message); err != nil {
		return false, routeKey, err
	}
	return true, routeKey, nil
}

// policyTarget 返回适配器名单判定使用的会话类型与目标 ID
func policyTarget(message map[string]any) (string, string) {
	if message == nil {
		return "private", ""
	}
	groupID := strings.TrimSpace(asString(message["group_id"]))
	if groupID == "" {
		if info, ok := message["message_info"].(map[string]any); ok {
			if group, ok := info["group_info"].(map[string]any); ok {
				groupID = strings.TrimSpace(asString(group["group_id"]))
			}
		}
	}
	if groupID != "" {
		return "group", groupID
	}
	userID := strings.TrimSpace(asString(message["user_id"]))
	if userID == "" {
		userInfo, _ := message["user_info"].(map[string]any)
		if userInfo == nil {
			if info, ok := message["message_info"].(map[string]any); ok {
				userInfo, _ = info["user_info"].(map[string]any)
			}
		}
		if userInfo != nil {
			userID = strings.TrimSpace(asString(userInfo["user_id"]))
		}
	}
	return "private", userID
}

// mergeRouteMetadata 把插件补充的路由元数据并入 additional_config
// 路由归属字段由 Host 统一写入正式字段，不再重复透传
func mergeRouteMetadata(message map[string]any, metadata map[string]any) {
	if len(metadata) == 0 {
		return
	}
	info, ok := message["message_info"].(map[string]any)
	if !ok {
		info = map[string]any{}
		message["message_info"] = info
	}
	config, ok := info["additional_config"].(map[string]any)
	if !ok {
		config = map[string]any{}
		info["additional_config"] = config
	}
	for key, value := range metadata {
		switch key {
		case "platform", "account_id", "scope", "self_id", "bot_account",
			"platform_io_account_id", "route_scope", "adapter_scope", "connection_id",
			"platform_io_scope":
			continue
		}
		if value == nil {
			continue
		}
		if strings.TrimSpace(asString(value)) == "" {
			continue
		}
		config[key] = value
	}
}

// ReceiveExternalMessage 把外部平台消息交给 Agent 会话调度
func (g *Gateway) ReceiveExternalMessage(ctx context.Context, message map[string]any) error {
	if g == nil {
		return fmt.Errorf("消息网关未接线")
	}
	g.mu.Lock()
	handler := g.inbound
	g.mu.Unlock()
	if handler == nil {
		return fmt.Errorf("消息网关入站未接线：Agent 会话调度尚未注册处理器")
	}
	return handler(ctx, message)
}

// SendMessageToExternal 向外部平台发送消息
func (g *Gateway) SendMessageToExternal(ctx context.Context, message map[string]any, _ bool) (bool, error) {
	if g == nil || g.manager == nil {
		return false, fmt.Errorf("发送端口未接线：Platform IO 未初始化")
	}
	platform := firstNonEmpty(asString(message["platform"]), "qq")
	chatType := strings.ToLower(firstNonEmpty(asString(message["chat_type"]), asString(message["message_type"])))
	targetID := firstNonEmpty(asString(message["target_id"]), asString(message["group_id"]), asString(message["user_id"]))
	if targetID == "" {
		return false, fmt.Errorf("消息缺少 target_id")
	}
	route := platformio.RouteKey{
		Platform:  platform,
		AccountID: asString(message["account_id"]),
		Scope:     asString(message["scope"]),
	}
	kind := platformio.ChatTypePrivate
	if chatType == "group" || asString(message["group_id"]) != "" {
		kind = platformio.ChatTypeGroup
	}
	segments, err := g.segments(message)
	if err != nil {
		return false, err
	}
	envelope := platformio.OutboundMessageEnvelope{
		InternalMessageID: firstNonEmpty(asString(message["message_id"]), asString(message["internal_message_id"])),
		RouteKey:          route,
		ChatType:          kind,
		TargetID:          targetID,
		Segments:          segments,
		PlainText:         firstNonEmpty(asString(message["content"]), asString(message["processed_plain_text"])),
		SessionID:         asString(message["session_id"]),
		MessageInfo:       pluginOutboundMessageInfo(message),
	}
	if timestamp := pluginTimestamp(message["timestamp"]); !timestamp.IsZero() {
		envelope.Timestamp = timestamp
	}
	if replyTo := asString(message["reply_to"]); replyTo != "" {
		envelope.ReplyToMessageID = replyTo
	}
	batch := g.manager.SendMessage(ctx, envelope, route)
	return batch.HasSuccess(), nil
}

// pluginOutboundMessageInfo 从插件消息字典透传出站 message_info
func pluginOutboundMessageInfo(message map[string]any) platformio.OutboundMessageInfo {
	info, _ := message["message_info"].(map[string]any)
	if info == nil {
		return platformio.OutboundMessageInfo{}
	}
	out := platformio.OutboundMessageInfo{AdditionalConfig: mapValue(info["additional_config"])}
	if user, ok := info["user_info"].(map[string]any); ok {
		out.UserInfo = platformio.OutboundUserInfo{
			UserID:       asString(user["user_id"]),
			UserNickname: asString(user["user_nickname"]),
			UserCardname: asString(user["user_cardname"]),
		}
	}
	if group, ok := info["group_info"].(map[string]any); ok {
		out.GroupInfo = &platformio.OutboundGroupInfo{
			GroupID:   asString(group["group_id"]),
			GroupName: asString(group["group_name"]),
		}
	}
	return out
}

func mapValue(value any) map[string]any {
	if mapped, ok := value.(map[string]any); ok {
		return mapped
	}
	return nil
}

// pluginTimestamp 解析插件消息字典的秒级时间戳
func pluginTimestamp(value any) time.Time {
	switch typed := value.(type) {
	case float64:
		return time.UnixMilli(int64(typed * 1000))
	case int64:
		return time.UnixMilli(typed * 1000)
	case int:
		return time.UnixMilli(int64(typed) * 1000)
	case string:
		var seconds float64
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%f", &seconds); err == nil && seconds > 0 {
			return time.UnixMilli(int64(seconds * 1000))
		}
	}
	return time.Time{}
}

// RegisterPluginGatewayDriver 注册插件消息网关驱动并绑定 Platform IO 路由
func (g *Gateway) RegisterPluginGatewayDriver(ctx context.Context, spec pluginruntime.PluginGatewayDriverSpec) error {
	if g == nil || g.manager == nil {
		return fmt.Errorf("Platform IO 未初始化，无法注册插件消息网关驱动")
	}
	g.mu.Lock()
	resolver := g.fileResolver
	g.mu.Unlock()
	driver, err := g.manager.RegisterPluginGatewayDriver(ctx, platformio.PluginGatewaySpec{
		PluginID:        spec.PluginID,
		GatewayName:     spec.GatewayName,
		ComponentName:   spec.ComponentName,
		Platform:        spec.Platform,
		AccountID:       spec.AccountID,
		Scope:           spec.Scope,
		RouteType:       spec.RouteType,
		PluginType:      spec.PluginType,
		Protocol:        spec.Protocol,
		SupportsSend:    spec.SupportsSend,
		SupportsReceive: spec.SupportsReceive,
		Priority:        spec.Priority,
		TimeoutMS:       spec.TimeoutMS,
		Metadata:        spec.Metadata,
	})
	if err != nil {
		return fmt.Errorf("注册插件消息网关驱动失败: %w", err)
	}
	if driver != nil && resolver != nil {
		driver.SetFileResolver(resolver)
	}
	return nil
}

// UnregisterPluginGatewayDriver 注销插件消息网关驱动并清理 Platform IO 路由绑定
func (g *Gateway) UnregisterPluginGatewayDriver(ctx context.Context, pluginID, gatewayName string) error {
	if g == nil || g.manager == nil {
		return fmt.Errorf("Platform IO 未初始化，无法注销插件消息网关驱动")
	}
	if err := g.manager.UnregisterPluginGatewayDriver(ctx, pluginID, gatewayName); err != nil {
		return fmt.Errorf("注销插件消息网关驱动失败: %w", err)
	}
	return nil
}

func (g *Gateway) segments(message map[string]any) ([]platformio.Segment, error) {
	raw := message["segments"]
	if raw == nil {
		raw = message["raw_message"]
	}
	if raw == nil {
		raw = message["message"]
	}
	if text, ok := raw.(string); ok {
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("消息内容为空")
		}
		return []platformio.Segment{{Kind: platformio.SegmentText, Text: text}}, nil
	}
	nodes, ok := raw.([]map[string]any)
	if !ok {
		if items, listOK := raw.([]any); listOK {
			nodes = make([]map[string]any, 0, len(items))
			for _, item := range items {
				if node, mapOK := item.(map[string]any); mapOK {
					nodes = append(nodes, node)
				}
			}
		}
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("消息缺少 segments")
	}
	return g.sender.segmentsFromMaps(nodes)
}

var _ pluginruntime.PluginMessageGatewayPort = (*Gateway)(nil)
