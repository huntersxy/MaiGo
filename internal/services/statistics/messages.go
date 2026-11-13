package stats

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	dirInbound  = "inbound"
	dirOutbound = "outbound"
)

type msgKey struct {
	Bucket          time.Time
	Direction       string
	ConversationKey string
}

// MessageCounters 进程内消息计数，可选按小时刷入 SQLite message_traffic_hourly / message_traffic_chat_hourly
type MessageCounters struct {
	mu       sync.Mutex
	pending  map[msgKey]int64
	db       *gorm.DB
	inbound  atomic.Int64
	outbound atomic.Int64
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

var defaultMessages atomic.Pointer[MessageCounters]

// NewMessageCounters 创建计数器
func NewMessageCounters() *MessageCounters {
	return &MessageCounters{
		pending: make(map[msgKey]int64),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// SetDefaultMessages 设置全局计数器，供 agent 钩子使用
func SetDefaultMessages(c *MessageCounters) { defaultMessages.Store(c) }

// DefaultMessageCounters 返回全局计数器（可能为 nil）
func DefaultMessageCounters() *MessageCounters { return defaultMessages.Load() }

// RecordInbound 入站消息 +1（用户侧，不含机器人自发 echo）
func RecordInbound(conversationKey string) { recordDirection(dirInbound, conversationKey) }

// RecordOutbound 出站消息 +1（机器人成功发送的段）
func RecordOutbound(conversationKey string) { recordDirection(dirOutbound, conversationKey) }

func recordDirection(direction, conversationKey string) {
	c := defaultMessages.Load()
	if c == nil {
		return
	}
	c.record(direction, normalizeConversationKey(conversationKey), 1)
}

func normalizeConversationKey(raw string) string {
	return strings.TrimSpace(raw)
}

func (c *MessageCounters) record(direction, conversationKey string, delta int64) {
	if c == nil || delta <= 0 || direction == "" {
		return
	}
	switch direction {
	case dirInbound:
		c.inbound.Add(delta)
	case dirOutbound:
		c.outbound.Add(delta)
	default:
		return
	}
	bucket := time.Now().UTC().Truncate(time.Hour)
	c.mu.Lock()
	c.pending[msgKey{Bucket: bucket, Direction: direction}] += delta
	if conversationKey != "" {
		c.pending[msgKey{Bucket: bucket, Direction: direction, ConversationKey: conversationKey}] += delta
	}
	c.mu.Unlock()
}

// Totals 返回自进程启动以来的累计计数
func (c *MessageCounters) Totals() (inbound, outbound int64) {
	if c == nil {
		return 0, 0
	}
	return c.inbound.Load(), c.outbound.Load()
}

// PendingChatTotals 返回尚未 flush 的按会话累计（入站+出站），供 top chats 与 PG 合并
func (c *MessageCounters) PendingChatTotals() map[string]int64 {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64)
	for k, count := range c.pending {
		if k.ConversationKey == "" || count <= 0 {
			continue
		}
		out[k.ConversationKey] += count
	}
	return out
}

// Start 绑定数据库并启动定时 flush；db 为 nil 时仅内存计数
func (c *MessageCounters) Start(db *gorm.DB) {
	if c == nil {
		return
	}
	c.db = db
	if db == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		defer close(c.done)
		for {
			select {
			case <-ticker.C:
				c.flush()
			case <-c.stop:
				c.flush()
				return
			}
		}
	}()
}

// Close 停止后台 flush
func (c *MessageCounters) Close() {
	if c == nil {
		return
	}
	c.once.Do(func() { close(c.stop) })
	if c.db != nil {
		<-c.done
	}
}

func (c *MessageCounters) flush() {
	if c == nil || c.db == nil {
		return
	}
	c.mu.Lock()
	batch := c.pending
	c.pending = make(map[msgKey]int64)
	c.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	err := c.db.Transaction(func(tx *gorm.DB) error {
		for k, count := range batch {
			if k.ConversationKey == "" {
				if err := tx.Exec(`INSERT INTO message_traffic_hourly(bucket_start,direction,message_count)
					VALUES(?,?,?) ON CONFLICT(bucket_start,direction) DO UPDATE SET
					message_count=message_traffic_hourly.message_count+EXCLUDED.message_count`,
					k.Bucket, k.Direction, count).Error; err != nil {
					return err
				}
				continue
			}
			if err := tx.Exec(`INSERT INTO message_traffic_chat_hourly(bucket_start,direction,conversation_key,message_count)
				VALUES(?,?,?,?) ON CONFLICT(bucket_start,direction,conversation_key) DO UPDATE SET
				message_count=message_traffic_chat_hourly.message_count+EXCLUDED.message_count`,
				k.Bucket, k.Direction, k.ConversationKey, count).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		return
	}
	zap.L().Warn("写入消息流量统计失败，将保留到下次重试", zap.Error(err))
	c.mu.Lock()
	for k, count := range batch {
		c.pending[k] += count
	}
	c.mu.Unlock()
}
