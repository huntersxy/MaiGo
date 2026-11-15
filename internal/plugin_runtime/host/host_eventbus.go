package host

import (
	"context"
	"strings"

	"MaiGo/internal/core/eventbus"
)

const pluginEventExtraArgsKey = "__pluginruntime_extra_args"

// EventBus 返回 Host 使用的事件总线，供主程序接入消息事件
func (h *Host) EventBus() *eventbus.EventBus {
	if h == nil {
		return nil
	}
	return h.eventBus
}

// installEventBusBridge 将全部内置事件类型接到插件 EventHandler 分发链
//
// 主程序通过 EventBus.Emit 触发事件时，插件 EventHandler 作为拦截型订阅者参与同一条
// 事件链，不再绕过 eventbus 直接调用 DispatchEvent。
func (h *Host) installEventBusBridge() {
	if h == nil || h.eventBus == nil {
		return
	}
	for _, eventType := range eventbus.AllEventTypes {
		h.ensureEventBusBridge(string(eventType))
	}
}

func (h *Host) ensureEventBusBridge(eventType string) {
	if h == nil || h.eventBus == nil {
		return
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		return
	}
	h.eventMu.Lock()
	if h.eventBusBridged == nil {
		h.eventBusBridged = make(map[string]struct{})
	}
	if _, ok := h.eventBusBridged[eventType]; ok {
		h.eventMu.Unlock()
		return
	}
	h.eventBusBridged[eventType] = struct{}{}
	h.eventMu.Unlock()
	h.eventBus.Subscribe(eventbus.EventType(eventType), "pluginruntime.event_handlers", -1_000_000, true,
		func(ctx context.Context, busMessage eventbus.Message) (bool, eventbus.Message, error) {
			return h.handleBusEvent(ctx, eventType, busMessage)
		})
}

// handleBusEvent 把 eventbus 消息还原为插件 EventHandler 参数
func (h *Host) handleBusEvent(ctx context.Context, eventType string, busMessage eventbus.Message) (bool, eventbus.Message, error) {
	values := map[string]any(busMessage)
	extra := map[string]any(nil)
	if raw, ok := values[pluginEventExtraArgsKey]; ok {
		extra, _ = raw.(map[string]any)
		delete(values, pluginEventExtraArgsKey)
	}
	var eventMessage map[string]any
	if len(values) > 0 {
		eventMessage = values
	}
	shouldContinue, modified := h.dispatchEventDirect(ctx, eventType, eventMessage, extra)
	if modified == nil {
		modified = values
	}
	return shouldContinue, eventbus.Message(modified), nil
}
