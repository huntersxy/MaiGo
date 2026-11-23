package policy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"go.uber.org/zap"
)

var (
	ErrClosed           = errors.New("policy: registry closed")
	ErrSwitchInProgress = errors.New("policy: switch in progress")
)

const retirementTimeout = 5 * time.Second

type transition struct {
	blocked  map[Key]*binding
	drained  chan struct{}
	finished chan struct{}
	closing  bool
}

func (pending *transition) checkDrained() {
	if pending.drained == nil {
		return
	}
	for _, entry := range pending.blocked {
		if entry.users != 0 {
			return
		}
	}
	close(pending.drained)
	pending.drained = nil
}

type Plan func(*Snapshot) (changes []Change, persist func() error, err error)

func (registry *Registry) Update(ctx context.Context, plan Plan) (*Snapshot, error) {
	pending := &transition{finished: make(chan struct{})}
	registry.mu.Lock()
	if registry.closed {
		registry.mu.Unlock()
		return nil, ErrClosed
	}
	if registry.transition != nil {
		registry.mu.Unlock()
		return nil, ErrSwitchInProgress
	}
	for active := leaseFrom(ctx); active != nil; active = active.parent {
		if active.registry == registry && !active.released {
			registry.mu.Unlock()
			return nil, ErrSwitchInProgress
		}
	}
	registry.transition = pending
	current := registry.Snapshot()
	registry.mu.Unlock()

	defer registry.endTransition(pending)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	changes, persist, err := plan(current)
	if err != nil {
		return nil, err
	}
	candidates, err := registry.prepare(current, changes)
	if err != nil {
		return nil, err
	}
	blocked := make(map[Key]*binding, len(changes))
	for key := range candidates {
		if previous := current.bindings[key]; previous != nil {
			blocked[key] = previous
		}
	}
	if err := registry.drain(ctx, pending, blocked); err != nil {
		registry.endTransition(pending)
		retire(candidates)
		return nil, err
	}
	if persist != nil {
		if err := persist(); err != nil {
			registry.endTransition(pending)
			retire(candidates)
			return nil, err
		}
	}
	next := &Snapshot{bindings: maps.Clone(current.bindings)}
	maps.Copy(next.bindings, candidates)
	registry.current.Store(next)
	registry.endTransition(pending)
	retire(blocked)
	return next, nil
}

func (registry *Registry) prepare(current *Snapshot, changes []Change) (map[Key]*binding, error) {
	candidates := make(map[Key]*binding, len(changes))
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for _, change := range changes {
		if change.entry == nil || registry.slots[change.key] != change.entry.slot || candidates[change.key] != nil ||
			current.bindings[change.key] == change.entry {
			return nil, fmt.Errorf("policy: invalid replacement for %q", change.key)
		}
		candidates[change.key] = change.entry
	}
	return candidates, nil
}

func (registry *Registry) drain(ctx context.Context, pending *transition, blocked map[Key]*binding) error {
	drained := make(chan struct{})
	registry.mu.Lock()
	pending.blocked = blocked
	pending.drained = drained
	pending.checkDrained()
	registry.mu.Unlock()
	select {
	case <-drained:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (registry *Registry) endTransition(pending *transition) {
	registry.mu.Lock()
	if registry.transition == pending {
		registry.transition = nil
		close(pending.finished)
	}
	registry.mu.Unlock()
}

func (registry *Registry) Close(ctx context.Context) error {
	for {
		registry.mu.Lock()
		for active := leaseFrom(ctx); active != nil; active = active.parent {
			if active.registry == registry && !active.released {
				registry.mu.Unlock()
				return ErrSwitchInProgress
			}
		}
		if registry.closed {
			registry.mu.Unlock()
			return nil
		}
		if current := registry.transition; current != nil {
			registry.mu.Unlock()
			select {
			case <-current.finished:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		pending := &transition{finished: make(chan struct{}), closing: true}
		registry.transition = pending
		current := registry.Snapshot()
		drained := make(chan struct{})
		pending.blocked = current.bindings
		pending.drained = drained
		pending.checkDrained()
		registry.mu.Unlock()
		result := make(chan error, 1)
		go func() {
			<-drained
			closeCtx, cancel := context.WithTimeout(context.Background(), retirementTimeout)
			defer cancel()
			err := shutdown(closeCtx, current.bindings)
			registry.mu.Lock()
			registry.closed = true
			registry.transition = nil
			close(pending.finished)
			registry.mu.Unlock()
			result <- err
		}()
		select {
		case err := <-result:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func retire(entries map[Key]*binding) {
	ctx, cancel := context.WithTimeout(context.Background(), retirementTimeout)
	defer cancel()
	if err := shutdown(ctx, entries); err != nil {
		zap.L().Warn("回收榫卯实现失败", zap.Error(err))
	}
}

func shutdown(ctx context.Context, entries map[Key]*binding) error {
	var errs []error
	for _, key := range slices.Sorted(maps.Keys(entries)) {
		entry := entries[key]
		if entry.stop != nil {
			if err := entry.stop(ctx); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", key, err))
			}
		}
	}
	return errors.Join(errs...)
}
