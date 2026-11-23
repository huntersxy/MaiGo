package test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"MaiGo/internal/runtime/execution/policy"
)

type trackedPolicy struct{ stopped atomic.Bool }

func (implementation *trackedPolicy) Shutdown(context.Context) error {
	implementation.stopped.Store(true)
	return nil
}

func policySlot(t *testing.T, registry *policy.Registry, key policy.Key) *policy.Slot[*trackedPolicy] {
	t.Helper()
	slot, ok := policy.RegisterSlot[*trackedPolicy](registry, key)
	if !ok {
		t.Fatal("could not register typed slot")
	}
	return slot
}

func waitPolicyResult[T any](t *testing.T, result <-chan T) T {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("policy operation timed out")
		var zero T
		return zero
	}
}

func publishPolicy(t *testing.T, registry *policy.Registry, changes ...policy.Change) *policy.Snapshot {
	t.Helper()
	snapshot, err := registry.Update(t.Context(), func(*policy.Snapshot) ([]policy.Change, func() error, error) {
		return changes, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestPolicyPublishesAllChangesTogetherAfterPersist(t *testing.T) {
	registry := policy.NewRegistry()
	first := policySlot(t, registry, policy.EventDispatcherKey)
	second := policySlot(t, registry, policy.WriteCoordinatorKey)
	oldFirst, oldSecond := &trackedPolicy{}, &trackedPolicy{}
	newFirst, newSecond := &trackedPolicy{}, &trackedPolicy{}
	previous := publishPolicy(t, registry, first.With(oldFirst), second.With(oldSecond))
	insidePersist := make(chan struct{})
	allowPersist := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := registry.Update(t.Context(), func(*policy.Snapshot) ([]policy.Change, func() error, error) {
			return []policy.Change{first.With(newFirst), second.With(newSecond)}, func() error {
				close(insidePersist)
				select {
				case <-allowPersist:
					return nil
				case <-t.Context().Done():
					return t.Context().Err()
				}
			}, nil
		})
		result <- err
	}()
	waitPolicyResult(t, insidePersist)
	if registry.Snapshot() != previous || oldFirst.stopped.Load() || oldSecond.stopped.Load() {
		t.Fatal("published or retired an implementation before persisting")
	}
	close(allowPersist)
	if err := waitPolicyResult(t, result); err != nil {
		t.Fatal(err)
	}
	next := registry.Snapshot()
	firstValue, _ := first.From(next)
	secondValue, _ := second.From(next)
	if firstValue != newFirst || secondValue != newSecond || !oldFirst.stopped.Load() || !oldSecond.stopped.Load() {
		t.Fatal("expected one published snapshot and retired old implementations")
	}
	firstValue, _ = first.From(previous)
	if firstValue != oldFirst {
		t.Fatal("previous snapshot was mutated")
	}
	if err := registry.Close(t.Context()); err != nil || !newFirst.stopped.Load() || !newSecond.stopped.Load() {
		t.Fatalf("failed to close current implementations: %v", err)
	}
}

func TestPolicyLocalSwitchKeepsUnchangedSlotInUse(t *testing.T) {
	registry := policy.NewRegistry()
	first := policySlot(t, registry, policy.EventDispatcherKey)
	second := policySlot(t, registry, policy.WriteCoordinatorKey)
	oldFirst, oldSecond, candidate := &trackedPolicy{}, &trackedPolicy{}, &trackedPolicy{}
	publishPolicy(t, registry, first.With(oldFirst), second.With(oldSecond))
	_, _, release, err := second.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = registry.Update(ctx, func(*policy.Snapshot) ([]policy.Change, func() error, error) {
		return []policy.Change{first.With(candidate)}, nil, nil
	})
	current, _ := second.Get()
	if err != nil || current != oldSecond || oldSecond.stopped.Load() || !oldFirst.stopped.Load() {
		t.Fatalf("unrelated slot was changed or waited on: %v", err)
	}
}

func TestPolicyDrainAllowsNestedOperationsAndBlocksNewOnes(t *testing.T) {
	registry := policy.NewRegistry()
	first := policySlot(t, registry, policy.EventDispatcherKey)
	second := policySlot(t, registry, policy.WriteCoordinatorKey)
	oldFirst, oldSecond, candidate := &trackedPolicy{}, &trackedPolicy{}, &trackedPolicy{}
	publishPolicy(t, registry, first.With(oldFirst), second.With(oldSecond))
	_, scoped, release, err := first.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ready := make(chan struct{})
	result := make(chan error, 1)
	switchCtx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	go func() {
		_, err := registry.Update(switchCtx, func(*policy.Snapshot) ([]policy.Change, func() error, error) {
			close(ready)
			return []policy.Change{first.With(candidate)}, nil, nil
		})
		result <- err
	}()
	waitPolicyResult(t, ready)
	probeCtx, stopProbe := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer stopProbe()
	for {
		_, _, releaseProbe, err := first.Acquire(probeCtx)
		if errors.Is(err, context.DeadlineExceeded) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		releaseProbe()
	}
	_, nested, releaseSecond, err := second.Acquire(scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSecond()
	value, _, releaseChild, err := first.Acquire(nested)
	if err != nil || value != oldFirst {
		t.Fatalf("nested task did not inherit access to the draining implementation: %v", err)
	}
	defer releaseChild()
	release()
	if oldFirst.stopped.Load() {
		t.Fatal("retired before nested task finished")
	}
	releaseChild()
	if err := waitPolicyResult(t, result); err != nil {
		t.Fatal(err)
	}
	current, _, releaseNew, err := first.Acquire(scoped)
	if err != nil || current != candidate {
		t.Fatalf("a released context retained an old implementation: %v", err)
	}
	releaseNew()
}

func TestPolicyTimeoutAndPersistFailureKeepOldImplementation(t *testing.T) {
	registry := policy.NewRegistry()
	slot := policySlot(t, registry, policy.EventDispatcherKey)
	old, drainedCandidate, failedCandidate := &trackedPolicy{}, &trackedPolicy{}, &trackedPolicy{}
	previous := publishPolicy(t, registry, slot.With(old))
	_, _, release, err := slot.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = registry.Update(ctx, func(*policy.Snapshot) ([]policy.Change, func() error, error) {
		return []policy.Change{slot.With(drainedCandidate)}, func() error {
			t.Error("persist called before the old implementation drained")
			return nil
		}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || registry.Snapshot() != previous || old.stopped.Load() || !drainedCandidate.stopped.Load() {
		t.Fatalf("unexpected timeout rollback: %v", err)
	}
	release()
	persistError := errors.New("disk write failed")
	_, err = registry.Update(t.Context(), func(*policy.Snapshot) ([]policy.Change, func() error, error) {
		return []policy.Change{slot.With(failedCandidate)}, func() error { return persistError }, nil
	})
	if !errors.Is(err, persistError) || registry.Snapshot() != previous || old.stopped.Load() || !failedCandidate.stopped.Load() {
		t.Fatalf("unexpected write failure rollback: %v", err)
	}
}
