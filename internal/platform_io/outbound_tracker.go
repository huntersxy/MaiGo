package platformio

import (
	"container/heap"
	"fmt"
	"strings"
	"sync"
	"time"
)

// PendingOutboundRecord 表示一条仍在等待完成的出站投递记录
type PendingOutboundRecord struct {
	InternalMessageID string
	RouteKey          RouteKey
	DriverID          string
	CreatedAt         float64
	ExpiresAt         float64
	Metadata          map[string]any
}

// StoredDeliveryReceipt 表示已完成并被索引的出站回执
type StoredDeliveryReceipt struct {
	Receipt   DeliveryReceipt
	StoredAt  float64
	ExpiresAt float64
}

type pendingExpireItem struct {
	expiresAt         float64
	internalMessageID string
	driverID          string
}

type receiptExpireItem struct {
	expiresAt         float64
	externalMessageID string
}

type pendingExpireHeap []pendingExpireItem

func (h pendingExpireHeap) Len() int           { return len(h) }
func (h pendingExpireHeap) Less(i, j int) bool { return h[i].expiresAt < h[j].expiresAt }
func (h pendingExpireHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *pendingExpireHeap) Push(x any)        { *h = append(*h, x.(pendingExpireItem)) }
func (h *pendingExpireHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

type receiptExpireHeap []receiptExpireItem

func (h receiptExpireHeap) Len() int           { return len(h) }
func (h receiptExpireHeap) Less(i, j int) bool { return h[i].expiresAt < h[j].expiresAt }
func (h receiptExpireHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *receiptExpireHeap) Push(x any)        { *h = append(*h, x.(receiptExpireItem)) }
func (h *receiptExpireHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// OutboundTracker 跟踪出站消息的 pending 状态与最终回执，对齐 MaiBot OutboundTracker
type OutboundTracker struct {
	mu       sync.Mutex
	ttl      time.Duration
	pending  map[string]PendingOutboundRecord
	expire   pendingExpireHeap
	receipts map[string]StoredDeliveryReceipt
	received receiptExpireHeap
}

// NewOutboundTracker 创建跟踪器；ttl 必须为正
func NewOutboundTracker(ttl time.Duration) (*OutboundTracker, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("ttl 必须大于 0")
	}
	tracker := &OutboundTracker{
		ttl:      ttl,
		pending:  make(map[string]PendingOutboundRecord),
		receipts: make(map[string]StoredDeliveryReceipt),
	}
	heap.Init(&tracker.expire)
	heap.Init(&tracker.received)
	return tracker, nil
}

// DefaultOutboundTracker 使用 1800 秒 TTL 创建跟踪器
func DefaultOutboundTracker() *OutboundTracker {
	tracker, err := NewOutboundTracker(30 * time.Minute)
	if err != nil {
		panic(err)
	}
	return tracker
}

func pendingTrackerKey(internalMessageID, driverID string) string {
	return internalMessageID + "\x00" + driverID
}

// BeginTracking 开始跟踪一次出站投递；同一消息与驱动重复 begin 返回错误
func (t *OutboundTracker) BeginTracking(internalMessageID string, routeKey RouteKey, driverID string, metadata map[string]any) (PendingOutboundRecord, error) {
	if t == nil {
		return PendingOutboundRecord{}, nil
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanupExpiredLocked(now)
	key := pendingTrackerKey(internalMessageID, driverID)
	if _, ok := t.pending[key]; ok {
		return PendingOutboundRecord{}, fmt.Errorf("消息 %s 在驱动 %s 上已存在未完成的出站跟踪记录", internalMessageID, driverID)
	}
	record := PendingOutboundRecord{
		InternalMessageID: internalMessageID,
		RouteKey:          routeKey,
		DriverID:          driverID,
		CreatedAt:         float64(now.UnixNano()) / float64(time.Second),
		ExpiresAt:         float64(now.Add(t.ttl).UnixNano()) / float64(time.Second),
		Metadata:          metadata,
	}
	t.pending[key] = record
	heap.Push(&t.expire, pendingExpireItem{expiresAt: record.ExpiresAt, internalMessageID: internalMessageID, driverID: driverID})
	return record, nil
}

// FinishTracking 用最终回执结束跟踪；若回执带外部消息 ID 则缓存索引
func (t *OutboundTracker) FinishTracking(receipt DeliveryReceipt) (PendingOutboundRecord, bool) {
	if t == nil {
		return PendingOutboundRecord{}, false
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanupExpiredLocked(now)

	var record PendingOutboundRecord
	var found bool
	if receipt.DriverID != "" {
		key := pendingTrackerKey(receipt.InternalMessageID, receipt.DriverID)
		record, found = t.pending[key]
		delete(t.pending, key)
	} else {
		var matchedKey string
		count := 0
		for key, candidate := range t.pending {
			if candidate.InternalMessageID == receipt.InternalMessageID {
				matchedKey = key
				count++
			}
		}
		if count == 1 {
			record, found = t.pending[matchedKey]
			delete(t.pending, matchedKey)
		}
	}
	if receipt.ExternalMessageID != "" {
		expiresAt := float64(now.Add(t.ttl).UnixNano()) / float64(time.Second)
		t.receipts[receipt.ExternalMessageID] = StoredDeliveryReceipt{
			Receipt:   receipt,
			StoredAt:  float64(now.UnixNano()) / float64(time.Second),
			ExpiresAt: expiresAt,
		}
		heap.Push(&t.received, receiptExpireItem{expiresAt: expiresAt, externalMessageID: receipt.ExternalMessageID})
	}
	return record, found
}

// GetPending 查询待完成记录；driverID 为空时要求唯一匹配
func (t *OutboundTracker) GetPending(internalMessageID, driverID string) (PendingOutboundRecord, bool) {
	if t == nil {
		return PendingOutboundRecord{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanupExpiredLocked(time.Now())
	if driverID != "" {
		record, ok := t.pending[pendingTrackerKey(internalMessageID, driverID)]
		return record, ok
	}
	var matched PendingOutboundRecord
	count := 0
	for _, candidate := range t.pending {
		if candidate.InternalMessageID == internalMessageID {
			matched = candidate
			count++
		}
	}
	if count == 1 {
		return matched, true
	}
	return PendingOutboundRecord{}, false
}

// GetReceiptByExternalID 按平台侧消息 ID 查询已完成回执
func (t *OutboundTracker) GetReceiptByExternalID(externalMessageID string) (DeliveryReceipt, bool) {
	if t == nil {
		return DeliveryReceipt{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanupExpiredLocked(time.Now())
	stored, ok := t.receipts[strings.TrimSpace(externalMessageID)]
	return stored.Receipt, ok
}

// Clear 清空全部记录
func (t *OutboundTracker) Clear() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = make(map[string]PendingOutboundRecord)
	t.receipts = make(map[string]StoredDeliveryReceipt)
	t.expire = nil
	t.received = nil
}

func (t *OutboundTracker) cleanupExpiredLocked(now time.Time) {
	nowValue := float64(now.UnixNano()) / float64(time.Second)
	for t.expire.Len() > 0 {
		item := t.expire[0]
		if item.expiresAt > nowValue {
			break
		}
		heap.Pop(&t.expire)
		key := pendingTrackerKey(item.internalMessageID, item.driverID)
		current, ok := t.pending[key]
		if !ok || current.ExpiresAt != item.expiresAt {
			continue
		}
		delete(t.pending, key)
	}
	for t.received.Len() > 0 {
		item := t.received[0]
		if item.expiresAt > nowValue {
			break
		}
		heap.Pop(&t.received)
		current, ok := t.receipts[item.externalMessageID]
		if !ok || current.ExpiresAt != item.expiresAt {
			continue
		}
		delete(t.receipts, item.externalMessageID)
	}
}
