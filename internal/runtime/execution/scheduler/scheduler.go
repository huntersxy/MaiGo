// Package scheduler 提供按会话 FIFO、跨会话有界并发的任务调度器。
//
// 它对应 RUNTIME_MODES.md 阶段 2 的 native session scheduler：
//   - 每个会话一条 FIFO 邮箱，同一会话同时只执行一个任务；
//   - 跨会话任务交给固定大小的工作池，避免每条消息一个无界 goroutine；
//   - 邮箱满时阻塞提交方，由调用方 context 决定是否放弃；
//   - Shutdown 先停止接收，再排空队列，最后取消任务上下文。
//
// 调度器不感知消息、LLM 或数据库，便于单独测试与复用。
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// ErrClosed 调度器已停止接收新任务。
var ErrClosed = errors.New("scheduler: closed")

// ErrEmptySessionKey 任务没有提供会话键。
var ErrEmptySessionKey = errors.New("scheduler: empty session key")

// MaxWorkers 是工作池上限，防止误配置创建过多 goroutine。
const MaxWorkers = 4096

// DefaultQueueSize 是单个会话默认的待处理任务上限。
const DefaultQueueSize = 256

// Task 是调度器执行的任务；runCtx 由调度器持有，Shutdown 超时时会取消。
type Task func(runCtx context.Context) error

// Options 调度器参数。
type Options struct {
	// Workers 跨会话工作协程数；<=0 时使用 CPU 核心数。
	Workers int
	// QueueSize 单会话待处理任务上限；<=0 时使用 DefaultQueueSize。
	QueueSize int
	// OnError 任务返回错误或 panic 时回调；回调在任务协程内执行。
	OnError func(sessionKey string, err error)
}

// Stats 调度器运行快照，供日志与只读接口使用。
type Stats struct {
	Workers        int    `json:"workers"`
	ActiveSessions int    `json:"active_sessions"`
	QueuedTasks    int    `json:"queued_tasks"`
	RunningTasks   int    `json:"running_tasks"`
	ReadySessions  int    `json:"ready_sessions"`
	Submitted      uint64 `json:"submitted"`
	Completed      uint64 `json:"completed"`
	Failed         uint64 `json:"failed"`
	Rejected       uint64 `json:"rejected"`
}

type sessionQueue struct {
	key       string
	pending   []Task
	sem       chan struct{}
	writers   int
	scheduled bool
}

// Scheduler 按会话串行、跨会话并行地执行任务。
type Scheduler struct {
	ctx    context.Context
	cancel context.CancelFunc

	workers   int
	queueSize int
	onError   func(string, error)

	jobs   chan string
	notify chan struct{}

	mu       sync.Mutex
	sessions map[string]*sessionQueue
	ready    []string
	closing  bool
	queued   int
	running  int
	drained  chan struct{}

	drainedOnce sync.Once
	wg          sync.WaitGroup

	submitted atomic.Uint64
	completed atomic.Uint64
	failed    atomic.Uint64
	rejected  atomic.Uint64
}

// New 创建并启动调度器；parent 取消时等同进入 Shutdown。
func New(parent context.Context, opts Options) *Scheduler {
	if parent == nil {
		parent = context.Background()
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers < 1 {
		workers = 1
	}
	if workers > MaxWorkers {
		workers = MaxWorkers
	}
	queueSize := opts.QueueSize
	if queueSize <= 0 {
		queueSize = DefaultQueueSize
	}

	ctx, cancel := context.WithCancel(parent)
	s := &Scheduler{
		ctx:       ctx,
		cancel:    cancel,
		workers:   workers,
		queueSize: queueSize,
		onError:   opts.OnError,
		jobs:      make(chan string),
		notify:    make(chan struct{}, 1),
		sessions:  make(map[string]*sessionQueue),
		drained:   make(chan struct{}),
	}

	s.wg.Add(1)
	go s.dispatchLoop()
	for index := 0; index < workers; index++ {
		s.wg.Add(1)
		go s.workerLoop()
	}
	return s
}

// Workers 返回工作池大小。
func (s *Scheduler) Workers() int {
	if s == nil {
		return 0
	}
	return s.workers
}

// Submit 把任务放入会话邮箱；同一会话严格按提交顺序执行。
//
// 邮箱满时会阻塞，直到有空位、ctx 取消或调度器关闭。
func (s *Scheduler) Submit(ctx context.Context, sessionKey string, task Task) error {
	if s == nil {
		return ErrClosed
	}
	if task == nil {
		return fmt.Errorf("scheduler: nil task")
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return ErrEmptySessionKey
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		s.rejected.Add(1)
		return ErrClosed
	}
	queue := s.sessions[sessionKey]
	if queue == nil {
		queue = &sessionQueue{
			key: sessionKey,
			sem: make(chan struct{}, s.queueSize),
		}
		s.sessions[sessionKey] = queue
	}
	queue.writers++
	s.mu.Unlock()

	select {
	case queue.sem <- struct{}{}:
	case <-ctx.Done():
		s.releaseWriter(queue)
		s.rejected.Add(1)
		return ctx.Err()
	case <-s.ctx.Done():
		s.releaseWriter(queue)
		s.rejected.Add(1)
		return ErrClosed
	}

	s.mu.Lock()
	if s.closing || s.ctx.Err() != nil {
		s.mu.Unlock()
		<-queue.sem
		s.releaseWriter(queue)
		s.rejected.Add(1)
		return ErrClosed
	}
	queue.pending = append(queue.pending, task)
	s.queued++
	if !queue.scheduled {
		queue.scheduled = true
		s.ready = append(s.ready, sessionKey)
		s.signalLocked()
	}
	queue.writers--
	s.submitted.Add(1)
	s.mu.Unlock()
	return nil
}

// Shutdown 停止接收新任务，等待已提交任务排空；ctx 到期时取消任务并返回错误。
func (s *Scheduler) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	s.closing = true
	s.signalLocked()
	s.maybeDrainedLocked()
	drained := s.drained
	s.mu.Unlock()

	select {
	case <-drained:
		s.cancel()
		s.wg.Wait()
		return nil
	case <-ctx.Done():
		s.cancel()
		return ctx.Err()
	}
}

