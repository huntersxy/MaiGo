package personinfo

// 人物事实写回队列，对齐 MaiBot PersonFactWritebackService
// 队列有界、后台单 worker、最多三次指数退避重试

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	personFactWritebackQueueSize = 256
	personFactWritebackAttempts  = 3
)

// WritebackQueueStats 写回队列统计
type WritebackQueueStats struct {
	Enqueued  int64 `json:"enqueued"`
	Processed int64 `json:"processed"`
	Failed    int64 `json:"failed"`
	Dropped   int64 `json:"dropped"`
	Pending   int   `json:"pending"`
	Started   bool  `json:"started"`
	Closed    bool  `json:"closed"`
}

type writebackJob struct {
	input    AnswerWritebackInput
	attempts int
}

// WritebackQueue 人物事实写回队列
type WritebackQueue struct {
	registry    *Registry
	capacity    int
	maxAttempts int
	retryDelay  time.Duration
	ch          chan writebackJob
	stop        chan struct{}
	done        chan struct{}
	startOnce   sync.Once
	stopOnce    sync.Once
	mu          sync.Mutex
	started     bool
	closed      bool
	stats       WritebackQueueStats
}

// NewWritebackQueue 创建有界写回队列
func NewWritebackQueue(registry *Registry, capacity int) *WritebackQueue {
	if capacity <= 0 {
		capacity = personFactWritebackQueueSize
	}
	return &WritebackQueue{
		registry: registry, capacity: capacity, maxAttempts: personFactWritebackAttempts,
		retryDelay: 100 * time.Millisecond,
		ch:         make(chan writebackJob, capacity),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
}

// Start 启动后台 worker
func (q *WritebackQueue) Start() {
	q.startOnce.Do(func() {
		q.mu.Lock()
		q.started = true
		q.mu.Unlock()
		go q.worker()
	})
}

// Enqueue 非阻塞入队，队列满返回 ErrWritebackQueueFull
func (q *WritebackQueue) Enqueue(ctx context.Context, in AnswerWritebackInput) error {
	if q == nil {
		return ErrWritebackQueueClosed
	}
	q.mu.Lock()
	closed := q.closed
	q.mu.Unlock()
	if closed {
		return ErrWritebackQueueClosed
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	q.Start()
	job := writebackJob{input: cloneWritebackInput(in)}
	select {
	case q.ch <- job:
		q.mu.Lock()
		q.stats.Enqueued++
		q.mu.Unlock()
		return nil
	default:
		q.mu.Lock()
		q.stats.Dropped++
		q.mu.Unlock()
		return ErrWritebackQueueFull
	}
}

// Shutdown 停止 worker 并等待退出
func (q *WritebackQueue) Shutdown(ctx context.Context) error {
	if q == nil {
		return nil
	}
	// 未启动时也创建 worker，保证 stop/done 生命周期闭合
	q.Start()
	q.stopOnce.Do(func() {
		q.mu.Lock()
		q.closed = true
		q.mu.Unlock()
		close(q.stop)
	})
	select {
	case <-q.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stats 读取队列统计
func (q *WritebackQueue) Stats() WritebackQueueStats {
	if q == nil {
		return WritebackQueueStats{}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.stats
	out.Pending = len(q.ch)
	out.Started = q.started
	out.Closed = q.closed
	return out
}

func (q *WritebackQueue) worker() {
	defer close(q.done)
	for {
		select {
		case <-q.stop:
			return
		case job := <-q.ch:
			q.process(job)
		}
	}
}

func (q *WritebackQueue) process(job writebackJob) {
	for {
		err := q.registry.StoreMemoryPointsFromAnswer(context.Background(), job.input)
		if err == nil {
			q.mu.Lock()
			q.stats.Processed++
			q.mu.Unlock()
			return
		}
		job.attempts++
		if job.attempts >= q.maxAttempts {
			q.mu.Lock()
			q.stats.Failed++
			q.mu.Unlock()
			zap.L().Warn("人物事实写回重试耗尽",
				zap.String("person_id", job.input.PersonID),
				zap.Int("attempts", job.attempts),
				zap.Error(err))
			return
		}
		select {
		case <-q.stop:
			return
		case <-time.After(time.Duration(job.attempts) * q.retryDelay):
		}
	}
}

func cloneWritebackInput(in AnswerWritebackInput) AnswerWritebackInput {
	out := in
	out.EvidenceMessageIDs = append([]string(nil), in.EvidenceMessageIDs...)
	if in.FactClaim != nil {
		out.FactClaim = cloneMapAny(in.FactClaim)
	}
	return out
}

// WritebackQueue 返回 Registry 的默认写回队列
func (r *Registry) WritebackQueue() *WritebackQueue {
	r.writebackOnce.Do(func() {
		r.writebackQueue = NewWritebackQueue(r, personFactWritebackQueueSize)
	})
	return r.writebackQueue
}

// StartMemoryWriteback 启动默认写回队列
func (r *Registry) StartMemoryWriteback() *WritebackQueue {
	queue := r.WritebackQueue()
	queue.Start()
	return queue
}

// StopMemoryWriteback 停止默认写回队列
func (r *Registry) StopMemoryWriteback(ctx context.Context) error {
	return r.WritebackQueue().Shutdown(ctx)
}

// EnqueueMemoryWriteback 将人物事实写入默认队列
func (r *Registry) EnqueueMemoryWriteback(ctx context.Context, in AnswerWritebackInput) error {
	return r.WritebackQueue().Enqueue(ctx, in)
}

// EnqueueMemoryWriteback 全局队列便捷入口
func EnqueueMemoryWriteback(ctx context.Context, in AnswerWritebackInput) error {
	return DefaultRegistry().EnqueueMemoryWriteback(ctx, in)
}
