package eventbus

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// EventType 事件类型，对齐 src/core/types.py 的 EventType
type EventType string

const (
	EventOnStart             EventType = "on_start"
	EventOnStop              EventType = "on_stop"
	EventOnMessagePreProcess EventType = "on_message_pre_process"
	EventOnMessage           EventType = "on_message"
	EventOnPlan              EventType = "on_plan"
	EventPostLLM             EventType = "post_llm"
	EventAfterLLM            EventType = "after_llm"
	EventPostSendPreProcess  EventType = "post_send_pre_process"
	EventPostSend            EventType = "post_send"
	EventAfterSend           EventType = "after_send"
	EventUnknown             EventType = "unknown"
)

// AllEventTypes 返回全部内置事件类型，对齐 EventBus 预注册行为
var AllEventTypes = []EventType{
	EventOnStart,
	EventOnStop,
	EventOnMessagePreProcess,
	EventOnMessage,
	EventOnPlan,
	EventPostLLM,
	EventAfterLLM,
	EventPostSendPreProcess,
	EventPostSend,
	EventAfterSend,
	EventUnknown,
}

// Message 事件消息的传输表示，对齐 MaiMessages.to_transport_dict 的字典结构
type Message map[string]any

// Handler 事件处理器：返回是否继续以及可能被修改的消息
type Handler func(ctx context.Context, message Message) (bool, Message, error)

// IPCBridge 插件运行时事件桥，对齐 PluginRuntimeManager.bridge_event
type IPCBridge interface {
	IsRunning() bool
	BridgeEvent(ctx context.Context, eventType string, message Message) (bool, Message, error)
}

// BridgeFunc 便于用函数接入 IPCBridge
type BridgeFunc struct {
	Running func() bool
	Bridge  func(ctx context.Context, eventType string, message Message) (bool, Message, error)
}

// IsRunning 实现 IPCBridge
func (b BridgeFunc) IsRunning() bool {
	if b.Running == nil {
		return true
	}
	return b.Running()
}

// BridgeEvent 实现 IPCBridge
func (b BridgeFunc) BridgeEvent(ctx context.Context, eventType string, message Message) (bool, Message, error) {
	if b.Bridge == nil {
		return true, nil, nil
	}
	return b.Bridge(ctx, eventType, message)
}

type handlerEntry struct {
	handler   Handler
	name      string
	weight    int
	intercept bool
}

type runningTask struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// EventBus 核心事件总线
//
// 拦截型 handler 按权重顺序同步执行，可修改消息或中断流程；
// 非拦截型 handler 在拦截链完成后并发执行，不阻塞 Emit。
type EventBus struct {
	mu       sync.RWMutex
	handlers map[EventType][]handlerEntry
	tasks    map[string]map[uint64]*runningTask
	taskSeq  uint64
	bridge   IPCBridge
}

// NewEventBus 构造事件总线并预注册全部内置事件类型
func NewEventBus() *EventBus {
	bus := &EventBus{
		handlers: make(map[EventType][]handlerEntry, len(AllEventTypes)),
		tasks:    make(map[string]map[uint64]*runningTask),
	}
	for _, eventType := range AllEventTypes {
		bus.handlers[eventType] = nil
	}
	return bus
}

// Default 全局事件总线单例，对齐 event_bus.event_bus
var Default = NewEventBus()

// SetBridge 设置插件运行时事件桥
func (b *EventBus) SetBridge(bridge IPCBridge) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.bridge = bridge
	b.mu.Unlock()
}

// Subscribe 注册事件处理器，按权重降序排序，同权重保持注册顺序
func (b *EventBus) Subscribe(eventType EventType, name string, weight int, intercept bool, handler Handler) {
	if b == nil || handler == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entries := append(b.handlers[eventType], handlerEntry{
		handler:   handler,
		name:      name,
		weight:    weight,
		intercept: intercept,
	})
	sortHandlers(entries)
	b.handlers[eventType] = entries
	zap.L().Debug("注册事件 handler",
		zap.String("name", name),
		zap.String("event_type", string(eventType)),
		zap.Int("weight", weight),
		zap.Bool("intercept", intercept),
	)
}

// Unsubscribe 取消指定名称的第一个处理器
func (b *EventBus) Unsubscribe(eventType EventType, name string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entries := b.handlers[eventType]
	for index, entry := range entries {
		if entry.name == name {
			b.handlers[eventType] = append(entries[:index], entries[index+1:]...)
			return true
		}
	}
	return false
}

