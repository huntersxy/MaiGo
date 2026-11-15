package host

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"

	"MaiGo/internal/core/eventbus"
	"MaiGo/internal/plugin_runtime/circuitbreaker"
	"MaiGo/internal/plugin_runtime/protocol"
)

const (
	// EventOnStart 进程启动完成事件，对齐 core.types.EventType.ON_START
	EventOnStart = "on_start"
	// EventOnStop 进程停止事件，对齐 core.types.EventType.ON_STOP
	EventOnStop = "on_stop"
	// eventHandlerHistoryLimit 每个事件类型的历史上限，对齐 event_dispatcher._MAX_HISTORY_LENGTH
	eventHandlerHistoryLimit = 100
	// defaultEventHandlerTimeoutMS plugin.emit_event 默认超时，对齐 supervisor.invoke_plugin 的 30000
	defaultEventHandlerTimeoutMS int64 = 30000
)

// EventHandlerComponent 已注册的事件处理器条目，对齐 component_registry.EventHandlerEntry
type EventHandlerComponent struct {
	PluginID       string
	Name           string
	FullName       string
	EventType      string
	Weight         int
	Intercept      bool
	Enabled        bool
	ChatScope      string
	AllowedSession []string
}

// EventResult 单个 EventHandler 的执行结果，对齐 event_dispatcher.EventResult
type EventResult struct {
	HandlerName        string
	Success            bool
	ContinueProcessing bool
	ModifiedMessage    map[string]any
	CustomResult       any
}

// EventHandlers 返回指定事件类型已启用的处理器，按 weight 降序（同权重保持注册顺序）
func (h *Host) EventHandlers(eventType string) []EventHandlerComponent {
	return h.eventHandlersMatching(eventType, "")
}

// EventHandlersForSession 返回指定聊天流可用的事件处理器
//
// 全局 Enabled 与聊天级禁用同时生效；空 sessionID 只应用全局状态。
func (h *Host) EventHandlersForSession(eventType, sessionID string) []EventHandlerComponent {
	return h.eventHandlersMatching(eventType, strings.TrimSpace(sessionID))
}

// eventHandlersMatching 按会话上下文筛选事件处理器
//
// registry 的 allowed_session 过滤需要真实 sessionID，不能先按空会话枚举再过滤。
func (h *Host) eventHandlersMatching(eventType, sessionID string) []EventHandlerComponent {
	h.eventMu.RLock()
	defer h.eventMu.RUnlock()
	var handlers []EventHandlerComponent
	for _, pluginID := range h.eventPluginOrder {
		for _, entry := range h.eventHandlers[pluginID] {
			if entry.EventType != eventType || !h.eventHandlerEnabledLocked(entry, sessionID) {
				continue
			}
			if sessionID != "" && h.eventHandlerLocalDisabledLocked(entry.FullName, sessionID) {
				continue
			}
			handlers = append(handlers, entry)
		}
	}
	sort.SliceStable(handlers, func(i, j int) bool {
		return handlers[i].Weight > handlers[j].Weight
	})
	return handlers
}

// SetEventHandlerLocalEnabled 设置聊天级 EventHandler 启停，返回是否找到处理器
func (h *Host) SetEventHandlerLocalEnabled(fullName, sessionID string, enabled bool) bool {
	if h == nil {
		return false
	}
	fullName = strings.TrimSpace(fullName)
	sessionID = strings.TrimSpace(sessionID)
	if fullName == "" || sessionID == "" {
		return false
	}
	h.eventMu.Lock()
	defer h.eventMu.Unlock()
	found := false
	for _, group := range h.eventHandlers {
		for _, entry := range group {
			if entry.FullName == fullName {
				found = true
				break
			}
		}
	}
	if !found {
		return false
	}
	if h.components != nil {
		h.components.SetComponentEnabled(fullName, enabled, sessionID)
	}
	if enabled {
		if sessions := h.eventDisabledLocal[fullName]; sessions != nil {
			delete(sessions, sessionID)
			if len(sessions) == 0 {
				delete(h.eventDisabledLocal, fullName)
			}
		}
		return true
	}
	if h.eventDisabledLocal == nil {
		h.eventDisabledLocal = make(map[string]map[string]struct{})
	}
	sessions := h.eventDisabledLocal[fullName]
	if sessions == nil {
		sessions = make(map[string]struct{})
		h.eventDisabledLocal[fullName] = sessions
	}
	sessions[sessionID] = struct{}{}
	return true
}

