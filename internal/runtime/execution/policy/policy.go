package policy

import (
	"context"
	"sync"
	"sync/atomic"
)

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

type SessionTask = func(context.Context) error

type SessionScheduler interface {
	Submit(ctx context.Context, sessionKey string, task SessionTask) error
	Shutdown(context.Context) error
	Stats() any
}

type Slot[T any] struct {
	registry *Registry
	key      Key
}

type binding struct {
	slot  any
	value any
	stop  func(context.Context) error
	users int
}

type Snapshot struct {
	bindings map[Key]*binding
}

func (slot *Slot[T]) From(snapshot *Snapshot) (T, bool) {
	if slot != nil && snapshot != nil {
		if entry := snapshot.bindings[slot.key]; entry != nil && entry.slot == slot {
			return *entry.value.(*T), true
		}
	}
	var zero T
	return zero, false
}

func (slot *Slot[T]) Get() (T, bool) {
	if slot == nil {
		var zero T
		return zero, false
	}
	return slot.From(slot.registry.Snapshot())
}

type Change struct {
	key   Key
	entry *binding
}

func (slot *Slot[T]) With(value T) Change {
	entry := &binding{slot: slot, value: &value}
	if closer, ok := any(value).(interface{ Shutdown(context.Context) error }); ok {
		entry.stop = closer.Shutdown
	}
	return Change{key: slot.key, entry: entry}
}

type DirectScheduler struct{}

func (DirectScheduler) Submit(ctx context.Context, _ string, task SessionTask) error {
	if task == nil {
		return nil
	}
	return task(ctx)
}

func (DirectScheduler) Shutdown(context.Context) error { return nil }
func (DirectScheduler) Stats() any                     { return nil }

type Registry struct {
	mu         sync.Mutex
	slots      map[Key]any
	current    atomic.Pointer[Snapshot]
	transition *transition
	closed     bool

	SessionScheduler *Slot[SessionScheduler]
	MessageCommitter *Slot[MessageCommitter]
}

func NewRegistry() *Registry {
	registry := &Registry{slots: make(map[Key]any)}
	registry.current.Store(&Snapshot{})
	registry.SessionScheduler, _ = RegisterSlot[SessionScheduler](registry, SessionSchedulerKey)
	registry.MessageCommitter, _ = RegisterSlot[MessageCommitter](registry, MessageCommitterKey)
	registry.current.Store(&Snapshot{bindings: map[Key]*binding{
		SessionSchedulerKey: registry.SessionScheduler.With(DirectScheduler{}).entry,
		MessageCommitterKey: registry.MessageCommitter.With(InlineCommitter{}).entry,
	}})
	return registry
}

func RegisterSlot[T any](registry *Registry, key Key) (*Slot[T], bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if existing, ok := registry.slots[key]; ok {
		slot, ok := existing.(*Slot[T])
		return slot, ok
	}
	if registry.closed || registry.transition != nil {
		return nil, false
	}
	slot := &Slot[T]{registry: registry, key: key}
	registry.slots[key] = slot
	return slot, true
}

func LookupSlot[T any](registry *Registry, key Key) (*Slot[T], bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	slot, ok := registry.slots[key].(*Slot[T])
	return slot, ok
}

func (registry *Registry) Snapshot() *Snapshot {
	return registry.current.Load()
}
