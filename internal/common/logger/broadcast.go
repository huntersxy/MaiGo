package logger

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap/zapcore"
)

// WebUIEntry 对齐 Dashboard LogEntry（legacy /ws/logs 与 unified logs 域）
type WebUIEntry struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Module    string `json:"module"`
	Message   string `json:"message"`
}

var logBroadcast = newBroadcastHub()

type broadcastHub struct {
	mu      sync.RWMutex
	subs    map[chan WebUIEntry]struct{}
	stopped bool
}

func newBroadcastHub() *broadcastHub {
	return &broadcastHub{subs: make(map[chan WebUIEntry]struct{})}
}

func (h *broadcastHub) subscribe() (chan WebUIEntry, func()) {
	ch := make(chan WebUIEntry, 64)
	h.mu.Lock()
	if h.stopped {
		// 关停后新订阅者拿到已关闭通道，WebSocket 处理器会按既有 ok=false 分支退出
		close(ch)
		h.mu.Unlock()
		return ch, func() {}
	}
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *broadcastHub) publish(entry WebUIEntry) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.stopped {
		return
	}
	for ch := range h.subs {
		select {
		case ch <- entry:
		default:
		}
	}
}

// stop 关闭全部订阅通道并拒绝后续广播；并发调用与重复调用均安全
func (h *broadcastHub) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return
	}
	h.stopped = true
	for ch := range h.subs {
		close(ch)
		delete(h.subs, ch)
	}
}

// SubscribeWebUI 订阅实时日志推送，返回只读 channel 与取消函数
func SubscribeWebUI() (<-chan WebUIEntry, func()) {
	return logBroadcast.subscribe()
}

// StopWebUIBroadcasts 停止 WebSocket 日志广播并关闭所有订阅通道；幂等
func StopWebUIBroadcasts() {
	logBroadcast.stop()
}

// RecentWebUIEntries 从内存 ring buffer 读取最近日志，按时间旧→新
func RecentWebUIEntries(limit int) []WebUIEntry {
	if limit <= 0 {
		return nil
	}
	if limit > 500 {
		limit = 500
	}
	raw, firstSeq := recentLogs.Snapshot()
	if len(raw) == 0 {
		return nil
	}
	start := 0
	if len(raw) > limit {
		start = len(raw) - limit
		firstSeq += uint64(start)
	}
	out := make([]WebUIEntry, 0, len(raw)-start)
	for i := start; i < len(raw); i++ {
		if entry, ok := parseWebUIEntry(raw[i], firstSeq+uint64(i-start)); ok {
			out = append(out, entry)
		}
	}
	return out
}

func publishRawLogLine(line string, seq uint64) {
	entry, ok := parseWebUIEntry(line, seq)
	if !ok {
		return
	}
	logBroadcast.publish(entry)
}

func parseWebUIEntry(line string, seq uint64) (WebUIEntry, bool) {
	var fields map[string]any
	if err := logJSONAPI.UnmarshalFromString(line, &fields); err != nil {
		return WebUIEntry{}, false
	}
	levelText, _ := fields["level"].(string)
	module := strings.TrimSpace(anyString(fields["logger"]))
	if module == "" {
		module = "maibot-go"
	}
	message := anyString(fields["msg"])
	timeText := formatWebUITimestamp(anyString(fields["time"]))
	id := fmt.Sprintf("%d_%d", webUITimestampMillis(anyString(fields["time"])), seq)
	return WebUIEntry{
		ID:        id,
		Timestamp: timeText,
		Level:     normalizeWebUILevel(levelText),
		Module:    module,
		Message:   message,
	}, true
}

// anyString 把日志字段转成文本；nil 视为空串，不能落成 "<nil>"
func anyString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprint(v)
	}
}

func normalizeWebUILevel(level string) string {
	var parsed zapcore.Level
	if parsed.UnmarshalText([]byte(strings.ToLower(strings.TrimSpace(level)))) != nil {
		return "INFO"
	}
	switch parsed {
	case zapcore.DebugLevel:
		return "DEBUG"
	case zapcore.InfoLevel:
		return "INFO"
	case zapcore.WarnLevel:
		return "WARNING"
	case zapcore.ErrorLevel:
		return "ERROR"
	case zapcore.DPanicLevel, zapcore.PanicLevel, zapcore.FatalLevel:
		return "CRITICAL"
	default:
		return "INFO"
	}
}

func webUITimestampMillis(raw string) int64 {
	raw = strings.TrimSpace(raw)
	layouts := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999Z07:00", "2006-01-02 15:04:05"}
	for _, layout := range layouts {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts.UnixMilli()
		}
	}
	return time.Now().UnixMilli()
}

func formatWebUITimestamp(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now().Format("2006-01-02 15:04:05")
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999Z07:00",
		"2006-01-02 15:04:05",
		"15:04:05",
	}
	for _, layout := range layouts {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts.Local().Format("2006-01-02 15:04:05")
		}
	}
	return raw
}