// EventHandlerLocalEnabled 查询 EventHandler 在指定聊天流是否启用
func (h *Host) EventHandlerLocalEnabled(fullName, sessionID string) bool {
	if h == nil {
		return false
	}
	h.eventMu.RLock()
	defer h.eventMu.RUnlock()
	return !h.eventHandlerLocalDisabledLocked(strings.TrimSpace(fullName), strings.TrimSpace(sessionID))
}

// eventHandlerEnabledLocked 读取统一组件索引的启用状态；索引缺失时回退事件条目自身状态
func (h *Host) eventHandlerEnabledLocked(entry EventHandlerComponent, sessionID string) bool {
	if h.components == nil {
		return entry.Enabled
	}
	if enabled, found := h.components.ComponentEnabled(entry.FullName, sessionID); found {
		return enabled
	}
	return entry.Enabled
}

func (h *Host) eventHandlerLocalDisabledLocked(fullName, sessionID string) bool {
	if fullName == "" || sessionID == "" {
		return false
	}
	sessions := h.eventDisabledLocal[fullName]
	if sessions == nil {
		return false
	}
	_, disabled := sessions[sessionID]
	return disabled
}

// AllEventHandlers 返回全部事件处理器（含禁用），保持插件注册顺序，供插件管理命令枚举组件
func (h *Host) AllEventHandlers() []EventHandlerComponent {
	h.eventMu.RLock()
	defer h.eventMu.RUnlock()
	var handlers []EventHandlerComponent
	for _, pluginID := range h.eventPluginOrder {
		for _, entry := range h.eventHandlers[pluginID] {
			if h.components != nil {
				if enabled, found := h.components.ComponentEnabled(entry.FullName, ""); found {
					entry.Enabled = enabled
				}
			}
			handlers = append(handlers, entry)
		}
	}
	return handlers
}

// buildEventHandlers 校验并转换全部组件声明，只保留 EVENT_HANDLER
func (h *Host) buildEventHandlers(rec PluginRecord, comps []protocol.ComponentDeclaration) ([]EventHandlerComponent, error) {
	pluginID := strings.TrimSpace(rec.PluginID)
	handlers := make([]EventHandlerComponent, 0, len(comps))
	for _, comp := range comps {
		componentType, err := normalizeComponentType(comp.ComponentType)
		if err != nil {
			return nil, err
		}
		if componentType != ComponentTypeEventHandler {
			continue
		}
		name := strings.TrimSpace(comp.Name)
		handlers = append(handlers, EventHandlerComponent{
			PluginID:       pluginID,
			Name:           name,
			FullName:       pluginID + "." + name,
			EventType:      metadataEventType(comp.Metadata),
			Weight:         metadataInt(comp.Metadata, "weight"),
			Intercept:      metadataTruthy(comp.Metadata, "intercept_message"),
			Enabled:        metadataEnabled(comp.Metadata),
			ChatScope:      normalizeComponentChatScope(comp.ChatScope),
			AllowedSession: normalizeComponentAllowedSession(comp.AllowedSession),
		})
	}
	return handlers, nil
}

// replaceEventHandlers 整体替换某插件的事件处理器，对齐 register_plugin_components 的全量替换
func (h *Host) replaceEventHandlers(pluginID string, handlers []EventHandlerComponent) {
	h.eventMu.Lock()
	defer h.eventMu.Unlock()
	delete(h.eventHandlers, pluginID)
	h.removeEventPluginOrderLocked(pluginID)
	if len(handlers) > 0 {
		h.eventHandlers[pluginID] = handlers
		h.eventPluginOrder = append(h.eventPluginOrder, pluginID)
	}
}

