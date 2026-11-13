// Package debounce 对齐 MaiBot src/chat/debounce 固定消息防抖
package debounce

import (
	"fmt"
	"strings"
	"time"
)

// Mode 对应 chat.reply_timing 的防抖档位
type Mode string

const (
	ModeOff     Mode = "off"
	ModeFast    Mode = "fast"
	ModeNatural Mode = "natural"
	ModeSteady  Mode = "steady"

	quietThreshold   = 30 * time.Second
	bareImageMinWait = 4 * time.Second
	batchLimit       = 8 * time.Second
	mentionLimit     = 1 * time.Second
)

type waitTimes struct {
	quiet           time.Duration
	active          time.Duration
	sameAuthor      time.Duration
	differentAuthor time.Duration
}

var waitProfiles = map[Mode]waitTimes{
	ModeFast:    {time.Second, 3 * time.Second, time.Second, 500 * time.Millisecond},
	ModeNatural: {2 * time.Second, 5 * time.Second, 2 * time.Second, 500 * time.Millisecond},
	ModeSteady:  {4 * time.Second, 7 * time.Second, 4 * time.Second, 2 * time.Second},
}

// NormalizeMode 归一化 TOML 档位名
func NormalizeMode(raw string) Mode {
	switch Mode(raw) {
	case ModeOff, ModeFast, ModeNatural, ModeSteady:
		return Mode(raw)
	default:
		return ModeNatural
	}
}

// Debouncer 单会话防抖状态
type Debouncer struct {
	first       time.Time
	lastArrival time.Time
	hasLast     bool
	lastAuthor  string
	deadline    time.Time
	hasDeadline bool
	urgent      time.Time
	hasUrgent   bool
	modeEnabled bool
}

// NewDebouncer 创建防抖器
func NewDebouncer() *Debouncer {
	return &Debouncer{}
}

// Remaining 距离截止还需等待的时长
func (d *Debouncer) Remaining(now time.Time) time.Duration {
	if d == nil || !d.hasDeadline {
		return 0
	}
	remain := d.deadline.Sub(now)
	if remain <= 0 {
		return 0
	}
	return remain
}

// SyncMode 档位或开关变化时结束当前批次（对齐 Python 热重载）
func (d *Debouncer) SyncMode(mode Mode) {
	if d == nil {
		return
	}
	enabled := mode != ModeOff
	if enabled != d.modeEnabled {
		d.Consume()
	}
	d.modeEnabled = enabled
}

// Consume 本批消息已收走，保留上一条时间与发言人
func (d *Debouncer) Consume() {
	if d == nil {
		return
	}
	d.first = time.Time{}
	d.deadline = time.Time{}
	d.hasDeadline = false
	d.urgent = time.Time{}
	d.hasUrgent = false
}

// ObserveInput 单次入站消息观测参数
type ObserveInput struct {
	Author      string
	IsBareImage bool
	IsPureEmoji bool
	AtBot       bool
	ReceivedAt  time.Time
	WallTime    time.Time
	Now         time.Time
	Mode        Mode
}

// Observe 更新截止时间并返回规则说明（对齐 Python observe -> str）
func (d *Debouncer) Observe(in ObserveInput) string {
	if d == nil || in.Mode == ModeOff {
		return ""
	}
	d.SyncMode(in.Mode)
	waits, ok := waitProfiles[in.Mode]
	if !ok {
		waits = waitProfiles[ModeNatural]
	}
	preprocess := in.WallTime.Sub(in.ReceivedAt)
	if preprocess < 0 {
		preprocess = 0
	}
	arrival := in.Now.Add(-preprocess)
	previousArrival := d.lastArrival
	previousAuthor := d.lastAuthor
	hadLast := d.hasLast
	if d.hasLast && arrival.Before(d.lastArrival) {
		arrival = d.lastArrival
	}
	d.lastArrival = arrival
	d.hasLast = true
	d.lastAuthor = in.Author

	newBatch := d.first.IsZero()
	if newBatch {
		d.first = arrival
	}
	if in.AtBot {
		urgent := in.Now.Add(-preprocess).Add(mentionLimit)
		if !d.hasUrgent || urgent.Before(d.urgent) {
			d.urgent = urgent
			d.hasUrgent = true
		}
	}

	var reason string
	if !d.hasDeadline || d.deadline.After(in.Now) {
		reasons := make([]string, 0, 4)
		limit := d.first.Add(batchLimit)
		if d.hasUrgent && d.urgent.Before(limit) {
			limit = d.urgent
		}
		var wait time.Duration
		if newBatch {
			active := hadLast && arrival.Sub(previousArrival) <= quietThreshold
			if active {
				wait = waits.active
				reasons = append(reasons, fmt.Sprintf("活跃开批 %s 秒", formatSecondsG(wait)))
			} else {
				wait = waits.quiet
				reasons = append(reasons, fmt.Sprintf("安静开批 %s 秒", formatSecondsG(wait)))
			}
			if in.IsBareImage {
				reasons = append(reasons, "裸图开批至少 4 秒")
				if wait < bareImageMinWait {
					wait = bareImageMinWait
				}
			}
		} else if in.Author == previousAuthor {
			wait = waits.sameAuthor
			reasons = append(reasons, fmt.Sprintf("同人续发 %s 秒", formatSecondsG(wait)))
		} else {
			wait = waits.differentAuthor
			reasons = append(reasons, fmt.Sprintf("换人 %s 秒", formatSecondsG(wait)))
		}
		candidate := arrival.Add(wait)
		if in.IsPureEmoji && !newBatch {
			reasons = append(reasons, "纯表情包不延长等待")
			if d.hasDeadline && candidate.After(d.deadline) {
				candidate = d.deadline
			}
		}
		if candidate.After(d.first.Add(batchLimit)) {
			reasons = append(reasons, "受整批 8 秒上限限制")
		}
		if d.hasUrgent && candidate.After(d.urgent) {
			reasons = append(reasons, "受@或提及 1 秒上限限制")
		}
		if candidate.After(limit) {
			candidate = limit
		}
		d.deadline = candidate
		d.hasDeadline = true
		reason = strings.Join(reasons, "；")
	} else {
		reason = "批次已到期，不再延长等待"
	}
	return reason
}

// Deadline 当前批次截止时间；ok=false 表示无截止
func (d *Debouncer) Deadline() (time.Time, bool) {
	if d == nil || !d.hasDeadline {
		return time.Time{}, false
	}
	return d.deadline, true
}

func formatSecondsG(wait time.Duration) string {
	return fmt.Sprintf("%g", float64(wait)/float64(time.Second))
}
