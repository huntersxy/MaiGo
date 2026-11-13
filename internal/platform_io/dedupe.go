package platformio

import (
	"container/heap"
	"sync"
	"time"
)

// monotonicBase 单调时钟基准
//
// 对齐 Python dedupe.py:78 的 time.monotonic()：系统时间被 NTP 校时或手工调整时，
// 墙钟会跳变导致 TTL 窗口错乱，单调时钟不受影响。
var monotonicBase = time.Now()

// monotonicNow 返回自基准起的单调时长，替代 Python 的 time.monotonic()
func monotonicNow() time.Duration { return time.Since(monotonicBase) }

// MessageDeduplicator 基于 TTL 的入站消息去重，对齐 MaiBot platform_io.dedupe.MessageDeduplicator
type MessageDeduplicator struct {
	ttl        time.Duration
	maxEntries int

	expireHeap expireHeap
	seen       map[string]time.Duration

	// mu 保护 seen 与 expireHeap
	//
	// Python 在 asyncio 单线程里无需加锁；Go 的入站消息可能由多个适配器
	// goroutine 并发投递，缺锁会导致 map 并发读写 panic。
	mu sync.Mutex
}

// NewMessageDeduplicator 创建去重器
func NewMessageDeduplicator(ttl time.Duration, maxEntries int) (*MessageDeduplicator, error) {
	if ttl <= 0 {
		return nil, errInvalidTTL
	}
	if maxEntries <= 0 {
		return nil, errInvalidMaxEntries
	}
	return &MessageDeduplicator{
		ttl:        ttl,
		maxEntries: maxEntries,
		seen:       make(map[string]time.Duration),
	}, nil
}

// DefaultDeduplicator 与 MaiBot 默认参数一致：300s、10000 条
func DefaultDeduplicator() (*MessageDeduplicator, error) {
	return NewMessageDeduplicator(300*time.Second, 10000)
}

// MarkSeen 标记去重键；首次见到返回 true，重复返回 false
func (d *MessageDeduplicator) MarkSeen(dedupeKey string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := monotonicNow()
	d.purgeExpired(now)

	if expiresAt, ok := d.seen[dedupeKey]; ok && expiresAt > now {
		return false
	}

	if len(d.seen) >= d.maxEntries {
		d.evictEarliestLive()
	}

	expiresAt := now + d.ttl
	d.seen[dedupeKey] = expiresAt
	heap.Push(&d.expireHeap, expireEntry{expiresAt: expiresAt, key: dedupeKey})
	return true
}

// Clear 清空缓存
func (d *MessageDeduplicator) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.expireHeap = nil
	d.seen = make(map[string]time.Duration)
}

func (d *MessageDeduplicator) purgeExpired(now time.Duration) {
	for len(d.expireHeap) > 0 && d.expireHeap[0].expiresAt <= now {
		entry := heap.Pop(&d.expireHeap).(expireEntry)
		current, ok := d.seen[entry.key]
		if !ok {
			continue
		}
		if current != entry.expiresAt {
			continue
		}
		delete(d.seen, entry.key)
	}
}

func (d *MessageDeduplicator) evictEarliestLive() {
	for len(d.expireHeap) > 0 {
		entry := heap.Pop(&d.expireHeap).(expireEntry)
		current, ok := d.seen[entry.key]
		if !ok {
			continue
		}
		if current != entry.expiresAt {
			continue
		}
		delete(d.seen, entry.key)
		return
	}
}

type expireEntry struct {
	expiresAt time.Duration
	key       string
}

type expireHeap []expireEntry

func (h expireHeap) Len() int           { return len(h) }
func (h expireHeap) Less(i, j int) bool { return h[i].expiresAt < h[j].expiresAt }
func (h expireHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *expireHeap) Push(x any) {
	*h = append(*h, x.(expireEntry))
}

func (h *expireHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
