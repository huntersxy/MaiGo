package monitor

import (
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	Domain = "maisaka_monitor"
	Topic  = "main"
)

// Event 统一 WebSocket 麦麦监控域出站事件
type Event struct {
	Event string
	Data  map[string]any
}

var (
	hub       = newBroadcastHub()
	publishMu sync.Mutex
)

type broadcastHub struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

func newBroadcastHub() *broadcastHub {
	return &broadcastHub{subs: make(map[chan Event]struct{})}
}

func (h *broadcastHub) subscribe() (chan Event, func()) {
	ch := make(chan Event, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *broadcastHub) publish(ev Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Subscribe 订阅麦麦监控实时事件
func Subscribe() (<-chan Event, func()) {
	return hub.subscribe()
}

// Publish 广播监控事件（供 WebUI WS 与其它订阅方消费）
func Publish(event string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	publishMu.Lock()
	defer publishMu.Unlock()
	if _, skipped := nonPersistedEvents[event]; !skipped {
		if store := CurrentEventStore(); store != nil {
			cleaned, err := store.Record(event, data)
			if err != nil {
				zap.L().Warn("写入麦麦观察事件失败", zap.String("event", event), zap.Error(err))
			} else {
				data = cleaned
			}
		}
	}
	hub.publish(Event{Event: event, Data: data})
}

// EmitThinkStart 对齐 Go Agent 思考轮开始（最小 P2-02 事件）
func EmitThinkStart(kind string, targetID int64, sessionID string) {
	now := float64(time.Now().UnixNano()) / 1e9
	Publish("think.start", map[string]any{
		"kind":       kind,
		"target_id":  targetID,
		"session_id": sessionID,
		"timestamp":  now,
	})
}

// EmitThinkEnd 对齐 Go Agent 思考轮结束
func EmitThinkEnd(kind string, targetID int64, sessionID string) {
	now := float64(time.Now().UnixNano()) / 1e9
	Publish("think.end", map[string]any{
		"kind":       kind,
		"target_id":  targetID,
		"session_id": sessionID,
		"timestamp":  now,
	})
}

// EmitHeartbeat 无 Agent 钩子时用于保活与连通性探测
func EmitHeartbeat(note string) {
	now := float64(time.Now().UnixNano()) / 1e9
	Publish("stage.status", map[string]any{
		"entries":   []any{},
		"timestamp": now,
		"note":      note,
		"heartbeat": true,
	})
}
