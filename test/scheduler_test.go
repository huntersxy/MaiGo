package test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"maibot-go/internal/runtime/execution/scheduler"
)

func TestSchedulerPerSessionFIFOAndCrossSessionParallel(t *testing.T) {
	engine := scheduler.New(context.Background(), scheduler.Options{
		Workers:   2,
		QueueSize: 32,
	})
	t.Cleanup(func() {
		_ = engine.Shutdown(context.Background())
	})

	var mu sync.Mutex
	orders := map[string][]int{}
	var completed atomic.Int32
	done := make(chan struct{})

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	record := func(session string, index int) {
		mu.Lock()
		orders[session] = append(orders[session], index)
		mu.Unlock()
		if completed.Add(1) == 10 {
			close(done)
		}
	}

	submit := func(session string, index int) {
		task := func(context.Context) error {
			if index == 0 {
				started <- struct{}{}
				<-release
			}
			record(session, index)
			return nil
		}
		if err := engine.Submit(context.Background(), session, task); err != nil {
			t.Errorf("submit %s/%d: %v", session, index, err)
		}
	}

	// 两个会话的队首任务会同时进入工作池，证明跨会话并行。
	for index := 0; index < 5; index++ {
		submit("session-a", index)
		submit("session-b", index)
	}
	// 队首任务在记录顺序前等待，确保两个会话确实同时开始。
	// 这里改用额外屏障任务会打乱顺序，因此直接依赖 Workers=2 与阻塞 Submit 之间的调度。
	// 为了让屏障生效，提交时把 index==0 的等待放在 record 之后不影响顺序验证。
	<-started
	<-started
	close(release)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tasks did not finish")
	}

	for _, session := range []string{"session-a", "session-b"} {
		got := orders[session]
		if len(got) != 5 {
			t.Fatalf("%s: expected 5 tasks, got %v", session, got)
		}
		for index, value := range got {
			if value != index {
				t.Fatalf("%s: expected FIFO order, got %v", session, got)
			}
		}
	}
}

func TestSchedulerSingleTaskPerSession(t *testing.T) {
	engine := scheduler.New(context.Background(), scheduler.Options{
		Workers:   4,
		QueueSize: 64,
	})
	t.Cleanup(func() {
		_ = engine.Shutdown(context.Background())
	})

	var inflight atomic.Int32
	var maxInflight atomic.Int32
	var completed atomic.Int32
	const total = 20
	done := make(chan struct{})

	for index := 0; index < total; index++ {
		if err := engine.Submit(context.Background(), "same-session", func(context.Context) error {
			current := inflight.Add(1)
			for {
				observed := maxInflight.Load()
				if current <= observed || maxInflight.CompareAndSwap(observed, current) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			inflight.Add(-1)
			if completed.Add(1) == total {
				close(done)
			}
			return nil
		}); err != nil {
			t.Fatalf("submit: %v", err)
		}
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tasks did not finish")
	}
	if got := maxInflight.Load(); got != 1 {
		t.Fatalf("expected at most one task per session, got %d", got)
	}
}

func TestSchedulerQueueCapacityAndCancel(t *testing.T) {
	engine := scheduler.New(context.Background(), scheduler.Options{
		Workers:   1,
		QueueSize: 2,
	})
	t.Cleanup(func() {
		_ = engine.Shutdown(context.Background())
	})

	started := make(chan struct{})
	release := make(chan struct{})
	if err := engine.Submit(context.Background(), "session", func(context.Context) error {
		close(started)
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	<-started

	var wg sync.WaitGroup
	wg.Add(2)
	for index := 0; index < 2; index++ {
		if err := engine.Submit(context.Background(), "session", func(context.Context) error {
			defer wg.Done()
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	// 邮箱已满，下一次提交应被 context 取消。
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := engine.Submit(ctx, "session", func(context.Context) error {
		return nil
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if engine.Stats().Rejected == 0 {
		t.Fatal("expected a rejected submission")
	}

	close(release)
	wg.Wait()
}

func TestSchedulerShutdownDrains(t *testing.T) {
	engine := scheduler.New(context.Background(), scheduler.Options{
		Workers:   2,
		QueueSize: 128,
	})

	var completed atomic.Int32
	const total = 20
	for index := 0; index < total; index++ {
		session := []string{"a", "b", "c", "d"}[index%4]
		if err := engine.Submit(context.Background(), session, func(context.Context) error {
			time.Sleep(time.Millisecond)
			completed.Add(1)
			return nil
		}); err != nil {
			t.Fatalf("submit: %v", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if got := completed.Load(); got != total {
		t.Fatalf("expected %d drained tasks, got %d", total, got)
	}
	if err := engine.Submit(context.Background(), "a", func(context.Context) error { return nil }); !errors.Is(err, scheduler.ErrClosed) {
		t.Fatalf("expected ErrClosed after shutdown, got %v", err)
	}

	stats := engine.Stats()
	if stats.Submitted != total || stats.Completed != total {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestSchedulerTaskErrorAndPanic(t *testing.T) {
	var mu sync.Mutex
	var reported []string
	engine := scheduler.New(context.Background(), scheduler.Options{
		Workers:   2,
		QueueSize: 8,
		OnError: func(sessionKey string, err error) {
			mu.Lock()
			reported = append(reported, sessionKey+":"+err.Error())
			mu.Unlock()
		},
	})
	t.Cleanup(func() {
		_ = engine.Shutdown(context.Background())
	})

	next := make(chan struct{})
	if err := engine.Submit(context.Background(), "session", func(context.Context) error {
		panic("boom")
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Submit(context.Background(), "session", func(context.Context) error {
		close(next)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-next:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not continue after panic")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(reported) != 1 || !strings.Contains(reported[0], "panic") {
		t.Fatalf("expected one panic report, got %v", reported)
	}
}

func TestSchedulerShutdownTimeoutDoesNotBlockSecondClose(t *testing.T) {
	engine := scheduler.New(context.Background(), scheduler.Options{
		Workers:   1,
		QueueSize: 4,
	})

	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	if err := engine.Submit(context.Background(), "session", func(context.Context) error {
		close(started)
		<-release
		close(finished)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	<-started

	firstCtx, firstCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer firstCancel()
	if err := engine.Shutdown(firstCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected first shutdown timeout, got %v", err)
	}

	secondCtx, secondCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer secondCancel()
	start := time.Now()
	if err := engine.Shutdown(secondCtx); err != nil {
		t.Fatalf("second shutdown failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("second shutdown blocked for %s", elapsed)
	}

	close(release)
	<-finished
}

func TestSchedulerStatsSnapshot(t *testing.T) {
	engine := scheduler.New(context.Background(), scheduler.Options{
		Workers:   2,
		QueueSize: 8,
	})
	t.Cleanup(func() {
		_ = engine.Shutdown(context.Background())
	})

	stats := engine.Stats()
	if stats.Workers != 2 {
		t.Fatalf("expected 2 workers, got %d", stats.Workers)
	}
	if stats.ActiveSessions != 0 || stats.QueuedTasks != 0 || stats.RunningTasks != 0 {
		t.Fatalf("expected empty stats, got %+v", stats)
	}
}
