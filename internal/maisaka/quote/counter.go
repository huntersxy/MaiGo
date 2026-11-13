package quote

import (
	"fmt"
	"time"
)

const maxQuoteWindow = 100

// Context 引用决策快照，对齐 ReplyQuoteContext
type Context struct {
	TargetDistance   *int
	PreviousSenderID string
	PreviousSeconds  float64
	PreviousBurst    int
}

// Counter 按消息顺序维护引用距离，不扫描上下文
type Counter struct {
	sessionID string
	token     uintptr

	positions    map[string]int
	order        []string
	sequence     int
	senderID     string
	previousTime *time.Time
	burst        int

	messageTokens map[string]uintptr
}

// NewCounter 创建会话级引用计数器
func NewCounter(sessionID string) *Counter {
	return &Counter{
		sessionID:     sessionID,
		token:         newToken(),
		positions:     make(map[string]int),
		messageTokens: make(map[string]uintptr),
	}
}

func newToken() uintptr {
	return uintptr(time.Now().UnixNano())
}

// RecordMessage 记录一条普通消息；notify 消息跳过
func (c *Counter) RecordMessage(messageID, senderID string, at time.Time, isNotify bool) error {
	if messageID == "" {
		return fmt.Errorf("引用计数消息缺少 message_id")
	}
	if isNotify {
		return nil
	}
	if tok, ok := c.messageTokens[messageID]; ok && tok == c.token {
		return nil
	}
	if _, ok := c.positions[messageID]; ok {
		c.messageTokens[messageID] = c.token
		return nil
	}

	c.sequence++
	c.messageTokens[messageID] = c.token
	c.positions[messageID] = c.sequence
	c.order = append(c.order, messageID)
	if len(c.order) > maxQuoteWindow {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.positions, oldest)
	}
	if senderID == c.senderID {
		c.burst++
		if c.burst > 16 {
			c.burst = 16
		}
	} else {
		c.burst = 1
	}
	c.senderID = senderID
	t := at
	c.previousTime = &t
	return nil
}

// Snapshot 为目标消息生成引用上下文；未知序号时返回 nil
func (c *Counter) Snapshot(targetMessageID string, targetSessionID string, isNotify bool, now time.Time) (*Context, error) {
	if targetSessionID != c.sessionID {
		return nil, fmt.Errorf("引用目标必须是当前会话的普通消息")
	}
	if isNotify {
		return nil, fmt.Errorf("引用目标必须是当前会话的普通消息")
	}
	seq, positioned := c.positions[targetMessageID]
	token, known := c.messageTokens[targetMessageID]
	if !positioned && (!known || token != c.token) {
		return nil, nil
	}
	if c.previousTime == nil {
		return nil, fmt.Errorf("引用计数器尚未记录普通消息")
	}
	var distPtr *int
	if positioned {
		distance := c.sequence - seq
		if distance < maxQuoteWindow {
			d := distance
			distPtr = &d
		}
	}
	seconds := now.Sub(*c.previousTime).Seconds()
	if seconds < 0 {
		seconds = 0
	}
	return &Context{
		TargetDistance:   distPtr,
		PreviousSenderID: c.senderID,
		PreviousSeconds:  seconds,
		PreviousBurst:    c.burst,
	}, nil
}