// removeEventHandlers 移除某插件的全部事件处理器，返回移除数量
func (h *Host) removeEventHandlers(pluginID string) int {
	h.eventMu.Lock()
	defer h.eventMu.Unlock()
	removed := len(h.eventHandlers[pluginID])
	delete(h.eventHandlers, pluginID)
	h.removeEventPluginOrderLocked(pluginID)
	return removed
}

func (h *Host) removeEventPluginOrderLocked(pluginID string) {
	for index, name := range h.eventPluginOrder {
		if name == pluginID {
			h.eventPluginOrder = append(h.eventPluginOrder[:index], h.eventPluginOrder[index+1:]...)
			return
		}
	}
}

// EmitLifecycleEvent 触发进程生命周期事件（on_start/on_stop）。
// MaiBot 只在 main.py 与 runtime.py 触发这两个事件，消息体为空。
func (h *Host) EmitLifecycleEvent(ctx context.Context, eventType string) bool {
	if h == nil || ctx == nil {
		return true
	}
	shouldContinue, _ := h.DispatchEvent(ctx, eventType, nil, nil)
	return shouldContinue
}

// DispatchEvent 将事件分发给已注册的插件处理器，对齐 EventDispatcher.dispatch_event
//
// 事件统一经过 core/eventbus 的拦截链，插件 EventHandler 作为其中一个订阅者参与分发；
// 未接入 eventbus 时回退直接分发，保持 Host 单独可用。
func (h *Host) DispatchEvent(ctx context.Context, eventType string, message map[string]any, extraArgs map[string]any) (bool, map[string]any) {
	if h == nil {
		return true, message
	}
	if h.eventBus == nil {
		return h.dispatchEventDirect(ctx, eventType, message, extraArgs)
	}
	h.ensureEventBusBridge(eventType)
	wrapped := cloneEventMessage(message)
	if wrapped == nil {
		wrapped = map[string]any{}
	}
	if len(extraArgs) > 0 {
		wrapped[pluginEventExtraArgsKey] = extraArgs
	}
	shouldContinue, modified := h.eventBus.Emit(ctx, eventbus.EventType(eventType), eventbus.Message(wrapped))
	if modified == nil {
		return shouldContinue, message
	}
	delete(modified, pluginEventExtraArgsKey)
	if len(modified) == 0 && message == nil {
		return shouldContinue, nil
	}
	return shouldContinue, map[string]any(modified)
}

// dispatchEventDirect 先按 weight 降序串行执行 intercept_message 处理器并可修改消息/中断后续流程，
// 再为其余处理器复制一份消息异步执行；返回 (是否继续, 修改后的消息)。
func (h *Host) dispatchEventDirect(ctx context.Context, eventType string, message map[string]any, extraArgs map[string]any) (bool, map[string]any) {
	sessionID := eventSessionID(message, extraArgs)
	handlers := h.EventHandlersForSession(eventType, sessionID)
	if len(handlers) == 0 {
		return true, nil
	}
	modified := message
	var intercepts, asyncHandlers []EventHandlerComponent
	for _, entry := range handlers {
		if entry.Intercept {
			intercepts = append(intercepts, entry)
		} else {
			asyncHandlers = append(asyncHandlers, entry)
		}
	}
	shouldContinue := true
	for _, entry := range intercepts {
		if ctx.Err() != nil {
			return shouldContinue, nil
		}
		result := h.invokeEventHandler(ctx, entry, eventDispatchArgs(eventType, modified, extraArgs), eventType)
		if !result.ContinueProcessing {
			shouldContinue = false
			break
		}
		if result.ModifiedMessage != nil {
			modified = result.ModifiedMessage
		}
	}
	if shouldContinue {
		for _, entry := range asyncHandlers {
			if ctx.Err() != nil {
				break
			}
			args := eventDispatchArgs(eventType, cloneEventMessage(modified), extraArgs)
			h.dispatchEventAsync(entry, args, eventType)
		}
	}
	return shouldContinue, modified
}

