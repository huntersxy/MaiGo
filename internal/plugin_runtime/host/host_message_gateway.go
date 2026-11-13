package host

import (
	"context"
	"fmt"
	"strings"
)

// SetMessageGatewayPort 注入外部消息网关（Platform IO）实现
func (h *Host) SetMessageGatewayPort(port PluginMessageGatewayPort) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.gateway = port
	h.mu.Unlock()
}

// MessageGatewayPort 返回已注入的外部消息网关实现
func (h *Host) MessageGatewayPort() PluginMessageGatewayPort {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.gateway
}

// ReceiveExternalMessage 接收外部平台消息，对齐 receive_external_message
func (h *Host) ReceiveExternalMessage(ctx context.Context, message map[string]any) error {
	if h == nil {
		return fmt.Errorf("Host 未初始化")
	}
	h.mu.RLock()
	port := h.gateway
	h.mu.RUnlock()
	if port == nil {
		return fmt.Errorf("消息网关未接线：主程序尚未注入 Platform IO")
	}
	return port.ReceiveExternalMessage(ctx, BuildPluginSessionMessage(message))
}

// SendMessageToExternal 向外部平台发送消息，对齐 send_message_to_external
func (h *Host) SendMessageToExternal(ctx context.Context, message map[string]any, saveToDB bool) (bool, error) {
	if h == nil {
		return false, fmt.Errorf("Host 未初始化")
	}
	h.mu.RLock()
	port := h.gateway
	h.mu.RUnlock()
	if port == nil {
		return false, fmt.Errorf("消息网关未接线：主程序尚未注入 Platform IO")
	}
	return port.SendMessageToExternal(ctx, BuildPluginSessionMessage(message), saveToDB)
}

// BuildPluginSessionMessage 归一化消息字典字段，对齐 message_gateway.build_message_dict
func BuildPluginSessionMessage(raw map[string]any) map[string]any {
	out := cloneAnyMap(raw)
	if out == nil {
		out = map[string]any{}
	}
	if platform := strings.TrimSpace(stringValue(out["platform"])); platform != "" {
		out["platform"] = platform
	}
	if rawID, exists := out["target_id"]; exists {
		if text, ok := rawID.(string); ok {
			out["target_id"] = strings.TrimSpace(text)
		}
	}
	if _, exists := out["message_id"]; !exists {
		if value, ok := out["msg_id"]; ok {
			out["message_id"] = value
		}
	}
	if _, exists := out["message"]; !exists {
		if value, ok := out["content"]; ok {
			out["message"] = value
		}
	}
	return out
}
