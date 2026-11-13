package hostports

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"maibot-go/internal/platform_io"
	"maibot-go/internal/plugin_runtime"
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
	batch := g.manager.SendMessage(ctx, platformio.OutboundMessageEnvelope{
		RouteKey: route,
		ChatType: kind,
		TargetID: targetID,
		Segments: segments,
	}, route)
	return batch.HasSuccess(), nil
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
	raw, ok := message["segments"]
	if !ok || raw == nil {
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
