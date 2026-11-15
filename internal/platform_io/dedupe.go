package platformio

import (
	"time"

	"github.com/jellydator/ttlcache/v3"
)

// MessageDeduplicator 基于 TTL 的入站消息去重，对齐 MaiBot platform_io.dedupe.MessageDeduplicator。
//
// Python 用 dict + heapq 维护「去重键 -> 过期时刻」，Go 侧交给 ttlcache：
//   - TTL 从标记时刻计算，重复键不续期（WithDisableTouchOnHit）
//   - 容量满时按最久未标记淘汰；重复键从不重写，因此 LRU 顺序即过期顺序，
//     与 Python 淘汰「最早过期的有效键」等价
//   - 过期项在命中检查或容量压力时被清除，不需要后台清理 goroutine
//
// 单调时钟：ttlcache 用 time.Now() 计算 expiresAt，两个 Time 值都带单调读数，
// 比较时走单调时钟，因此 NTP 校时或手工改表不会打乱 TTL 窗口，
// 与 Python 的 time.monotonic() 语义一致。
type MessageDeduplicator struct {
	items *ttlcache.Cache[string, struct{}]
}

// NewMessageDeduplicator 创建去重器
func NewMessageDeduplicator(ttl time.Duration, maxEntries int) (*MessageDeduplicator, error) {
	if ttl <= 0 {
		return nil, errInvalidTTL
	}
	if maxEntries <= 0 {
		return nil, errInvalidMaxEntries
	}
	return &MessageDeduplicator{items: ttlcache.New(
		ttlcache.WithTTL[string, struct{}](ttl),
		ttlcache.WithCapacity[string, struct{}](uint64(maxEntries)),
		ttlcache.WithDisableTouchOnHit[string, struct{}](),
	)}, nil
}

// DefaultDeduplicator 与 MaiBot 默认参数一致：300s、10000 条
func DefaultDeduplicator() (*MessageDeduplicator, error) {
	return NewMessageDeduplicator(300*time.Second, 10000)
}

// MarkSeen 标记去重键；首次见到返回 true，重复返回 false
func (d *MessageDeduplicator) MarkSeen(dedupeKey string) bool {
	if d == nil || d.items == nil {
		return true
	}
	if d.items.Has(dedupeKey) {
		return false
	}
	// 过期但尚未清理的旧条目先删除，确保重新标记的键回到 LRU 队首，
	// 与 Python 先 purge 再插入的行为一致。
	d.items.Delete(dedupeKey)
	d.items.Set(dedupeKey, struct{}{}, ttlcache.DefaultTTL)
	return true
}

// Clear 清空缓存
func (d *MessageDeduplicator) Clear() {
	if d == nil || d.items == nil {
		return
	}
	d.items.DeleteAll()
}
