package host

import (
	"context"
	"errors"
	"strings"

	"MaiGo/internal/plugin_runtime/protocol"
)

func (s *RPCServer) handleUpdateMessageGatewayState(ctx context.Context, env protocol.Envelope) protocol.Envelope {
	payload, err := decodePayload[protocol.MessageGatewayStateUpdatePayload](env.Payload)
	if err != nil {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), "消息网关状态更新 payload 非法", nil)
	}
	pluginID := strings.TrimSpace(env.PluginID)
	if pluginID == "" {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), "plugin_id 不能为空", nil)
	}
	state, routeKey, err := s.host.UpdateMessageGatewayStateContext(ctx, pluginID, payload)
	if err != nil {
		return gatewayErrorResponse(env, err)
	}
	return env.MakeResponse(map[string]any{
		"accepted":  true,
		"ready":     state.Ready,
		"route_key": routeKey,
	}, nil)
}

func (s *RPCServer) handleRouteMessage(ctx context.Context, env protocol.Envelope) protocol.Envelope {
	payload, err := decodePayload[protocol.RouteMessagePayload](env.Payload)
	if err != nil {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), "消息网关路由 payload 非法", nil)
	}
	pluginID := strings.TrimSpace(env.PluginID)
	if pluginID == "" {
		return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), "plugin_id 不能为空", nil)
	}
	accepted, routeKey, err := s.host.RouteMessage(ctx, pluginID, payload)
	if err != nil {
		return gatewayErrorResponse(env, err)
	}
	return env.MakeResponse(map[string]any{
		"accepted":  accepted,
		"route_key": routeKey,
	}, nil)
}

func gatewayErrorResponse(env protocol.Envelope, err error) protocol.Envelope {
	var rpcErr *GatewayRPCError
	if errors.As(err, &rpcErr) && rpcErr != nil {
		return env.MakeErrorResponse(rpcErr.Code, rpcErr.Message, nil)
	}
	return env.MakeErrorResponse(string(protocol.ErrorCodeBadPayload), err.Error(), nil)
}