// Stats 返回当前运行快照。
func (s *Scheduler) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	s.mu.Lock()
	active := 0
	for _, queue := range s.sessions {
		if queue.scheduled || len(queue.pending) > 0 {
			active++
		}
	}
	stats := Stats{
		Workers:        s.workers,
		ActiveSessions: active,
		QueuedTasks:    s.queued,
		RunningTasks:   s.running,
		ReadySessions:  len(s.ready),
		Submitted:      s.submitted.Load(),
		Completed:      s.completed.Load(),
		Failed:         s.failed.Load(),
		Rejected:       s.rejected.Load(),
	}
	s.mu.Unlock()
	return stats
}

func (s *Scheduler) dispatchLoop() {
	defer s.wg.Done()
	for {
		s.mu.Lock()
		// 关闭后仍要继续派发 ready 中的会话，直到 ctx 在排空后取消。
		for len(s.ready) == 0 {
			if s.ctx.Err() != nil {
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()
			select {
			case <-s.notify:
			case <-s.ctx.Done():
				return
			}
			s.mu.Lock()
		}
		sessionKey := s.ready[0]
		s.ready = s.ready[1:]
		s.mu.Unlock()

		select {
		case s.jobs <- sessionKey:
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *Scheduler) workerLoop() {
	defer s.wg.Done()
	for {
		select {
		case sessionKey := <-s.jobs:
			s.runSessionTask(sessionKey)
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *Scheduler) runSessionTask(sessionKey string) {
	s.mu.Lock()
	queue := s.sessions[sessionKey]
	if queue == nil || len(queue.pending) == 0 {
		if queue != nil {
			queue.scheduled = false
			s.cleanupLocked(queue)
		}
		s.mu.Unlock()
		return
	}
	task := queue.pending[0]
	queue.pending = queue.pending[1:]
	s.queued--
	s.running++
	s.mu.Unlock()

	// 任务开始后释放邮箱容量，允许后续消息继续排队。
	<-queue.sem
	err := invokeTask(s.ctx, task)

	s.mu.Lock()
	s.running--
	if err != nil {
		s.failed.Add(1)
	} else {
		s.completed.Add(1)
	}
	if len(queue.pending) > 0 {
		s.ready = append(s.ready, sessionKey)
		s.signalLocked()
	} else {
		queue.scheduled = false
		s.cleanupLocked(queue)
	}
	s.maybeDrainedLocked()
	s.mu.Unlock()

	if err != nil && s.onError != nil {
		reportTaskError(s.onError, sessionKey, err)
	}
}

func reportTaskError(onError func(string, error), sessionKey string, err error) {
	defer func() {
		// 错误回调本身不能杀死工作协程。
		_ = recover()
	}()
	onError(sessionKey, err)
}

func invokeTask(ctx context.Context, task Task) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("scheduler: task panic: %v", recovered)
		}
	}()
	return task(ctx)
}

func (s *Scheduler) releaseWriter(queue *sessionQueue) {
	s.mu.Lock()
	if queue.writers > 0 {
		queue.writers--
	}
	s.cleanupLocked(queue)
	s.maybeDrainedLocked()
	s.mu.Unlock()
}

func (s *Scheduler) cleanupLocked(queue *sessionQueue) {
	if queue == nil {
		return
	}
	if len(queue.pending) == 0 && !queue.scheduled && queue.writers == 0 {
		if current := s.sessions[queue.key]; current == queue {
			delete(s.sessions, queue.key)
		}
	}
}

func (s *Scheduler) maybeDrainedLocked() {
	if !s.closing || s.running > 0 || s.queued > 0 {
		return
	}
	for _, queue := range s.sessions {
		if queue.writers > 0 {
			return
		}
	}
	s.drainedOnce.Do(func() {
		close(s.drained)
	})
}

func (s *Scheduler) signalLocked() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}
