package test

import (
	"context"
	"testing"
	"time"

	"MaiGo/internal/maisaka"
)

func TestMaisakaBackgroundTurnKeepsCallerContextAndWaitsForCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type testKey struct{}
	ctx = context.WithValue(ctx, testKey{}, "session")
	loop := maisaka.NewTurnLoop(ctx, 1)
	defer loop.Stop()
	started := make(chan struct{})
	finished := make(chan bool, 1)
	release := make(chan struct{})
	go func() {
		finished <- loop.Run(ctx, func(callCtx context.Context) error {
			if callCtx.Value(testKey{}) != "session" {
				t.Error("session context was lost")
			}
			close(started)
			select {
			case <-release:
			case <-callCtx.Done():
			}
			return nil
		})
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("turn did not start")
	}
	select {
	case <-finished:
		t.Error("caller returned before the turn completed")
	default:
	}
	close(release)
	select {
	case accepted := <-finished:
		if !accepted {
			t.Error("turn was dropped")
		}
	case <-ctx.Done():
		t.Error("caller did not resume when the turn completed")
	}
}
