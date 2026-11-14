package test

import (
	"context"
	"sync"
	"testing"

	"maibot-go/internal/runtime/execution/policy"
)

type fakePolicyScheduler struct {
	mu        sync.Mutex
	submitted int
	shutdown  int
	stats     any
}

func (f *fakePolicyScheduler) Submit(ctx context.Context, _ string, task policy.SessionTask) error {
	f.mu.Lock()
	f.submitted++
	f.mu.Unlock()
	if task == nil {
		return nil
	}
	return task(ctx)
}

func (f *fakePolicyScheduler) Shutdown(context.Context) error {
	f.mu.Lock()
	f.shutdown++
	f.mu.Unlock()
	return nil
}

func (f *fakePolicyScheduler) Stats() any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats
}

type testPolicyComponent interface {
	Name() string
	Shutdown(context.Context) error
}

type fakePolicyComponent struct {
	mu     sync.Mutex
	closed bool
}

func (f *fakePolicyComponent) Name() string { return "fake" }

func (f *fakePolicyComponent) Shutdown(context.Context) error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func TestPolicySlotSetAndSwap(t *testing.T) {
	slot := policy.NewSlot[policy.SessionScheduler](policy.DirectScheduler{})
	current, ok := slot.Get()
	if !ok || current == nil {
		t.Fatal("expected initial scheduler")
	}
	if _, ok := current.(policy.DirectScheduler); !ok {
		t.Fatalf("expected DirectScheduler, got %T", current)
	}

	replacement := &fakePolicyScheduler{}
	old, ok := slot.Swap(replacement)
	if !ok {
		t.Fatal("expected old value from Swap")
	}
	if _, ok := old.(policy.DirectScheduler); !ok {
		t.Fatalf("expected old DirectScheduler, got %T", old)
	}
	current, ok = slot.Get()
	if !ok || current != replacement {
		t.Fatal("expected replacement scheduler")
	}
}

func TestDirectSchedulerRunsInline(t *testing.T) {
	called := false
	err := (policy.DirectScheduler{}).Submit(context.Background(), "session", func(context.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}
	if !called {
		t.Fatal("expected DirectScheduler to run the task inline")
	}
}

func TestRegistryDefaultsToDirectScheduler(t *testing.T) {
	registry := policy.NewRegistry()
	scheduler, ok := registry.SessionScheduler.Get()
	if !ok || scheduler == nil {
		t.Fatal("expected default scheduler")
	}
	if _, ok := scheduler.(policy.DirectScheduler); !ok {
		t.Fatalf("expected DirectScheduler, got %T", scheduler)
	}
	if stats := registry.SessionSchedulerStats(); stats != nil {
		t.Fatalf("expected nil compatibility stats, got %v", stats)
	}
}

func TestRegistryReplaceAndCloseScheduler(t *testing.T) {
	registry := policy.NewRegistry()
	replacement := &fakePolicyScheduler{stats: "snapshot"}
	old, ok := registry.SessionScheduler.Swap(replacement)
	if !ok {
		t.Fatal("expected old scheduler")
	}
	if _, ok := old.(policy.DirectScheduler); !ok {
		t.Fatalf("expected old DirectScheduler, got %T", old)
	}

	called := false
	if err := registry.SessionSchedulerStats().(string); err != "snapshot" {
		t.Fatalf("unexpected stats: %v", registry.SessionSchedulerStats())
	}
	current, _ := registry.SessionScheduler.Get()
	if err := current.Submit(context.Background(), "session", func(context.Context) error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("Submit failed: %v", err)
	}
	if !called {
		t.Fatal("expected replacement scheduler task to run")
	}

	if err := registry.Close(context.Background()); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if replacement.shutdown != 1 {
		t.Fatalf("expected scheduler shutdown once, got %d", replacement.shutdown)
	}
}

func TestRegistryTypedComponentSlot(t *testing.T) {
	registry := policy.NewRegistry()
	slot, ok := policy.RegisterSlot[testPolicyComponent](registry, policy.EventDispatcherKey)
	if !ok {
		t.Fatal("expected empty placeholder to upgrade to typed slot")
	}
	component := &fakePolicyComponent{}
	slot.Set(component)
	lookedUp, ok := policy.LookupSlot[testPolicyComponent](registry, policy.EventDispatcherKey)
	if !ok {
		t.Fatal("expected typed slot lookup")
	}
	current, ok := lookedUp.Get()
	if !ok || current != component {
		t.Fatal("expected typed component")
	}

	if err := registry.Close(context.Background()); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !component.closed {
		t.Fatal("expected component shutdown")
	}
}
