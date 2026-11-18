package committer

// native 实现：每个会话一条独立车道，车道内按到达序号定序、车道间并发执行。
//
// 与 compatibility 的区别只在调度：单会话严格保序，跨会话不再互相等待。
// 车道按需创建、排空空闲后回收，回收时把序号水位留在注册表，重开时继续。

import (
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	// nativePendingWindow 单会话乱序等待窗口上限；超限时跳过缺口，避免永久等待
	nativePendingWindow = 256
	// nativeGapWait 缺口最长等待时间；异常丢号不能阻塞该会话后续事件
	nativeGapWait = 30 * time.Second
	// nativeIdleTTL 车道空闲回收时间，只保留水位不给静默会话常驻协程
	nativeIdleTTL = 10 * time.Minute
)

type laneItem struct {
	seq uint64
	run func()
}

// Native 按会话并行、会话内定序提交事件。
type Native struct {
	mu     sync.Mutex
	lanes  map[string]*nativeLane
	water  map[string]uint64
	closed bool
	wg     sync.WaitGroup
}

// nativeLane 一条会话车道；queue 与 retired 由 mu 保护，pending/next 只由运行器读写。
type nativeLane struct {
	key     string
	mu      sync.Mutex
	wake    chan struct{}
	queue   []laneItem
	pending map[uint64]func()
	next    uint64
	retired bool
}

// NewNative 创建原生提交器。
func NewNative() *Native {
	return &Native{lanes: make(map[string]*nativeLane), water: make(map[string]uint64)}
}

// Submit 提交一条事件；同一会话按 seq 定序，seq 为 0 时同步执行。
func (n *Native) Submit(kind string, targetID int64, seq uint64, run func()) {
	if n == nil || run == nil {
		return
	}
	if seq == 0 {
		runSafely(run)
		return
	}
	n.enqueue(laneKey(kind, targetID), laneItem{seq: seq, run: run})
}

// Skip 消费一个不会产生实际处理的序号，避免车道等待永久缺口。
func (n *Native) Skip(kind string, targetID int64, seq uint64) {
	if n == nil || seq == 0 {
		return
	}
	n.enqueue(laneKey(kind, targetID), laneItem{seq: seq})
}

// Shutdown 停止接收并等待全部车道排空退出。
func (n *Native) Shutdown() {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.closed = true
	lanes := make([]*nativeLane, 0, len(n.lanes))
	for _, l := range n.lanes {
		lanes = append(lanes, l)
	}
	n.mu.Unlock()
	for _, l := range lanes {
		l.mu.Lock()
		l.retired = true
		l.mu.Unlock()
		l.signal()
	}
	n.wg.Wait()
}

// enqueue 追加事件并确保该车道有运行器。
func (n *Native) enqueue(key string, item laneItem) {
	l, created := n.lane(key)
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.retired {
		l.mu.Unlock()
		if created {
			// 新建即被关停标记：交给运行器完成回收与计数归还
			go n.runLane(l)
		}
		return
	}
	l.queue = append(l.queue, item)
	l.mu.Unlock()
	l.signal()
	if created {
		go n.runLane(l)
	}
}

// lane 取或新建车道；新建时延续该会话上次回收时的水位并预登记运行器。
func (n *Native) lane(key string) (*nativeLane, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, false
	}
	if existing := n.lanes[key]; existing != nil {
		return existing, false
	}
	next := n.water[key]
	if next == 0 {
		next = 1
	}
	l := &nativeLane{key: key, wake: make(chan struct{}, 1), pending: make(map[uint64]func()), next: next}
	n.lanes[key] = l
	n.wg.Add(1)
	return l, true
}

