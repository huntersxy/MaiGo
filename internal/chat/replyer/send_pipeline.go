package replyer

import (
	"context"
	"strconv"
	"time"

	"go.uber.org/zap"
)

// SendKind 发送循环的会话类型
type SendKind string

const (
	// SendKindGroup 群聊
	SendKindGroup SendKind = "group"
	// SendKindPrivate 私聊
	SendKindPrivate SendKind = "private"
)

// OutboundMessage 待出站消息；由调用方注入的 SendFunc 负责真实发送
//
// 字段与 runtime 的 outboundMessage 对齐，Storage/ShowLog 由调用方在 prepare 时填充。
type OutboundMessage struct {
	Kind     SendKind
	TargetID int64
	Text     string
	Mentions []int64
	ReplyTo  int64
	// ReplyAnchor 被动回复锚点，独立于显示引用，供平台适配器占用被动额度
	ReplyAnchor int64
	Typing            bool
	Storage           bool
	ShowLog           bool
	MessageID         int64
	ExternalMessageID string
}

// SendFunc 执行一次真实发送；返回平台消息 ID、到达序号、是否已发送
type SendFunc func(ctx context.Context, msg *OutboundMessage) (messageID int64, arrivalSeq uint64, sent bool, err error)

// SendHooks 发送循环依赖的宿主能力，全部由调用方注入
type SendHooks struct {
	// Send 真实发送入口
	Send SendFunc
	// BroadcastTyping 广播内部打字状态
	BroadcastTyping func(ctx context.Context, kind SendKind, targetID int64, typing, start bool)
	// TypingSimulation 是否启用旧式打字模拟
	TypingSimulation bool
	// TypingSpeed 打字速度，<=0 按 6 处理
	TypingSpeed int
	// QuotePreviousAllowed 非首段是否允许引用上一段
	QuotePreviousAllowed bool
	// AnchorReplyTo 被动回复锚点；每段消息都携带，不随显示引用变化
	AnchorReplyTo int64
	// AbortOnFailure 段发送失败时立即停止；false 时跳过该段继续后续段
	AbortOnFailure bool
	// AfterSegment 每段发送成功后回调，收到已发送的消息（含 Storage/ShowLog/最终文本）
	AfterSegment func(ctx context.Context, msg *OutboundMessage, arrivalSeq uint64, at time.Time)
	// AfterAll 全部发送结束后回调，收到最后一条消息 ID（0 表示没有成功段）
	AfterAll func(ctx context.Context, kind SendKind, targetID int64, lastMessageID int64, segmentCount int, showLog bool)
}

// SendSegments 逐段发送并返回真实结果
//
// 对齐 Python maisaka/builtin_tool/reply.py 的发送循环：
// 首段使用初始引用，后续段按 QuotePrevious 引用上一段；abortOnFailure 时失败即停止。
func SendSegments(ctx context.Context, kind SendKind, targetID int64, segments []Segment, initialReplyTo int64, mentions []int64, hooks SendHooks) ([]SendSegmentResult, error) {
	results := make([]SendSegmentResult, 0, len(segments))
	if hooks.Send == nil {
		return results, nil
	}
	typingSpeed := hooks.TypingSpeed
	if typingSpeed <= 0 {
		typingSpeed = 6
	}

	var lastMessageID int64
	lastShowLog := true
	for index, seg := range segments {
		text := seg.Text
		if text == "" {
			continue
		}
		delaySec := seg.TypingDelaySec
		if !hooks.TypingSimulation && delaySec <= 0 {
			delaySec = 0
		}
		typing := delaySec > 0 || (hooks.TypingSimulation && index == 0)
		if hooks.BroadcastTyping != nil {
			hooks.BroadcastTyping(ctx, kind, targetID, typing, true)
		}
		err := WaitSpeakDelay(ctx, delaySec, hooks.TypingSimulation && index == 0 && delaySec <= 0, len([]rune(text)), typingSpeed)
		if hooks.BroadcastTyping != nil {
			hooks.BroadcastTyping(ctx, kind, targetID, typing, false)
		}
		if err != nil {
			return results, err
		}

		replyTo := int64(0)
		switch {
		case index == 0:
			replyTo = initialReplyTo
		case hooks.QuotePreviousAllowed && seg.QuotePrevious && lastMessageID != 0:
			replyTo = lastMessageID
		}
		segmentMentions := mentions
		if index > 0 {
			segmentMentions = nil
		}

		out := &OutboundMessage{
			Kind:        kind,
			TargetID:    targetID,
			Text:        text,
			Mentions:    segmentMentions,
			ReplyTo:     replyTo,
			ReplyAnchor: hooks.AnchorReplyTo,
			Typing:      typing,
		}
		messageID, arrivalSeq, sent, err := hooks.Send(ctx, out)
		if err != nil {
			zap.L().Error("发言失败", zap.String("kind", string(kind)), zap.Int64("target_id", targetID), zap.Error(err))
			return results, err
		}
		if !sent {
			results = append(results, SendSegmentResult{Index: index, Segment: text, SetQuote: replyTo != 0, Success: false})
			if hooks.AbortOnFailure {
				break
			}
			continue
		}
		// 发送入口可能改写文本、@ 与引用（Hook 可调整），以改写后的值为准；
		// 直接赋值，不做非空兜底——Hook 清空引用就是明确表示「这段不引用」
		lastMessageID = messageID
		lastShowLog = out.ShowLog

		results = append(results, SendSegmentResult{
			Index:     index,
			Segment:   out.Text,
			SetQuote:  out.ReplyTo != 0,
			Success:   true,
			MessageID: strconv.FormatInt(messageID, 10),
		})
		if hooks.AfterSegment != nil {
			hooks.AfterSegment(ctx, out, arrivalSeq, time.Now())
		}
	}

	if hooks.AfterAll != nil {
		hooks.AfterAll(ctx, kind, targetID, lastMessageID, len(segments), lastShowLog)
	}
	return results, nil
}

// WaitSpeakDelay 按段延迟等待；秒数优先，其次按旧式打字速度估算，上限 5 秒
func WaitSpeakDelay(ctx context.Context, seconds float64, legacyTyping bool, contentLen int, typingSpeed int) error {
	var delay time.Duration
	switch {
	case seconds > 0:
		delay = time.Duration(seconds * float64(time.Second))
	case legacyTyping && contentLen > 0:
		speed := typingSpeed
		if speed <= 0 {
			speed = 6
		}
		delay = time.Duration(float64(contentLen)/float64(speed)*1000) * time.Millisecond
	default:
		return nil
	}
	if delay > 5*time.Second {
		delay = 5 * time.Second
	}
	if delay > 0 && delay < 500*time.Millisecond {
		delay = 500 * time.Millisecond
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
