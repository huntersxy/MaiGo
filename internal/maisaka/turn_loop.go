package maisaka

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"

	"go.uber.org/zap"
)

// TurnJob 一次后台思考任务；由会话级 TurnLoop 串行执行
type TurnJob func(ctx context.Context) error

// TurnLoop 会话级独立后台 RunLoop，保证同一会话的思考轮次严格串行
//
// 设计目标：把每轮一次性执行的 RunTurnOnce 从入站事件处理路径移出，同时保留
// 单轮同步回退能力。任务上下文由提交方持有，Stop 只停止调度，不强制取消正在执行的模型调用
type TurnLoop struct {
	ctx    context.Context
	cancel context.CancelFunc
	queue  chan TurnJob

	mu      sync.Mutex
	started bool
	stopped bool
	wg      sync.WaitGroup
}

// NewTurnLoop 创建会话级后台循环；capacity 小于 1 时按 4 处理
func NewTurnLoop(parent context.Context, capacity int) *TurnLoop {
	if parent == nil {
		parent = context.Background()
	}
	if capacity < 1 {
		capacity = 4
	}
	ctx, cancel := context.WithCancel(parent)
	return &TurnLoop{ctx: ctx, cancel: cancel, queue: make(chan TurnJob, capacity)}
}

// Start 惰性启动后台协程，重复调用安全
func (l *TurnLoop) Start() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.started || l.stopped {
		l.mu.Unlock()
		return
	}
	l.started = true
	l.wg.Add(1)
	l.mu.Unlock()
	go l.run()
}

func (l *TurnLoop) run() {
	defer l.wg.Done()
	for {
		select {
		case <-l.ctx.Done():
			return
		case job := <-l.queue:
			if job == nil {
				continue
			}
			if err := l.runJob(job); err != nil {
				zap.L().Error("Maisaka 后台 RunLoop 任务失败", zap.Error(err))
			}
		}
	}
}

func (l *TurnLoop) runJob(job TurnJob) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("Maisaka 后台 RunLoop 任务 panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return job(l.ctx)
}

// Submit 提交一次后台任务；ctx 只用于任务归属，循环停止或 ctx 取消时返回 false
func (l *TurnLoop) Submit(ctx context.Context, job TurnJob) bool {
	if l == nil || job == nil {
		return false
	}
	l.Start()
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		return false
	}
	l.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return false
	case <-l.ctx.Done():
		return false
	case l.queue <- job:
		return true
	}
}

// Running 后台循环是否已启动且未停止
func (l *TurnLoop) Running() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.started && !l.stopped
}

// Stop 停止接受新任务并等待正在执行的任务结束
func (l *TurnLoop) Stop() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		return
	}
	l.stopped = true
	l.mu.Unlock()
	l.cancel()
	l.wg.Wait()
}