// runLane 独占消费一条车道，排空或空闲回收后退出。
func (n *Native) runLane(l *nativeLane) {
	defer n.wg.Done()
	for {
		l.mu.Lock()
		if len(l.queue) == 0 {
			if l.retired {
				l.mu.Unlock()
				n.drainLane(l)
				if n.retireLane(l) {
					return
				}
				continue
			}
			if len(l.pending) > 0 {
				l.mu.Unlock()
				select {
				case <-l.wake:
				case <-time.After(nativeGapWait):
					n.flushGap(l)
				}
				continue
			}
			l.mu.Unlock()
			timer := time.NewTimer(nativeIdleTTL)
			select {
			case <-l.wake:
			case <-timer.C:
			}
			timer.Stop()
			l.mu.Lock()
			idle := len(l.queue) == 0 && len(l.pending) == 0 && !l.retired
			l.mu.Unlock()
			if idle && n.retireLane(l) {
				return
			}
			continue
		}
		item := l.queue[0]
		l.queue[0] = laneItem{}
		l.queue = l.queue[1:]
		l.mu.Unlock()
		n.deliver(l, item)
	}
}

// retireLane 在排空后注销车道并记下水位，新车道从同一水位继续。
func (n *Native) retireLane(l *nativeLane) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.queue) != 0 {
		return false
	}
	n.mu.Lock()
	if n.lanes[l.key] == l {
		delete(n.lanes, l.key)
		n.water[l.key] = l.next
	}
	n.mu.Unlock()
	l.retired = true
	return true
}

// deliver 按到达序号提交；seq 落后于水位说明已被跳过，直接丢弃。
func (n *Native) deliver(l *nativeLane, item laneItem) {
	for {
		switch {
		case item.seq < l.next:
			n.flushPending(l)
			return
		case item.seq == l.next:
			if item.run != nil {
				runSafely(item.run)
			}
			l.next++
			n.flushPending(l)
			return
		case len(l.pending) < nativePendingWindow:
			l.pending[item.seq] = item.run
			return
		default:
			// 窗口已满：跳过缺口，保证后续事件不被永久阻塞
			if !n.skipNearest(l, item.seq) {
				return
			}
		}
	}
}

func (n *Native) flushPending(l *nativeLane) {
	for {
		run, ok := l.pending[l.next]
		if !ok {
			return
		}
		delete(l.pending, l.next)
		if run != nil {
			runSafely(run)
		}
		l.next++
	}
}

// skipNearest 跳过最接近水位的缺口；到达项更接近时直接推进到到达项。
func (n *Native) skipNearest(l *nativeLane, seq uint64) bool {
	oldest, ok := minPendingSeq(l.pending)
	if !ok {
		return false
	}
	if seq < oldest {
		zap.L().Warn("入站提交定序等待窗口溢出，跳过缺口提交",
			zap.String("lane", l.key), zap.Uint64("skipped_from", l.next), zap.Uint64("seq", seq))
		l.next = seq
		return true
	}
	delete(l.pending, oldest)
	zap.L().Error("入站提交定序等待窗口溢出，丢弃最旧等待项",
		zap.String("lane", l.key), zap.Uint64("dropped_seq", oldest), zap.Uint64("watermark", oldest+1))
	l.next = oldest + 1
	return true
}

// flushGap 等待超时后推进水位到最旧等待项，再连续提交已就绪项。
func (n *Native) flushGap(l *nativeLane) {
	oldest, ok := minPendingSeq(l.pending)
	if !ok {
		return
	}
	if oldest > l.next {
		zap.L().Warn("入站提交定序等待超时，跳过缺口",
			zap.String("lane", l.key), zap.Uint64("skipped_from", l.next), zap.Uint64("seq", oldest))
		l.next = oldest
	}
	n.flushPending(l)
}

// drainLane 关停时按序号升序补齐全部等待项，避免停机丢消息。
func (n *Native) drainLane(l *nativeLane) {
	for {
		oldest, ok := minPendingSeq(l.pending)
		if !ok {
			return
		}
		l.next = oldest
		n.flushPending(l)
	}
}

func (l *nativeLane) signal() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func minPendingSeq(pending map[uint64]func()) (uint64, bool) {
	oldest := ^uint64(0)
	for seq := range pending {
		if seq < oldest {
			oldest = seq
		}
	}
	return oldest, oldest != ^uint64(0)
}

func laneKey(kind string, targetID int64) string {
	return kind + ":" + strconv.FormatInt(targetID, 10)
}
