package host

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vmihailenco/msgpack/v5"
	"go.uber.org/zap"

	"maibot-go/internal/plugin_runtime/capabilities"
	"maibot-go/internal/plugin_runtime/protocol"
)

func decodePayload[T any](payload map[string]any) (T, error) {
	var out T
	if payload == nil {
		payload = map[string]any{}
	}
	b, err := msgpack.Marshal(payload)
	if err != nil {
		return out, err
	}
	err = msgpack.Unmarshal(b, &out)
	return out, err
}

func (s *RPCServer) dispatchRequest(ctx context.Context, env protocol.Envelope) protocol.Envelope {
	method := strings.TrimSpace(env.Method)
	switch method {
	case protocol.MethodCapCall:
		return s.handleCapCall(ctx, env)
	case protocol.MethodHostRouteMessage:
		return s.handleRouteMessage(ctx, env)
	case protocol.MethodHostUpdateMessageGatewayState:
		return s.handleUpdateMessageGatewayState(ctx, env)
	case protocol.MethodHostPing, protocol.MethodPing:
		return env.MakeResponse(map[string]any{"pong": true}, nil)
	case protocol.MethodPluginRegisterComponents, protocol.MethodPluginRegisterPlugin:
		return s.handleRegisterPlugin(ctx, env)
	case protocol.MethodPluginBootstrap:
		return s.handleBootstrapPlugin(env)
	case protocol.MethodPluginUnregister:
		return s.handleUnregisterPlugin(env)
	case protocol.MethodRunnerReady:
		return s.handleRunnerReady(env)
	default:
		return env.MakeErrorResponse(
			string(protocol.ErrorCodeMethodNotAllowed),
			"未注册的方法: "+env.Method,
			nil,
		)
	}
}

func (s *RPCServer) dispatchBroadcast(ctx context.Context, env protocol.Envelope) error {
	err := s.handleBroadcast(ctx, env)
	if err != nil {
		// 广播没有响应信封，失败必须写入 Host 日志，避免解码失败被静默丢弃
		zap.L().Warn("Runner 广播处理失败",
			zap.String("method", env.Method),
			zap.String("plugin_id", env.PluginID),
			zap.Error(err),
		)
	}
	return err
}

func (s *RPCServer) handleBroadcast(ctx context.Context, env protocol.Envelope) error {
	switch env.Method {
	case protocol.MethodRunnerLogBatch:
		entries, err := decodePayload[protocol.LogBatchPayload](env.Payload)
		if err != nil {
			return fmt.Errorf("runner.log_batch payload 非法: %w", err)
		}
		s.host.HandleRunnerLogs(env.PluginID, entries.Entries)
		return nil
	case protocol.MethodRunnerReady:
		_ = s.handleRunnerReady(env)
		return nil
	default:
		return nil
	}
}

// capabilityRPCError 将能力调用错误映射为 RPC 错误，透传 CapabilityError 的 code/message/details
func capabilityRPCError(err error) map[string]any {
	var capErr *CapabilityError
	if errors.As(err, &capErr) && capErr != nil {
		details := capErr.Details
		if details == nil {
			details = map[string]any{}
		}
		return map[string]any{"code": capErr.Code, "message": capErr.Message, "details": details}
	}
	return map[string]any{
		"code":    string(protocol.ErrorCodeCapabilityFailed),
		"message": err.Error(),
		"details": map[string]any{},
	}
}

func (s *RPCServer) handleCapCall(ctx context.Context, env protocol.Envelope) protocol.Envelope {
	req, err := decodePayload[protocol.CapabilityRequestPayload](env.Payload)
	if err != nil {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), "能力调用 payload 非法", nil)
	}
	capability := strings.TrimSpace(req.Capability)
	if capability == "" {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), "能力调用 payload 非法", nil)
	}
	args := req.Args
	if args == nil {
		args = map[string]any{}
	}
	if !capabilities.IsAlwaysAllowed(capability) {
		allowed, reason := s.host.Authorization().Check(env.PluginID, capability)
		if !allowed {
			return env.MakeErrorResponse(string(protocol.ErrorCodeCapabilityDenied), reason, map[string]any{
				"plugin_id":  env.PluginID,
				"capability": capability,
			})
		}
	}
	result, err := s.host.InvokeCapability(ctx, env.PluginID, capability, args)
	if err != nil {
		rpcErr := capabilityRPCError(err)
		details, _ := rpcErr["details"].(map[string]any)
		return env.MakeErrorResponse(stringValue(rpcErr["code"]), stringValue(rpcErr["message"]), details)
	}
	return env.MakeResponse(map[string]any{
		"success": true,
		"result":  result,
	}, nil)
}

