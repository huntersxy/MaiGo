package protocol

// RPC 方法名常量，对齐 Host rpc_server / supervisor 注册表（子集）
const (
	MethodRunnerHello    = "runner.hello"
	MethodRunnerReady    = "runner.ready"
	MethodRunnerLogBatch = "runner.log_batch"

	MethodHostPing = "host.ping"
	MethodPing     = "ping"

	MethodCapCall = "cap.call"

	MethodHostRouteMessage              = "host.route_message"
	MethodHostUpdateMessageGatewayState = "host.update_message_gateway_state"

	MethodPluginBootstrap          = "plugin.bootstrap"
	MethodPluginRegisterComponents = "plugin.register_components"
	MethodPluginRegisterPlugin     = "plugin.register_plugin"
	MethodPluginUnregister         = "plugin.unregister"
	MethodPluginHealth             = "plugin.health"
	MethodPluginInvokeHook         = "plugin.invoke_hook"
	MethodPluginEmitEvent          = "plugin.emit_event"
	MethodPluginConfigUpdated      = "plugin.config_updated"
	MethodPluginValidateConfig     = "plugin.validate_config"
	MethodPluginInspectConfig      = "plugin.inspect_config"
	MethodPluginDesktopPetButtons  = "plugin.desktop_pet_buttons"
	MethodPluginPrepareShutdown    = "plugin.prepare_shutdown"
	MethodPluginShutdown           = "plugin.shutdown"
	MethodPluginReload             = "plugin.reload"
	MethodPluginReloadBatch        = "plugin.reload_batch"
	MethodPluginUnloadBatch        = "plugin.unload_batch"
	MethodPluginRecover            = "plugin.recover"

	MethodCapPrefix = "cap."
)
