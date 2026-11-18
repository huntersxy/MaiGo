package system_probe

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// StatfsResult 是平台中立的 statfs 结果；字段全部为 int64。
type StatfsResult struct {
	Blocks    int64
	BFree     int64
	BAvail    int64
	Files     int64
	FFree     int64
	FSTypeID  int64
	BlockSize int64
}

// statfsLimiter 限制同时执行的 statfs 数量；槽位只在 goroutine 真正返回时释放。
type statfsLimiter struct {
	mu          sync.Mutex
	sem         chan struct{}
	total       int64
	ok          int64
	skipped     int64
	stuck       int64
	maxDuration time.Duration
}

func newStatfsLimiter(capacity int) *statfsLimiter {
	if capacity <= 0 {
		capacity = 2
	}
	return &statfsLimiter{sem: make(chan struct{}, capacity)}
}

type statfsOutcome struct {
	result StatfsResult
	err    error
}

// run 先按 ctx 排队获取槽位，再在 goroutine 中执行 fn；ctx 超时后槽位保持占用并计入 stuck。
func (l *statfsLimiter) run(ctx context.Context, fn func() (StatfsResult, error)) (StatfsResult, bool, error) {
	if l == nil || fn == nil {
		return StatfsResult{}, true, nil
	}
	l.mu.Lock()
	l.total++
	l.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		l.mu.Lock()
		l.skipped++
		l.mu.Unlock()
		return StatfsResult{}, true, err
	}
	select {
	case l.sem <- struct{}{}:
	case <-ctx.Done():
		l.mu.Lock()
		l.skipped++
		l.mu.Unlock()
		return StatfsResult{}, true, ctx.Err()
	}
	start := time.Now()
	done := make(chan statfsOutcome, 1)
	go func() {
		out := statfsOutcome{}
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					out.err = fmt.Errorf("statfs panic: %v", recovered)
				}
			}()
			out.result, out.err = fn()
		}()
		<-l.sem
		done <- out
	}()
	select {
	case out := <-done:
		duration := time.Since(start)
		l.mu.Lock()
		l.ok++
		if duration > l.maxDuration {
			l.maxDuration = duration
		}
		l.mu.Unlock()
		return out.result, false, out.err
	case <-ctx.Done():
		l.mu.Lock()
		l.stuck++
		l.mu.Unlock()
		return StatfsResult{}, true, ctx.Err()
	}
}

func (l *statfsLimiter) stats() LimiterStats {
	if l == nil {
		return LimiterStats{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return LimiterStats{
		Capacity:      cap(l.sem),
		Total:         l.total,
		OK:            l.ok,
		Skipped:       l.skipped,
		Stuck:         l.stuck,
		MaxDurationMS: l.maxDuration.Milliseconds(),
	}
}