func (s *RPCServer) handleRegisterPlugin(ctx context.Context, env protocol.Envelope) protocol.Envelope {
	reg, err := decodePayload[protocol.RegisterPluginPayload](env.Payload)
	if err != nil {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), "plugin.register payload 非法", nil)
	}
	pluginID := strings.TrimSpace(reg.PluginID)
	if pluginID == "" {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), "plugin_id 不能为空", nil)
	}
	buttons, err := NormalizeDesktopPetButtons(reg.DesktopPetButtons, reg.ConfigError)
	if err != nil {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), err.Error(), map[string]any{
			"plugin_id": pluginID,
		})
	}
	rec := PluginRecord{
		PluginID:                  pluginID,
		DisplayName:               reg.DisplayName,
		PluginVersion:             reg.PluginVersion,
		PluginType:                reg.PluginType,
		RunnerGroup:               s.RunnerGroup(),
		ConfigError:               reg.ConfigError,
		DefaultConfig:             reg.DefaultConfig,
		ConfigSchema:              reg.ConfigSchema,
		Capabilities:              reg.CapabilitiesRequired,
		ConfigReloadSubscriptions: reg.ConfigReloadSubscriptions,
		LLMProviders:              reg.LLMProviders,
		DesktopPetButtons:         buttons,
	}
	if registry := s.host.LLMProviderRegistry(); registry != nil {
		if err := registry.ValidatePluginProviders(pluginID, reg.LLMProviders); err != nil {
			return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), err.Error(), map[string]any{
				"plugin_id":          pluginID,
				"llm_provider_count": len(reg.LLMProviders),
			})
		}
	}
	// 先做无副作用的 WebUI 校验，再落地组件订阅；任一环节不合法则整体失败
	if _, err := ValidateWebUIExtension(reg.WebUI); err != nil {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), err.Error(), map[string]any{
			"plugin_id": pluginID,
		})
	}
	// 对齐 Python supervisor.py:1215-1226：WebUI 只能绑定本次注册中明确存在的静态 API
	if err := ValidateWebUIAPIBindings(reg.Components, reg.WebUI); err != nil {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), err.Error(), map[string]any{
			"plugin_id": pluginID,
		})
	}
	hookHandlers, err := s.host.ApplyPluginComponents(rec, reg.Components)
	if err != nil {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), err.Error(), map[string]any{
			"plugin_id":       pluginID,
			"component_count": len(reg.Components),
		})
	}
	if err := s.host.ApplyWebUIExtension(pluginID, reg.WebUI); err != nil {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), err.Error(), map[string]any{
			"plugin_id": pluginID,
		})
	}
	if err := s.host.UnregisterPluginGatewayDrivers(ctx, pluginID); err != nil {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), err.Error(), map[string]any{
			"plugin_id": pluginID,
		})
	}
	s.host.RegisterPlugin(rec)
	_ = ctx
	registeredAPIs := 0
	if registry := s.host.APIRegistry(); registry != nil {
		registeredAPIs = registry.CountByPlugin(pluginID)
	}
	llmProviders := 0
	if registry := s.host.LLMProviderRegistry(); registry != nil {
		llmProviders = registry.CountPlugin(pluginID)
	}
	return env.MakeResponse(map[string]any{
		"accepted":                 true,
		"plugin_id":                pluginID,
		"registered_components":    countRuntimeComponents(reg.Components),
		"registered_apis":          registeredAPIs,
		"message_gateways":         s.host.MessageGatewayCount(pluginID),
		"llm_providers":            llmProviders,
		"registered_hook_handlers": hookHandlers,
	}, nil)
}

func (s *RPCServer) handleUnregisterPlugin(env protocol.Envelope) protocol.Envelope {
	req, err := decodePayload[protocol.UnregisterPluginPayload](env.Payload)
	if err != nil {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), "plugin.unregister payload 非法", nil)
	}
	pluginID := strings.TrimSpace(req.PluginID)
	if pluginID == "" {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), "plugin_id 不能为空", nil)
	}
	removedAPIs := 0
	if registry := s.host.APIRegistry(); registry != nil {
		removedAPIs = registry.CountByPlugin(pluginID)
	}
	removedProviders := 0
	if registry := s.host.LLMProviderRegistry(); registry != nil {
		removedProviders = registry.CountPlugin(pluginID)
	}
	removedComponents, removedRegistration := s.host.UnregisterPlugin(pluginID)
	return env.MakeResponse(map[string]any{
		"accepted":              true,
		"plugin_id":             pluginID,
		"reason":                req.Reason,
		"removed_components":    removedComponents,
		"removed_apis":          removedAPIs,
		"removed_llm_providers": removedProviders,
		"removed_registration":  removedRegistration,
	}, nil)
}

func (s *RPCServer) handleBootstrapPlugin(env protocol.Envelope) protocol.Envelope {
	payload, err := decodePayload[protocol.BootstrapPluginPayload](env.Payload)
	if err != nil {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), "plugin.bootstrap payload 非法", nil)
	}
	pluginID := strings.TrimSpace(payload.PluginID)
	if pluginID == "" {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), "plugin_id 不能为空", nil)
	}
	capabilities := make([]string, 0, len(payload.CapabilitiesRequired))
	for _, capability := range payload.CapabilitiesRequired {
		if trimmed := strings.TrimSpace(capability); trimmed != "" {
			capabilities = append(capabilities, trimmed)
		}
	}
	if len(capabilities) > 0 {
		s.host.Authorization().RegisterPlugin(pluginID, capabilities)
	} else {
		s.host.Authorization().Revoke(pluginID)
	}
	return env.MakeResponse(map[string]any{
		"accepted":  true,
		"plugin_id": pluginID,
	}, nil)
}

func countRuntimeComponents(components []protocol.ComponentDeclaration) int {
	count := 0
	for _, component := range components {
		if !IsAPIComponent(component.ComponentType) {
			count++
		}
	}
	return count
}

func (s *RPCServer) handleRunnerReady(env protocol.Envelope) protocol.Envelope {
	payload, err := decodePayload[protocol.RunnerReadyPayload](env.Payload)
	if err != nil {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), "runner.ready payload 非法", nil)
	}
	s.host.ApplyRunnerReadyForGroup(payload, s.RunnerGroup())
	return env.MakeResponse(map[string]any{"accepted": true}, nil)
}