func (h *Host) dispatchEventAsync(entry EventHandlerComponent, args map[string]any, eventType string) {
	h.eventWG.Add(1)
	go func() {
		defer h.eventWG.Done()
		h.invokeEventHandler(h.eventContext(), entry, args, eventType)
	}()
}

func (h *Host) eventContext() context.Context {
	h.eventMu.RLock()
	defer h.eventMu.RUnlock()
	if h.eventsCtx != nil {
		return h.eventsCtx
	}
	return context.Background()
}

func (h *Host) invokeEventHandler(ctx context.Context, entry EventHandlerComponent, args map[string]any, eventType string) EventResult {
	result := EventResult{HandlerName: entry.FullName, Success: true, ContinueProcessing: true}
	if ctx.Err() != nil {
		return result
	}
	permit, err := circuitbreaker.Default().TryAcquire(entry.PluginID, entry.FullName, "event:"+eventType)
	if err != nil {
		zap.L().Warn("EventHandler 熔断检查失败", zap.String("handler", entry.FullName), zap.Error(err))
		return result
	}
	if !permit.Allowed {
		return result
	}

	envelope, invokeErr := h.InvokePlugin(ctx, protocol.MethodPluginEmitEvent, entry.PluginID, entry.Name, args, defaultEventHandlerTimeoutMS)
	if invokeErr != nil {
		if eventShouldRecordCircuitFailure(invokeErr, nil) {
			circuitbreaker.Default().RecordFailure(permit, invokeErr.Error())
		}
		zap.L().Error("EventHandler 执行失败", zap.String("handler", entry.FullName), zap.Error(invokeErr))
		result.Success = false
		return h.recordEventResult(eventType, result)
	}
	if envelope.Error != nil {
		rpcErr := protocol.RPCErrorFromMap(envelope.Error)
		if eventShouldRecordCircuitFailure(rpcErr, envelope.Error) {
			circuitbreaker.Default().RecordFailure(permit, rpcErr.Error())
		}
		zap.L().Error("EventHandler 执行失败", zap.String("handler", entry.FullName), zap.Error(rpcErr))
		result.Success = false
		return h.recordEventResult(eventType, result)
	}
	circuitbreaker.Default().RecordSuccess(permit)

	payload := envelope.Payload
	result.Success = payloadBool(payload, "success", true)
	result.ContinueProcessing = payloadBool(payload, "continue_processing", true)
	result.ModifiedMessage = eventModifiedMessage(payload["modified_message"])
	result.CustomResult = payload["custom_result"]
	return h.recordEventResult(eventType, result)
}

func (h *Host) recordEventResult(eventType string, result EventResult) EventResult {
	h.eventMu.Lock()
	defer h.eventMu.Unlock()
	if !h.eventHistoryEnabled[eventType] {
		return result
	}
	history := append(h.eventHistory[eventType], result)
	if len(history) > eventHandlerHistoryLimit {
		history = history[len(history)-eventHandlerHistoryLimit:]
	}
	h.eventHistory[eventType] = history
	return result
}

// EnableEventHistory 开启某事件类型的结果历史，对齐 EventDispatcher.enable_history
func (h *Host) EnableEventHistory(eventType string) {
	h.eventMu.Lock()
	defer h.eventMu.Unlock()
	h.eventHistoryEnabled[eventType] = true
	if _, ok := h.eventHistory[eventType]; !ok {
		h.eventHistory[eventType] = nil
	}
}

// DisableEventHistory 关闭并清理某事件类型的历史，对齐 EventDispatcher.disable_history
func (h *Host) DisableEventHistory(eventType string) {
	h.eventMu.Lock()
	defer h.eventMu.Unlock()
	delete(h.eventHistoryEnabled, eventType)
	delete(h.eventHistory, eventType)
}

// EventHistory 返回某事件类型的历史快照
func (h *Host) EventHistory(eventType string) []EventResult {
	h.eventMu.RLock()
	defer h.eventMu.RUnlock()
	return append([]EventResult(nil), h.eventHistory[eventType]...)
}