// Emit 触发事件并返回是否继续以及修改后的消息
func (b *EventBus) Emit(ctx context.Context, eventType EventType, message Message) (bool, Message) {
	if b == nil {
		return true, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.RLock()
	entries := append([]handlerEntry(nil), b.handlers[eventType]...)
	bridge := b.bridge
	b.mu.RUnlock()
	if len(entries) == 0 {
		return true, nil
	}

	continueFlag := true
	currentMessage := cloneMessage(message)
	var intercepts, asyncHandlers []handlerEntry
	for _, entry := range entries {
		if entry.intercept {
			intercepts = append(intercepts, entry)
		} else {
			asyncHandlers = append(asyncHandlers, entry)
		}
	}

	for _, entry := range intercepts {
		shouldContinue, modified, err := callHandler(ctx, entry.handler, currentMessage)
		if err != nil {
			zap.L().Error("拦截型 handler 执行异常",
				zap.String("handler", entry.name),
				zap.String("event_type", string(eventType)),
				zap.Error(err),
			)
			continue
		}
		if modified != nil {
			currentMessage = modified
		}
		if !shouldContinue {
			continueFlag = false
			break
		}
	}

	if continueFlag {
		for _, entry := range asyncHandlers {
			b.fireAndForget(ctx, entry, eventType, cloneMessage(currentMessage))
		}
	}

	return b.bridgeEvent(ctx, bridge, eventType, continueFlag, currentMessage)
}

func (b *EventBus) bridgeEvent(ctx context.Context, bridge IPCBridge, eventType EventType, continueFlag bool, message Message) (bool, Message) {
	if !continueFlag || bridge == nil || !bridge.IsRunning() {
		return continueFlag, message
	}
	newContinue, modified, err := bridge.BridgeEvent(ctx, string(eventType), message)
	if err != nil {
		zap.L().Warn("桥接事件到 IPC 运行时失败", zap.String("event_type", string(eventType)), zap.Error(err))
		return continueFlag, message
	}
	return newContinue, modified
}

func (b *EventBus) fireAndForget(parent context.Context, entry handlerEntry, eventType EventType, message Message) {
	taskCtx, cancel := context.WithCancel(context.WithoutCancel(parent))
	b.mu.Lock()
	b.taskSeq++
	seq := b.taskSeq
	task := &runningTask{cancel: cancel, done: make(chan struct{})}
	group := b.tasks[entry.name]
	if group == nil {
		group = make(map[uint64]*runningTask)
		b.tasks[entry.name] = group
	}
	group[seq] = task
	b.mu.Unlock()

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				zap.L().Error("handler 异步任务异常",
					zap.String("handler", entry.name),
					zap.String("event_type", string(eventType)),
					zap.Any("panic", recovered),
				)
			}
			cancel()
			close(task.done)
			b.mu.Lock()
			if current, ok := b.tasks[entry.name]; ok {
				if _, exists := current[seq]; exists {
					delete(current, seq)
					if len(current) == 0 {
						delete(b.tasks, entry.name)
					}
				}
			}
			b.mu.Unlock()
		}()
		if _, _, err := callHandler(taskCtx, entry.handler, message); err != nil && taskCtx.Err() == nil {
			zap.L().Error("handler 异步任务异常",
				zap.String("handler", entry.name),
				zap.String("event_type", string(eventType)),
				zap.Error(err),
			)
		}
	}()
}

// CancelHandlerTasks 取消某个 handler 的全部运行中任务并等待退出
func (b *EventBus) CancelHandlerTasks(name string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	group := b.tasks[name]
	delete(b.tasks, name)
	b.mu.Unlock()
	if len(group) == 0 {
		return
	}
	for _, task := range group {
		task.cancel()
	}
	for _, task := range group {
		<-task.done
	}
	zap.L().Info("已取消 handler 任务", zap.String("handler", name), zap.Int("count", len(group)))
}

// HandlerCount 返回指定事件的处理器数量，便于测试与诊断
func (b *EventBus) HandlerCount(eventType EventType) int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.handlers[eventType])
}

// 包级便捷函数，操作全局单例

// Subscribe 在全局事件总线上注册处理器
func Subscribe(eventType EventType, name string, weight int, intercept bool, handler Handler) {
	Default.Subscribe(eventType, name, weight, intercept, handler)
}

// Unsubscribe 在全局事件总线上取消处理器
func Unsubscribe(eventType EventType, name string) bool {
	return Default.Unsubscribe(eventType, name)
}

// Emit 在全局事件总线上触发事件
func Emit(ctx context.Context, eventType EventType, message Message) (bool, Message) {
	return Default.Emit(ctx, eventType, message)
}

// CancelHandlerTasks 在全局事件总线上取消 handler 任务
func CancelHandlerTasks(name string) {
	Default.CancelHandlerTasks(name)
}

// SetBridge 设置全局事件总线的 IPC 桥
func SetBridge(bridge IPCBridge) {
	Default.SetBridge(bridge)
}

func sortHandlers(entries []handlerEntry) {
	for index := 1; index < len(entries); index++ {
		current := entries[index]
		position := index
		for position > 0 && entries[position-1].weight < current.weight {
			entries[position] = entries[position-1]
			position--
		}
		entries[position] = current
	}
}

func callHandler(ctx context.Context, handler Handler, message Message) (shouldContinue bool, modified Message, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError{value: recovered}
		}
	}()
	return handler(ctx, message)
}

type panicError struct {
	value any
}

func (p panicError) Error() string {
	return fmt.Sprintf("handler panic: %v", p.value)
}

func cloneMessage(message Message) Message {
	if message == nil {
		return nil
	}
	cloned, ok := cloneValue(message).(Message)
	if !ok {
		return nil
	}
	return cloned
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case Message:
		out := make(Message, len(typed))
		for key, item := range typed {
			out[key] = cloneValue(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = cloneValue(item)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	case bool, string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return typed
	case time.Time:
		return typed
	default:
		return typed
	}
}
