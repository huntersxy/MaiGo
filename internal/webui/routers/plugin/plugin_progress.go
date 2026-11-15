package plugin

import (
	"sync"

	"MaiGo/internal/plugin_market"
)

const (
	// ProgressDomain WebSocket 订阅域
	ProgressDomain = "plugin_progress"
	// ProgressTopic WebSocket 订阅主题
	ProgressTopic = "main"
)

const (
	pluginProgressDomain = ProgressDomain
	pluginProgressTopic  = ProgressTopic
)

// pluginProgressHub 保存最近一次插件安装/更新/卸载进度并广播给统一 WebSocket
// 对齐 routers/plugin/progress.py 的 current_progress 与 broadcast_progress
type PluginProgressHub struct {
	mu          sync.RWMutex
	current     pluginmarket.ProgressUpdate
	subscribers map[uint64]chan pluginmarket.ProgressUpdate
	nextID      uint64
}

func newPluginProgressHub() *PluginProgressHub {
	return &PluginProgressHub{
		current: pluginmarket.ProgressUpdate{
			Operation: "idle",
			Stage:     "idle",
		},
		subscribers: map[uint64]chan pluginmarket.ProgressUpdate{},
	}
}

// defaultPluginProgressHub 由 Mount 接到 PluginMarket 服务，供统一 WS 读取快照
var defaultPluginProgressHub = newPluginProgressHub()

// DefaultProgressHub 返回进程级插件进度中心，供统一 WebSocket 读取快照与订阅
func DefaultProgressHub() *PluginProgressHub { return defaultPluginProgressHub }

func (h *PluginProgressHub) Snapshot() pluginmarket.ProgressUpdate {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.current
}

// Publish 更新快照并唤醒订阅者；订阅者消费慢时丢弃该次更新，不能让安装线程阻塞
func (h *PluginProgressHub) Publish(update pluginmarket.ProgressUpdate) {
	h.mu.Lock()
	h.current = update
	subscribers := make([]chan pluginmarket.ProgressUpdate, 0, len(h.subscribers))
	for _, ch := range h.subscribers {
		subscribers = append(subscribers, ch)
	}
	h.mu.Unlock()
	for _, ch := range subscribers {
		select {
		case ch <- update:
		default:
		}
	}
}

func (h *PluginProgressHub) Subscribe() (<-chan pluginmarket.ProgressUpdate, func()) {
	ch := make(chan pluginmarket.ProgressUpdate, 16)
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	h.subscribers[id] = ch
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subscribers, id)
		h.mu.Unlock()
	}
}

// setupPluginProgress 把市场安装进度接到 WebSocket 广播；未接市场服务时保持 idle
func setupPluginProgress(d Deps) {
	if d.PluginMarket == nil {
		return
	}
	d.PluginMarket.SetProgressReporter(defaultPluginProgressHub.Publish)
}