// ClearEventHistory 清空某事件类型的历史
func (h *Host) ClearEventHistory(eventType string) {
	h.eventMu.Lock()
	defer h.eventMu.Unlock()
	if _, ok := h.eventHistory[eventType]; ok {
		h.eventHistory[eventType] = nil
	}
}

// eventSessionID 从事件消息或额外参数里提取聊天流 ID，供聊天级 EventHandler 过滤
func eventSessionID(message, extraArgs map[string]any) string {
	for _, source := range []map[string]any{extraArgs, message} {
		if source == nil {
			continue
		}
		for _, key := range []string{"stream_id", "session_id", "chat_id"} {
			if value := strings.TrimSpace(stringValue(source[key])); value != "" {
				return value
			}
		}
		if nested, ok := source["message"].(map[string]any); ok {
			for _, key := range []string{"stream_id", "session_id", "chat_id"} {
				if value := strings.TrimSpace(stringValue(nested[key])); value != "" {
					return value
				}
			}
		}
	}
	return ""
}

func eventDispatchArgs(eventType string, message map[string]any, extraArgs map[string]any) map[string]any {
	args := map[string]any{
		"event_type": eventType,
		"message":    message,
	}
	for key, value := range extraArgs {
		args[key] = value
	}
	return args
}

func cloneEventMessage(message map[string]any) map[string]any {
	if message == nil {
		return nil
	}
	cloned := make(map[string]any, len(message))
	for key, value := range message {
		cloned[key] = value
	}
	return cloned
}

func eventModifiedMessage(raw any) map[string]any {
	switch typed := raw.(type) {
	case nil:
		return nil
	case map[string]any:
		return typed
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[fmt.Sprint(key)] = value
		}
		return out
	default:
		return nil
	}
}

func eventShouldRecordCircuitFailure(err error, errorPayload map[string]any) bool {
	if err == nil {
		return false
	}
	if rpcErr, ok := err.(*protocol.RPCError); ok {
		return rpcErr.Code == protocol.ErrorCodeTimeout || rpcErr.Code == protocol.ErrorCodePluginCrashed
	}
	if code, ok := errorPayload["code"].(string); ok {
		if protocol.ErrorCode(code) == protocol.ErrorCodeTimeout || protocol.ErrorCode(code) == protocol.ErrorCodePluginCrashed {
			return true
		}
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "超时") || strings.Contains(text, "timeout") || strings.Contains(text, "崩溃")
}

// metadataEventType 读取 EventHandler 的 event_type；MaiBot 按原值比较，非字符串不会命中任何事件名
func metadataEventType(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	raw, ok := metadata["event_type"]
	if !ok {
		return ""
	}
	if text, ok := raw.(string); ok {
		return text
	}
	return ""
}

// metadataInt 读取整数元数据，缺失或非数值按 0 处理（对齐 metadata.get(key, 0) 的常用形态）
func metadataInt(metadata map[string]any, key string) int {
	if metadata == nil {
		return 0
	}
	switch typed := metadata[key].(type) {
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case uint:
		return int(typed)
	case uint8:
		return int(typed)
	case uint16:
		return int(typed)
	case uint32:
		return int(typed)
	case uint64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	case bool:
		if typed {
			return 1
		}
		return 0
	default:
		return 0
	}
}

// metadataTruthy 按 Python 真值语义读取布尔型元数据
func metadataTruthy(metadata map[string]any, key string) bool {
	if metadata == nil {
		return false
	}
	switch typed := metadata[key].(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case int:
		return typed != 0
	case int8:
		return typed != 0
	case int16:
		return typed != 0
	case int32:
		return typed != 0
	case int64:
		return typed != 0
	case uint:
		return typed != 0
	case uint8:
		return typed != 0
	case uint16:
		return typed != 0
	case uint32:
		return typed != 0
	case uint64:
		return typed != 0
	case float32:
		return typed != 0
	case float64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}
