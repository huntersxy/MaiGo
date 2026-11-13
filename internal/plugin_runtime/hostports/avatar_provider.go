package hostports

import (
	"context"
	"fmt"
	"strings"

	"maibot-go/internal/platform_io"
	"maibot-go/internal/plugin_runtime"
)

// adapterAvatarProvider 对齐 avatar_provider.query_adapter_avatar：按 Platform IO 路由查插件适配器
const adapterAvatarAPIName = "adapter.avatar.get"
const adapterAvatarAPIVersion = "1"

type adapterAvatarProvider struct {
	manager *platformio.Manager
	invoke  func(ctx context.Context, pluginID string, args map[string]any) (map[string]any, error)
}

// NewAdapterAvatarProvider 供 WebUI / Sender 共用同一适配器头像查询实现
func NewAdapterAvatarProvider(manager *platformio.Manager) platformio.AvatarProvider {
	return &adapterAvatarProvider{manager: manager}
}

func (p *adapterAvatarProvider) QueryAvatar(ctx context.Context, platform, targetID, targetType, accountID, scope string) (map[string]any, error) {
	if p == nil || p.manager == nil {
		return avatarUnsupported(), nil
	}
	descriptor, err := p.resolveDriver(platform, accountID, scope)
	if err != nil {
		return nil, err
	}
	if descriptor == nil {
		return avatarUnsupported(), nil
	}
	invoke := p.invoke
	if invoke == nil {
		invoke = invokeAdapterAvatarAPI
	}
	result, err := invoke(ctx, descriptor.PluginID, map[string]any{
		"platform":    platform,
		"target_id":   targetID,
		"target_type": targetType,
		"account_id":  firstNonEmpty(accountID, descriptor.AccountID),
		"scope":       firstNonEmpty(scope, descriptor.Scope),
	})
	if err != nil {
		return nil, fmt.Errorf("适配器头像查询失败: %w", err)
	}
	if result == nil {
		return avatarUnsupported(), nil
	}
	return result, nil
}

// resolveDriver 对齐 Python query_adapter_avatar 的两段式解析
//
// 先按发送路由解析（legacy/local 驱动没有插件 API，不能遮蔽账号级头像提供方），
// 路由为空时再回退到「仅接收」的插件适配器列表；两段都只保留带 plugin_id 的驱动。
func (p *adapterAvatarProvider) resolveDriver(platform, accountID, scope string) (*platformio.DriverDescriptor, error) {
	platform = strings.TrimSpace(platform)
	accountID = strings.TrimSpace(accountID)
	scope = strings.TrimSpace(scope)
	if platform == "" {
		return nil, fmt.Errorf("头像平台不能为空")
	}
	route := platformio.RouteKey{Platform: platform, AccountID: accountID, Scope: scope}
	descriptors := make([]platformio.DriverDescriptor, 0)
	for _, adapter := range p.manager.ResolveSendAdapters(route) {
		if adapter == nil {
			continue
		}
		descriptor := adapter.Descriptor()
		if strings.TrimSpace(descriptor.PluginID) == "" {
			continue
		}
		descriptors = append(descriptors, descriptor)
	}
	if len(descriptors) == 0 {
		// 无平台级发送绑定时，仍允许仅接收的适配器提供头像
		for _, driver := range p.manager.PluginGatewayDrivers("") {
			if driver == nil {
				continue
			}
			descriptor := driver.Descriptor()
			if !strings.EqualFold(strings.TrimSpace(descriptor.Platform), platform) || strings.TrimSpace(descriptor.PluginID) == "" {
				continue
			}
			if accountID != "" && strings.TrimSpace(descriptor.AccountID) != accountID {
				continue
			}
			if scope != "" && strings.TrimSpace(descriptor.Scope) != scope {
				continue
			}
			descriptors = append(descriptors, descriptor)
		}
		pluginIDs := make(map[string]struct{}, len(descriptors))
		for _, descriptor := range descriptors {
			pluginIDs[descriptor.PluginID] = struct{}{}
		}
		if len(pluginIDs) > 1 {
			return nil, fmt.Errorf("头像路由存在多个适配器，请指定 account_id 和 scope")
		}
	}
	if len(descriptors) == 0 {
		return nil, nil
	}
	selected := descriptors[0]
	return &selected, nil
}

func invokeAdapterAvatarAPI(ctx context.Context, pluginID string, args map[string]any) (map[string]any, error) {
	host := pluginruntime.DefaultHost()
	if host == nil {
		return nil, fmt.Errorf("插件运行时未初始化")
	}
	entry := host.APIRegistry().GetAPI(pluginID, adapterAvatarAPIName, adapterAvatarAPIVersion, false)
	if entry == nil || !entry.Public {
		return nil, nil
	}
	if !entry.Enabled {
		return nil, fmt.Errorf("适配器头像 API 当前不可用: %s", entry.RegistryKey)
	}
	timeoutMS, err := pluginruntime.ResolveComponentRPCTimeoutMS(entry.TimeoutMS)
	if err != nil {
		return nil, err
	}
	envelope, err := host.InvokeAPI(ctx, pluginID, entry.HandlerName, args, timeoutMS)
	if err != nil {
		return nil, err
	}
	if len(envelope.Error) > 0 {
		message := asString(envelope.Error["message"])
		if message == "" {
			message = "适配器头像 API 调用失败"
		}
		return nil, fmt.Errorf("%s", message)
	}
	payload := envelope.Payload
	if len(payload) == 0 {
		return nil, fmt.Errorf("适配器头像查询返回空结果")
	}
	if success, ok := payload["success"].(bool); !ok || !success {
		return nil, fmt.Errorf("适配器头像查询返回无效结果: %v", payload)
	}
	result, ok := payload["result"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("适配器头像查询返回无效结果: %v", payload)
	}
	return result, nil
}

func avatarUnsupported() map[string]any {
	return map[string]any{"status": string(platformio.AvatarStatusUnsupported)}
}
