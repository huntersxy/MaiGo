package policy

import (
	"context"
	"fmt"
)

type leaseKey struct{}

type lease struct {
	registry *Registry
	snapshot *Snapshot
	parent   *lease
	entries  []*binding
	released bool
}

func leaseFrom(ctx context.Context) *lease {
	active, _ := ctx.Value(leaseKey{}).(*lease)
	return active
}

func (active *lease) pin(ctx context.Context) context.Context {
	return context.WithValue(ctx, leaseKey{}, active)
}

func (registry *Registry) acquire(ctx context.Context, keys ...Key) (*lease, error) {
	parent := leaseFrom(ctx)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		registry.mu.Lock()
		pending := registry.transition
		inherited := pending != nil && pending.holds(registry, parent)
		if registry.closed || (pending != nil && pending.closing && !inherited) {
			registry.mu.Unlock()
			return nil, ErrClosed
		}
		if pending != nil && !inherited && pending.blocks(keys) {
			registry.mu.Unlock()
			select {
			case <-pending.finished:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		snapshot := registry.Snapshot()
		entries := make([]*binding, 0, len(keys))
		for _, key := range keys {
			entry := snapshot.bindings[key]
			if entry == nil {
				registry.mu.Unlock()
				return nil, fmt.Errorf("policy: unbound slot %q", key)
			}
			entries = append(entries, entry)
		}
		for _, entry := range entries {
			entry.users++
		}
		registry.mu.Unlock()
		return &lease{registry: registry, snapshot: snapshot, parent: parent, entries: entries}, nil
	}
}

func (pending *transition) holds(registry *Registry, active *lease) bool {
	for ; active != nil; active = active.parent {
		if active.registry != registry || active.released {
			continue
		}
		for _, entry := range active.entries {
			for _, blocked := range pending.blocked {
				if blocked == entry {
					return true
				}
			}
		}
	}
	return false
}

func (pending *transition) blocks(keys []Key) bool {
	for _, key := range keys {
		if pending.blocked[key] != nil {
			return true
		}
	}
	return false
}

func (active *lease) release() {
	registry := active.registry
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if active.released {
		return
	}
	active.released = true
	for _, entry := range active.entries {
		entry.users--
	}
	if pending := registry.transition; pending != nil {
		pending.checkDrained()
	}
}

func (slot *Slot[T]) Acquire(ctx context.Context) (T, context.Context, func(), error) {
	active, err := slot.registry.acquire(ctx, slot.key)
	if err != nil {
		var zero T
		return zero, ctx, nil, err
	}
	value, _ := slot.From(active.snapshot)
	return value, active.pin(ctx), active.release, nil
}

func (registry *Registry) SubmitSession(ctx context.Context, sessionKey string, task SessionTask) (err error) {
	if task == nil {
		return nil
	}
	active, err := registry.acquire(ctx, SessionSchedulerKey, MessageCommitterKey)
	if err != nil {
		return err
	}
	queued := false
	defer func() {
		if !queued {
			active.release()
		}
	}()
	scheduler, _ := registry.SessionScheduler.From(active.snapshot)
	err = scheduler.Submit(active.pin(ctx), sessionKey, func(runCtx context.Context) error {
		defer active.release()
		return task(active.pin(runCtx))
	})
	queued = err == nil
	return err
}
