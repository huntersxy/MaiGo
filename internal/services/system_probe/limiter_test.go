package system_probe

import (
	"context"
	"runtime"
	"testing"
	"time"
)

func TestStatfsLimiterSequentialReuse(t *testing.T) {
	limiter := newStatfsLimiter(2)
	for i := 0; i < 5; i++ {
		_, skipped, err := limiter.run(context.Background(), func() (StatfsResult, error) {
			return StatfsResult{Blocks: 1, BlockSize: 4096}, nil
		})
		if err != nil || skipped {
			t.Fatalf("串行调用不应跳过: i=%d skipped=%v err=%v", i, skipped, err)
		}
	}
	stats := limiter.stats()
	if stats.Capacity != 2 || stats.Total != 5 || stats.OK != 5 || stats.Skipped != 0 || stats.Stuck != 0 {
		t.Fatalf("串行统计不符: %+v", stats)
	}
	if stats.MaxDurationMS < 0 {
		t.Fatalf("max_duration_ms 不应为负: %+v", stats)
	}
}

func TestStatfsLimiterStuckSlots(t *testing.T) {
	limiter := newStatfsLimiter(2)
	block := make(chan struct{})
	blocking := func() (StatfsResult, error) {
		<-block
		return StatfsResult{}, nil
	}
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer firstCancel()
	if _, skipped, err := limiter.run(firstCtx, blocking); !skipped || err == nil {
		t.Fatalf("第一次超时应跳槽: skipped=%v err=%v", skipped, err)
	}
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer secondCancel()
	if _, skipped, err := limiter.run(secondCtx, blocking); !skipped || err == nil {
		t.Fatalf("第二次超时应跳槽: skipped=%v err=%v", skipped, err)
	}
	before := runtime.NumGoroutine()
	started := time.Now()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer waitCancel()
	_, skipped, err := limiter.run(waitCtx, blocking)
	if !skipped || err == nil {
		t.Fatalf("两个槽位均卡死时应在 ctx 到期后跳过: skipped=%v err=%v", skipped, err)
	}
	if elapsed := time.Since(started); elapsed < 20*time.Millisecond {
		t.Fatalf("槽位占满时应等待剩余超时而非立即跳过: %v", elapsed)
	}
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("等待槽位不应新增 goroutine: before=%d after=%d", before, after)
	}
	stats := limiter.stats()
	if stats.Stuck != 2 {
		t.Fatalf("stuck 应记录 2 个卡死槽位: %+v", stats)
	}
	if stats.Skipped < 1 {
		t.Fatalf("等待超时应计入 skipped: %+v", stats)
	}

	close(block)
	time.Sleep(50 * time.Millisecond)
	result, skipped, err := limiter.run(context.Background(), func() (StatfsResult, error) {
		time.Sleep(2 * time.Millisecond)
		return StatfsResult{Blocks: 8, BlockSize: 4096}, nil
	})
	if skipped || err != nil || result.Blocks != 8 {
		t.Fatalf("解除阻塞后应恢复: result=%+v skipped=%v err=%v", result, skipped, err)
	}
	stats = limiter.stats()
	if stats.MaxDurationMS < 2 {
		t.Fatalf("max_duration_ms 应记录耗时: %+v", stats)
	}
}

func TestStatfsLimiterWaitsForSlot(t *testing.T) {
	limiter := newStatfsLimiter(1)
	block := make(chan struct{})
	started := make(chan struct{})
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _, _ = limiter.run(context.Background(), func() (StatfsResult, error) {
			close(started)
			<-block
			return StatfsResult{}, nil
		})
	}()
	<-started
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	_, skipped, err := limiter.run(waitCtx, func() (StatfsResult, error) {
		return StatfsResult{Blocks: 1, BlockSize: 4096}, nil
	})
	if !skipped || err == nil {
		t.Fatalf("等待槽位超时应跳过: skipped=%v err=%v", skipped, err)
	}
	if elapsed := time.Since(startedAt); elapsed < 20*time.Millisecond {
		t.Fatalf("并发请求应排队等待而非立即 skipped: %v", elapsed)
	}
	close(block)
	<-firstDone
}
