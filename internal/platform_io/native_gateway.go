package platformio

import (
	"context"
	"fmt"
	"strings"
)

// NativeGatewaySpec 进程内原生消息网关注册声明
// Descriptor 强制 Kind=plugin + plugin_type=adapter，对齐 WebUI 过滤
type NativeGatewaySpec struct {
	PluginID        string
	GatewayName     string
	ComponentName   string
	Platform        string
	AccountID       string
	Scope           string
	RouteType       string
	Protocol        string
	SupportsSend    bool
	SupportsReceive bool
	Priority        int
	Metadata        map[string]any
	Adapter         Adapter
}

type nativeGatewayDriver struct {
	Adapter
}

func (*nativeGatewayDriver) Start(context.Context) error { return nil }
func (*nativeGatewayDriver) Stop(context.Context) error  { return nil }

func (d *nativeGatewayDriver) ClearOnMessage() {
	if adapter, ok := d.Adapter.(interface{ ClearOnMessage() }); ok {
		adapter.ClearOnMessage()
	}
}

// RegisterNativeGatewayDriver 注册原生 Adapter 并 BindSend/BindReceive
// 对齐 RegisterPluginGatewayDriver（plugin_gateway_broker.go:64-84）；独立 Adapter.Send
func (m *Manager) RegisterNativeGatewayDriver(ctx context.Context, spec NativeGatewaySpec) error {
	if m == nil {
		return fmt.Errorf("Platform IO 未初始化")
	}
	spec = spec.normalized()
	if err := spec.validate(); err != nil {
		return err
	}
	driverID := BuildPluginGatewayDriverID(spec.PluginID, spec.GatewayName)
	if err := m.unregisterPluginGatewayDriverByID(ctx, driverID); err != nil {
		return fmt.Errorf("注销旧原生消息网关驱动 %s 失败: %w", driverID, err)
	}
	if err := m.AddDriver(ctx, &nativeGatewayDriver{Adapter: spec.Adapter}); err != nil {
		if cleanupErr := m.unregisterPluginGatewayDriverByID(ctx, driverID); cleanupErr != nil {
			return fmt.Errorf("注册原生消息网关驱动 %s 失败: %w（回滚失败: %v）", driverID, err, cleanupErr)
		}
		return fmt.Errorf("注册原生消息网关驱动 %s 失败: %w", driverID, err)
	}
	binding := RouteBinding{
		RouteKey: RouteKey{
			Platform:  spec.Platform,
			AccountID: spec.AccountID,
			Scope:     spec.Scope,
		},
		DriverID:   driverID,
		DriverKind: DriverKindPlugin,
		Priority:   spec.Priority,
		Metadata:   spec.bindingMetadata(),
	}
	if spec.SupportsSend {
		if err := m.BindSendRoute(binding); err != nil {
			return m.rollbackPluginGatewayRegistration(ctx, driverID, fmt.Errorf("绑定原生消息网关发送路由失败: %w", err))
		}
	}
	if spec.SupportsReceive {
		if err := m.BindReceiveRoute(binding); err != nil {
			return m.rollbackPluginGatewayRegistration(ctx, driverID, fmt.Errorf("绑定原生消息网关接收路由失败: %w", err))
		}
	}
	return nil
}

func (s NativeGatewaySpec) normalized() NativeGatewaySpec {
	s.PluginID = strings.TrimSpace(s.PluginID)
	s.GatewayName = strings.TrimSpace(s.GatewayName)
	s.ComponentName = strings.TrimSpace(s.ComponentName)
	if s.ComponentName == "" {
		s.ComponentName = s.GatewayName
	}
	s.Platform = strings.TrimSpace(s.Platform)
	s.AccountID = strings.TrimSpace(s.AccountID)
	s.Scope = strings.TrimSpace(s.Scope)
	s.RouteType = strings.TrimSpace(s.RouteType)
	s.Protocol = strings.TrimSpace(s.Protocol)
	route := strings.ToLower(s.RouteType)
	if !s.SupportsSend && (route == "send" || route == "duplex") {
		s.SupportsSend = true
	}
	if !s.SupportsReceive && (route == "receive" || route == "duplex") {
		s.SupportsReceive = true
	}
	return s
}

func (s NativeGatewaySpec) validate() error {
	if s.PluginID == "" {
		return errPluginGatewayPluginIDRequired
	}
	if s.GatewayName == "" {
		return errPluginGatewayNameRequired
	}
	if s.Platform == "" {
		return fmt.Errorf("%w: %s", errPluginGatewayPlatformRequired, s.GatewayName)
	}
	if s.Adapter == nil {
		return fmt.Errorf("原生消息网关 %s 未提供 Adapter", s.GatewayName)
	}
	desc := s.Adapter.Descriptor()
	wantID := BuildPluginGatewayDriverID(s.PluginID, s.GatewayName)
	if strings.TrimSpace(desc.DriverID) != wantID {
		return fmt.Errorf("原生 Adapter DriverID 必须为 %s，实际为 %s", wantID, desc.DriverID)
	}
	if desc.Kind != DriverKindPlugin {
		return fmt.Errorf("原生 Adapter Kind 必须为 plugin")
	}
	if pluginType, _ := desc.Metadata["plugin_type"].(string); strings.TrimSpace(pluginType) != "adapter" {
		return fmt.Errorf("原生 Adapter Metadata.plugin_type 必须为 adapter")
	}
	return nil
}

func (s NativeGatewaySpec) bindingMetadata() map[string]any {
	metadata := cloneStringAnyMap(s.Metadata)
	if metadata == nil {
		metadata = make(map[string]any, 8)
	}
	metadata["route_type"] = s.RouteType
	metadata["plugin_type"] = "adapter"
	metadata["protocol"] = s.Protocol
	metadata["plugin_id"] = s.PluginID
	metadata["gateway_name"] = s.GatewayName
	metadata["component_name"] = s.ComponentName
	metadata["supports_send"] = s.SupportsSend
	metadata["supports_receive"] = s.SupportsReceive
	metadata["source"] = "native_message_gateway"
	return metadata
}

// BuildNativeAdapterDescriptor 构造原生网关 Descriptor（Kind=plugin + plugin_type=adapter）
func BuildNativeAdapterDescriptor(pluginID, gatewayName, platform, accountID, scope string, metadata map[string]any) DriverDescriptor {
	meta := cloneStringAnyMap(metadata)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["plugin_type"] = "adapter"
	meta["source"] = "native_message_gateway"
	meta["gateway_name"] = strings.TrimSpace(gatewayName)
	return DriverDescriptor{
		DriverID:  BuildPluginGatewayDriverID(pluginID, gatewayName),
		Kind:      DriverKindPlugin,
		Platform:  strings.TrimSpace(platform),
		AccountID: strings.TrimSpace(accountID),
		Scope:     strings.TrimSpace(scope),
		PluginID:  strings.TrimSpace(pluginID),
		Metadata:  meta,
	}
}
