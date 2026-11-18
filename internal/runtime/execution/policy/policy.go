// Package policy 提供可独立原子替换的本地执行策略插槽。
//
// compatibility（Python 对齐实现）是每个插槽的默认值；native（Go 特调）实现
// 在启动装配时按局部选择写入。业务代码只从 Slot 读取当前实现，不出现 mode 判断。
// 新增一个局部特调只需要：
//  1. 定义该能力的接口；
//  2. 在 Registry 注册一个 typed Slot（或使用已有 Slot）；
//  3. 启动装配时 Set 兼容/native 实现；
//  4. 消费方按操作读取 Slot。
//
// Slot 基于 atomic.Pointer，可单独 Swap；当前模式切换仍要求重启，但替换本身是原子的。
package policy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// Key 标识一个局部执行策略插槽。
type Key string

const (
	SessionSchedulerKey Key = "native_session_scheduler"
	MessageCommitterKey Key = "native_message_committer"
	EventDispatcherKey  Key = "native_event_dispatcher"
	WriteCoordinatorKey Key = "native_write_coordinator"
	MemoryWritebackKey  Key = "async_memory_writeback"
	LLMConcurrencyKey   Key = "parallel_llm_calls"
	BatchDBWritesKey    Key = "batch_db_writes"
	SQLiteReadPoolKey   Key = "sqlite_read_pool"
)

// SessionTask 调度器执行的单个会话任务。
type SessionTask func(context.Context) error

// SessionScheduler 会话调度策略。
type SessionScheduler interface {
	Submit(ctx context.Context, sessionKey string, task SessionTask) error
	Shutdown(ctx context.Context) error
	Stats() any
}

// Slot 一个可独立原子替换的实现插槽。
type Slot[T any] struct {
	value atomic.Pointer[T]
}

// NewSlot 创建并写入初始值的插槽。
func NewSlot[T any](initial T) *Slot[T] {
	slot := &Slot[T]{}
	slot.Set(initial)
	return slot
}

// Get 读取当前实现；未写入时返回零值与 false。
func (s *Slot[T]) Get() (T, bool) {
	if s == nil {
		var zero T
		return zero, false
	}
	value := s.value.Load()
	if value == nil {
		var zero T
		return zero, false
	}
	return *value, true
}

// Set 原子写入实现。
func (s *Slot[T]) Set(value T) {
	if s == nil {
		return
	}
	s.value.Store(&value)
}

// Swap 原子替换实现并返回旧值；旧值未设置时 ok 为 false。
func (s *Slot[T]) Swap(value T) (T, bool) {
	if s == nil {
		var zero T
		return zero, false
	}
	old := s.value.Swap(&value)
	if old == nil {
		var zero T
		return zero, false
	}
	return *old, true
}

// Value 以类型擦除形式返回当前实现，供 Registry 统一做生命周期收尾。
func (s *Slot[T]) Value() (any, bool) {
	value, ok := s.Get()
	if !ok {
		return nil, false
	}
	return any(value), true
}

// DirectScheduler compatibility 实现：在当前 goroutine 直接执行任务。
//
// 它保留 Python 侧“入站处理在调用链内完成”的可观察节奏，是默认实现。
type DirectScheduler struct{}

// Submit 直接执行任务。
func (DirectScheduler) Submit(ctx context.Context, _ string, task SessionTask) error {
	if task == nil {
		return nil
	}
	return task(ctx)
}

// Shutdown compatibility 实现没有后台资源。
func (DirectScheduler) Shutdown(context.Context) error { return nil }

// Stats compatibility 实现没有调度统计。
func (DirectScheduler) Stats() any { return nil }

// Registry 汇总所有局部策略插槽。
type Registry struct {
	mu    sync.RWMutex
	slots map[Key]any

	// 已接入的 typed 槽；其余模块实现时用 RegisterSlot 注册自己的槽，
	// 不需要修改本结构体。
	SessionScheduler *Slot[SessionScheduler]
	MessageCommitter *Slot[MessageCommitter]
}

// NewRegistry 创建默认全 compatibility 的注册表。
func NewRegistry() *Registry {
	registry := &Registry{slots: make(map[Key]any)}
	registry.SessionScheduler, _ = RegisterSlot[SessionScheduler](registry, SessionSchedulerKey)
	registry.SessionScheduler.Set(DirectScheduler{})
	registry.MessageCommitter, _ = RegisterSlot[MessageCommitter](registry, MessageCommitterKey)
	registry.MessageCommitter.Set(InlineCommitter{})
	for _, key := range []Key{
		EventDispatcherKey,
		WriteCoordinatorKey,
		MemoryWritebackKey,
		LLMConcurrencyKey,
		BatchDBWritesKey,
		SQLiteReadPoolKey,
	} {
		_, _ = RegisterSlot[any](registry, key)
	}
	return registry
}

// RegisterSlot 注册一个 typed Slot；已存在同类型槽时复用。
//
// 如果已有未写入的空 Slot[any] 占位，会升级为请求的 typed 槽；已有实现时拒绝覆盖。
// Go 1.26 尚不支持泛型方法，因此使用包级函数。
func RegisterSlot[T any](r *Registry, key Key) (*Slot[T], bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.slots[key]; ok {
		if slot, ok := existing.(*Slot[T]); ok {
			return slot, true
		}
		if placeholder, ok := existing.(*Slot[any]); ok {
			if _, set := placeholder.Value(); !set {
				slot := &Slot[T]{}
				r.slots[key] = slot
				return slot, true
			}
		}
		return nil, false
	}
	slot := &Slot[T]{}
	r.slots[key] = slot
	return slot, true
}

// LookupSlot 查询指定类型的槽。
func LookupSlot[T any](r *Registry, key Key) (*Slot[T], bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	slot, ok := r.slots[key].(*Slot[T])
	return slot, ok
}

// SessionSchedulerStats 返回当前会话调度器统计；compatibility 实现返回 nil。
func (r *Registry) SessionSchedulerStats() any {
	if r == nil || r.SessionScheduler == nil {
		return nil
	}
	scheduler, ok := r.SessionScheduler.Get()
	if !ok || scheduler == nil {
		return nil
	}
	return scheduler.Stats()
}

// Close 关闭所有实现了 Shutdown(context.Context) 的当前实现。
func (r *Registry) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	slots := make([]any, 0, len(r.slots))
	for _, slot := range r.slots {
		slots = append(slots, slot)
	}
	r.mu.RUnlock()

	var errs []error
	for _, raw := range slots {
		slot, ok := raw.(interface{ Value() (any, bool) })
		if !ok {
			continue
		}
		value, ok := slot.Value()
		if !ok || value == nil {
			continue
		}
		shutdowner, ok := value.(interface{ Shutdown(context.Context) error })
		if !ok {
			continue
		}
		if err := shutdowner.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
