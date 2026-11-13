package platformio

import "errors"

var (
	errPluginGatewayPluginIDRequired = errors.New("插件消息网关驱动缺少 plugin_id")
	errPluginGatewayNameRequired     = errors.New("插件消息网关驱动缺少 gateway_name")
	errPluginGatewayPlatformRequired = errors.New("插件消息网关未提供有效的平台名称")
	errPluginGatewayDriverIDRequired = errors.New("插件消息网关驱动缺少 driver_id")
	errPluginInboundDriverMismatch   = errors.New("入站消息的 driver_id 与插件消息网关驱动不一致")
	errPluginInboundKindMismatch     = errors.New("入站消息的 driver_kind 不是 plugin")
	errPluginInboundPlatformMismatch = errors.New("入站消息的 platform 与插件消息网关驱动不一致")
	errPluginInboundRouteMismatch    = errors.New("入站消息的路由信息与插件消息网关驱动冲突")
)
