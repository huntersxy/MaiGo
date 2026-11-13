package learners

import "sync"

const MaxPendingLearningMessages = 100

// PendingLearningBuffer 对齐 src/learners/pending_learning_buffer.py 的去重缓冲
// 超限时淘汰最旧素材，避免长期积压
type PendingLearningBuffer[T any] struct {
	mu      sync.Mutex
	keyFn   func(T) string
	items   []T
	seen    map[string]struct{}
	dropped int
}

func NewPendingLearningBuffer[T any](keyFn func(T) string) *PendingLearningBuffer[T] {
	return &PendingLearningBuffer[T]{
		keyFn: keyFn,
		seen:  map[string]struct{}{},
	}
}

// Extend 追加素材并按消息 ID 去重，超过上限时淘汰最旧一条
func (b *PendingLearningBuffer[T]) Extend(items []T) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, item := range items {
		key := b.keyFn(item)
		if _, ok := b.seen[key]; ok {
			continue
		}
		b.seen[key] = struct{}{}
		b.items = append(b.items, item)
		if len(b.items) > MaxPendingLearningMessages {
			oldest := b.items[0]
			b.items = b.items[1:]
			delete(b.seen, b.keyFn(oldest))
			b.dropped++
		}
	}
}

func (b *PendingLearningBuffer[T]) Snapshot() []T {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]T, len(b.items))
	copy(out, b.items)
	return out
}

// Discard 只消费已取得执行名额的批次，不影响运行期间收进来的新素材
func (b *PendingLearningBuffer[T]) Discard(items []T) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	remove := map[string]struct{}{}
	for _, item := range items {
		remove[b.keyFn(item)] = struct{}{}
	}
	if len(remove) > 0 {
		next := make([]T, 0, len(b.items))
		nextSeen := map[string]struct{}{}
		for _, item := range b.items {
			key := b.keyFn(item)
			if _, drop := remove[key]; drop {
				continue
			}
			next = append(next, item)
			nextSeen[key] = struct{}{}
		}
		b.items = next
		b.seen = nextSeen
	}
	b.dropped = 0
}

func (b *PendingLearningBuffer[T]) Len() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.items)
}

func (b *PendingLearningBuffer[T]) Clear() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items = nil
	b.seen = map[string]struct{}{}
	b.dropped = 0
}

// DroppedCount 返回因超限被淘汰的素材数量
func (b *PendingLearningBuffer[T]) DroppedCount() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

// WaitingDetail 生成等待文案，对齐 pending_learning_buffer.waiting_detail
func (b *PendingLearningBuffer[T]) WaitingDetail(minMessages int) string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	reason := "等待下次裁切补足"
	if len(b.items) >= minMessages {
		reason = "已达到门槛，等待执行条件允许"
	}
	detail := "已累计 " + itoa(len(b.items)) + "/" + itoa(minMessages) + " 条有效消息，" + reason +
		"（最多保留 " + itoa(MaxPendingLearningMessages) + " 条）"
	if b.dropped > 0 {
		detail += "；已淘汰最旧的 " + itoa(b.dropped) + " 条素材"
	}
	return detail
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
