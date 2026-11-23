package test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"MaiGo/internal/runtime/execution"
	"MaiGo/internal/runtime/execution/committer"
	"MaiGo/internal/runtime/execution/policy"
	"MaiGo/internal/runtime/execution/scheduler"
)

func TestExecutionSwitchDrainsQueuedTasksAndKeepsFileOnTimeout(t *testing.T) {
	t.Setenv(execution.EnvExecutionMode, "")
	dir := t.TempDir()
	writeRuntimeToml(t, dir, "[execution]\nmode = \"native\"\nworker_pool_size = 1\n[features]\nnative_session_scheduler = true\nnative_message_committer = true\n")
	controller := liveGoSettings(t, dir).ExecutionSettings
	registry := controller.Policies()
	initial := registry.Snapshot()
	path := execution.RuntimeTomlPath(dir)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	started, releaseTask := make(chan struct{}), make(chan struct{})
	defer close(releaseTask)
	if err := registry.SubmitSession(t.Context(), "group:1", func(ctx context.Context) error {
		close(started)
		select {
		case <-releaseTask:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}); err != nil {
		t.Fatal(err)
	}
	waitPolicyResult(t, started)
	queued := make(chan error, 1)
	if err := registry.SubmitSession(t.Context(), "group:1", func(ctx context.Context) error {
		implementation, _, release, err := registry.MessageCommitter.Acquire(ctx)
		if err == nil {
			if _, ok := implementation.(*committer.Native); !ok {
				err = errors.New("queued task saw a different committer")
			}
			release()
		}
		queued <- err
		return err
	}); err != nil {
		t.Fatal(err)
	}
	compatibility := func(profile *execution.Profile, _ *execution.FeatureToggles) error {
		profile.Mode = execution.ModeCompatibility
		return nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, err = controller.Update(ctx, compatibility)
	after, readErr := os.ReadFile(path)
	if !errors.Is(err, context.DeadlineExceeded) || readErr != nil || !bytes.Equal(before, after) || registry.Snapshot() != initial {
		t.Fatalf("unexpected switch timeout or changed state: switch=%v read=%v", err, readErr)
	}
	result := make(chan error, 1)
	go func() {
		_, err := controller.Update(t.Context(), compatibility)
		result <- err
	}()
	releaseTask <- struct{}{}
	if err := waitPolicyResult(t, queued); err != nil {
		t.Fatal(err)
	}
	if err := waitPolicyResult(t, result); err != nil {
		t.Fatal(err)
	}
	snapshot := registry.Snapshot()
	activeScheduler, _ := registry.SessionScheduler.From(snapshot)
	activeCommitter, _ := registry.MessageCommitter.From(snapshot)
	if _, ok := activeScheduler.(policy.DirectScheduler); !ok {
		t.Fatalf("got %T, want baseline scheduler", activeScheduler)
	}
	if _, ok := activeCommitter.(policy.InlineCommitter); !ok {
		t.Fatalf("got %T, want baseline committer", activeCommitter)
	}
	state := controller.Status()
	if !state.Features.NativeSessionScheduler || !state.Features.NativeMessageCommitter {
		t.Fatal("global switch erased local preferences")
	}
}

func TestExecutionSwitchLocalToggleAndReenableCommitter(t *testing.T) {
	t.Setenv(execution.EnvExecutionMode, "")
	dir := t.TempDir()
	writeRuntimeToml(t, dir, "[execution]\nmode = \"native\"\nworker_pool_size = 2\n[features]\nnative_session_scheduler = true\nnative_message_committer = true\n")
	controller := liveGoSettings(t, dir).ExecutionSettings
	registry := controller.Policies()
	oldScheduler, _ := registry.SessionScheduler.Get()
	oldCommitter, _ := registry.MessageCommitter.Get()
	for _, enabled := range []bool{true, false, true} {
		state, err := controller.Update(t.Context(), func(_ *execution.Profile, toggles *execution.FeatureToggles) error {
			toggles.NativeMessageCommitter = enabled
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		currentScheduler, _ := registry.SessionScheduler.Get()
		if currentScheduler != oldScheduler || state.SessionScheduler.(scheduler.Stats).Workers != 2 {
			t.Fatal("committer toggle recreated the scheduler")
		}
		implementation, _, release, err := registry.MessageCommitter.Acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		_, native := implementation.(*committer.Native)
		if native != enabled {
			release()
			t.Fatalf("committer native=%v, want %v", native, enabled)
		}
		finish, err := implementation.Reserve("group", 1)
		if err != nil {
			release()
			t.Fatal(err)
		}
		done := make(chan struct{})
		finish(func() {
			release()
			close(done)
		})
		waitPolicyResult(t, done)
	}
	newCommitter, _ := registry.MessageCommitter.Get()
	if oldCommitter == newCommitter {
		t.Fatal("expected a newly enabled committer")
	}
}
