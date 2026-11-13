package platformio

import (
	"context"
	"fmt"
	"strings"
)

// PluginGatewaySpec 插件消息网关在 Platform IO 的注册声明
// 对齐 MaiBot Supervisor._register_message_gateway_driver
type PluginGatewaySpec struct {
	PluginID        string
	GatewayName     string
	ComponentName   string
	Platform        string
	AccountID       string
	Scope           string
	RouteType       string
	PluginType      string
	Protocol        string
	SupportsSend    bool
	SupportsReceive bool
	Priority        int
	TimeoutMS       int64
	Metadata        map[string]any
}

// BuildPluginGatewayDriverID 构造插件消息网关驱动 ID，对齐 Python gateway:<plugin_id>:<gateway_name>
func BuildPluginGatewayDriverID(pluginID, gatewayName string) string {
	return "gateway:" + strings.TrimSpace(pluginID) + ":" + strings.TrimSpace(gatewayName)
}

// RegisterPluginGatewayDriver 注册或替换插件消息网关驱动并绑定发送/接收路由
// 已存在同 ID 驱动时先注销旧驱动，注册或绑定失败会回滚已注册的驱动
func (m *Manager) RegisterPluginGatewayDriver(ctx context.Context, spec PluginGatewaySpec) (*PluginPlatformDriver, error) {
	spec = spec.normalized()
	if err := spec.validate(); err != nil {
		return nil, err
	}
	driverID := BuildPluginGatewayDriverID(spec.PluginID, spec.GatewayName)
	if err := m.unregisterPluginGatewayDriverByID(ctx, driverID); err != nil {
		return nil, fmt.Errorf("注销旧插件消息网关驱动 %s 失败: %w", driverID, err)
	}

	driver := NewPluginPlatformDriver(PluginDriverOptions{
		DriverID:      driverID,
		Platform:      spec.Platform,
		AccountID:     spec.AccountID,
		Scope:         spec.Scope,
		PluginID:      spec.PluginID,
		ComponentName: spec.ComponentName,
		GatewayName:   spec.GatewayName,
		SupportsSend:  spec.SupportsSend,
		TimeoutMS:     spec.TimeoutMS,
		Metadata:      spec.driverMetadata(),
	})
	if err := m.AddDriver(ctx, driver); err != nil {
		if cleanupErr := m.unregisterPluginGatewayDriverByID(ctx, driverID); cleanupErr != nil {
			return nil, fmt.Errorf("注册插件消息网关驱动 %s 失败: %w（回滚失败: %v）", driverID, err, cleanupErr)
		}
		return nil, fmt.Errorf("注册插件消息网关驱动 %s 失败: %w", driverID, err)
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
			return nil, m.rollbackPluginGatewayRegistration(ctx, driverID, fmt.Errorf("绑定插件消息网关发送路由失败: %w", err))
		}
	}
	if spec.SupportsReceive {
		if err := m.BindReceiveRoute(binding); err != nil {
			return nil, m.rollbackPluginGatewayRegistration(ctx, driverID, fmt.Errorf("绑定插件消息网关接收路由失败: %w", err))
		}
	}
	return driver, nil
}

// UnregisterPluginGatewayDriver 注销插件消息网关驱动并清理发送/接收路由绑定
func (m *Manager) UnregisterPluginGatewayDriver(ctx context.Context, pluginID, gatewayName string) error {
	pluginID = strings.TrimSpace(pluginID)
	gatewayName = strings.TrimSpace(gatewayName)
	if pluginID == "" {
		return errPluginGatewayPluginIDRequired
	}
	if gatewayName == "" {
		return errPluginGatewayNameRequired
	}
	return m.unregisterPluginGatewayDriverByID(ctx, BuildPluginGatewayDriverID(pluginID, gatewayName))
}

// PluginGatewayDriver 按插件与网关名查询已注册的插件消息网关驱动
func (m *Manager) PluginGatewayDriver(pluginID, gatewayName string) *PluginPlatformDriver {
	m.mu.RLock()
	defer m.mu.RUnlock()
	driver, _ := m.registry.Get(BuildPluginGatewayDriverID(pluginID, gatewayName)).(*PluginPlatformDriver)
	return driver
}

// PluginGatewayDrivers 列出指定插件的全部插件消息网关驱动；pluginID 为空时列出全部
func (m *Manager) PluginGatewayDrivers(pluginID string) []*PluginPlatformDriver {
	pluginID = strings.TrimSpace(pluginID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	adapters := m.registry.List(nil, "")
	out := make([]*PluginPlatformDriver, 0, len(adapters))
	for _, adapter := range adapters {
		driver, ok := adapter.(*PluginPlatformDriver)
		if !ok {
			continue
		}
		if pluginID != "" && driver.Descriptor().PluginID != pluginID {
			continue
		}
		out = append(out, driver)
	}
	return out
}

func (m *Manager) rollbackPluginGatewayRegistration(ctx context.Context, driverID string, cause error) error {
	if cleanupErr := m.unregisterPluginGatewayDriverByID(ctx, driverID); cleanupErr != nil {
		return fmt.Errorf("%w（回滚失败: %v）", cause, cleanupErr)
	}
	return cause
}

func (m *Manager) unregisterPluginGatewayDriverByID(ctx context.Context, driverID string) error {
	m.mu.Lock()
	m.sendRouteTable.RemoveBindingsByDriver(driverID)
	m.receiveRouteTable.RemoveBindingsByDriver(driverID)
	adapter := m.registry.Get(driverID)
	running := m.started
	m.mu.Unlock()
	if adapter == nil {
		return nil
	}
	if running {
		_, err := m.RemoveDriver(ctx, driverID)
		return err
	}
	return m.UnregisterChecked(driverID)
}

func (s PluginGatewaySpec) normalized() PluginGatewaySpec {
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
	s.PluginType = strings.TrimSpace(s.PluginType)
	s.Protocol = strings.TrimSpace(s.Protocol)
	return s
}

func (s PluginGatewaySpec) validate() error {
	if s.PluginID == "" {
		return errPluginGatewayPluginIDRequired
	}
	if s.GatewayName == "" {
		return errPluginGatewayNameRequired
	}
	if s.Platform == "" {
		return fmt.Errorf("%w: %s", errPluginGatewayPlatformRequired, s.GatewayName)
	}
	return nil
}

func (s PluginGatewaySpec) driverMetadata() map[string]any {
	metadata := cloneStringAnyMap(s.Metadata)
	if metadata == nil {
		metadata = make(map[string]any, 3)
	}
	metadata["route_type"] = s.RouteType
	metadata["plugin_type"] = s.PluginType
	metadata["protocol"] = s.Protocol
	return metadata
}

func (s PluginGatewaySpec) bindingMetadata() map[string]any {
	metadata := s.driverMetadata()
	metadata["plugin_id"] = s.PluginID
	metadata["gateway_name"] = s.GatewayName
	metadata["component_name"] = s.ComponentName
	metadata["supports_send"] = s.SupportsSend
	metadata["supports_receive"] = s.SupportsReceive
	return metadata
}
