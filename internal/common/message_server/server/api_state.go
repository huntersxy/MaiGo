package server

import (
	"sync"
	"time"
)

// apiStats 对齐 maim_message WebSocketServer 统计字段
type apiStats struct {
	mu                      sync.Mutex
	totalAuthRequests       int64
	successfulAuths         int64
	failedAuths             int64
	messagesProcessed       int64
	customMessagesProcessed int64
	activeHandlerTasks      int64
	duplicateIgnored        int64
}

func (s *apiStats) incAuthRequest() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.totalAuthRequests++
	s.mu.Unlock()
}

func (s *apiStats) incAuthResult(ok bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if ok {
		s.successfulAuths++
	} else {
		s.failedAuths++
	}
	s.mu.Unlock()
}

func (s *apiStats) incMessage() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.messagesProcessed++
	s.mu.Unlock()
}

func (s *apiStats) incCustomMessage() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.customMessagesProcessed++
	s.mu.Unlock()
}

func (s *apiStats) incDuplicate() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.duplicateIgnored++
	s.mu.Unlock()
}

func (s *apiStats) beginHandler() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.activeHandlerTasks++
	s.mu.Unlock()
}

func (s *apiStats) endHandler() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.activeHandlerTasks > 0 {
		s.activeHandlerTasks--
	}
	s.mu.Unlock()
}

func (s *apiStats) snapshot(currentUsers, currentConnections int) map[string]any {
	if s == nil {
		return map[string]any{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{
		"total_auth_requests":        s.totalAuthRequests,
		"successful_auths":           s.successfulAuths,
		"failed_auths":               s.failedAuths,
		"messages_processed":         s.messagesProcessed,
		"custom_messages_processed":  s.customMessagesProcessed,
		"current_users":              currentUsers,
		"current_connections":        currentConnections,
		"active_handler_tasks":       s.activeHandlerTasks,
		"duplicate_messages_ignored": s.duplicateIgnored,
	}
}

// apiCachedMessage 待客户端 ACK 的出站 API 消息
type apiCachedMessage struct {
	MsgID      string
	TargetUUID string
	Payload    []byte
	CreatedAt  time.Time
	RetryCount int
	Status     string
}

// apiMessageCache 对齐 maim_message MessageCache：
// TTL 清理、最大条数淘汰、按目标连接重放、ACK 后移除
type apiMessageCache struct {
	mu           sync.Mutex
	ttl          time.Duration
	maxSize      int
	cleanup      time.Duration
	entries      map[string]*apiCachedMessage
	order        []string
	ackedTotal   int64
	evictedTotal int64
	lastCleanup  time.Time
}

func newAPIMessageCache(ttl time.Duration, maxSize int, cleanup time.Duration) *apiMessageCache {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if maxSize <= 0 {
		maxSize = 1000
	}
	if cleanup <= 0 {
		cleanup = time.Minute
	}
	return &apiMessageCache{
		ttl:         ttl,
		maxSize:     maxSize,
		cleanup:     cleanup,
		entries:     make(map[string]*apiCachedMessage),
		lastCleanup: time.Now(),
	}
}

func (c *apiMessageCache) Add(msgID, targetUUID string, payload []byte) {
	if c == nil || msgID == "" || targetUUID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.cleanupLocked(now)
	if len(c.entries) >= c.maxSize {
		c.evictOldestLocked()
	}
	if _, exists := c.entries[msgID]; exists {
		return
	}
	c.entries[msgID] = &apiCachedMessage{
		MsgID:      msgID,
		TargetUUID: targetUUID,
		Payload:    append([]byte(nil), payload...),
		CreatedAt:  now,
		RetryCount: 0,
		Status:     "pending",
	}
	c.order = append(c.order, msgID)
}

func (c *apiMessageCache) Ack(msgID string) bool {
	if c == nil || msgID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[msgID]; !ok {
		return false
	}
	delete(c.entries, msgID)
	c.ackedTotal++
	c.compactOrderLocked()
	return true
}

func (c *apiMessageCache) PendingByTarget(targetUUID string) []apiCachedMessage {
	if c == nil || targetUUID == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(time.Now())
	out := make([]apiCachedMessage, 0)
	for _, msgID := range c.order {
		entry, ok := c.entries[msgID]
		if !ok || entry.TargetUUID != targetUUID {
			continue
		}
		entry.RetryCount++
		entry.Status = "retrying"
		out = append(out, *entry)
	}
	return out
}

func (c *apiMessageCache) Stats() map[string]any {
	if c == nil {
		return map[string]any{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(time.Now())
	return map[string]any{
		"size":          len(c.entries),
		"max_size":      c.maxSize,
		"ttl_seconds":   c.ttl.Seconds(),
		"acked_total":   c.ackedTotal,
		"evicted_total": c.evictedTotal,
	}
}

func (c *apiMessageCache) cleanupLocked(now time.Time) {
	if now.Sub(c.lastCleanup) < c.cleanup {
		return
	}
	c.lastCleanup = now
	changed := false
	for msgID, entry := range c.entries {
		if now.Sub(entry.CreatedAt) > c.ttl {
			delete(c.entries, msgID)
			changed = true
		}
	}
	if changed {
		c.compactOrderLocked()
	}
}

func (c *apiMessageCache) evictOldestLocked() {
	for len(c.order) > 0 {
		msgID := c.order[0]
		c.order = c.order[1:]
		if _, ok := c.entries[msgID]; ok {
			delete(c.entries, msgID)
			c.evictedTotal++
			return
		}
	}
}

func (c *apiMessageCache) compactOrderLocked() {
	kept := c.order[:0]
	for _, msgID := range c.order {
		if _, ok := c.entries[msgID]; ok {
			kept = append(kept, msgID)
		}
	}
	c.order = kept
}
