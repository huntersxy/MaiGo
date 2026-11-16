package host

// 插件自报的通用运行标记，随运行实例清理并支持到期通知，对齐 host/status_badges.py

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

const maxRuntimeStatusBadges = 8

// runtimeStatusBadge 单个运行标记
type runtimeStatusBadge struct {
	ID        string
	Label     string
	ExpiresAt *float64
}

// RuntimeStatusBadges 每插件最多八个短文本标记；所有插件共用一个到期计时器
type RuntimeStatusBadges struct {
	mu        sync.Mutex
	badges    map[string][]runtimeStatusBadge
	timer     *time.Timer
	onChanged func()
}

// NewRuntimeStatusBadges 创建运行标记表
func NewRuntimeStatusBadges(onChanged func()) *RuntimeStatusBadges {
	return &RuntimeStatusBadges{badges: map[string][]runtimeStatusBadge{}, onChanged: onChanged}
}

// Update 替换调用者的标记列表；空列表清除
func (b *RuntimeStatusBadges) Update(pluginID string, payload any) error {
	items, ok := payload.([]any)
	if !ok || len(items) > maxRuntimeStatusBadges {
		return fmt.Errorf("运行标记必须是列表，最多八个")
	}
	now := float64(time.Now().UnixNano()) / 1e9
	normalized := make([]runtimeStatusBadge, 0, len(items))
	identifiers := map[string]struct{}{}
	for _, raw := range items {
		badge, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("运行标记必须是对象")
		}
		identifier, _ := badge["id"].(string)
		label, _ := badge["label"].(string)
		if strings.TrimSpace(identifier) == "" || len([]rune(identifier)) > 64 ||
			strings.TrimSpace(label) == "" || len([]rune(label)) > 64 {
			return fmt.Errorf("运行标记的 id 和 label 必须是 1 至 64 字符的文本")
		}
		if _, duplicate := identifiers[identifier]; duplicate {
			return fmt.Errorf("运行标记 id 不得重复")
		}
		identifiers[identifier] = struct{}{}
		var expiresAt *float64
		if rawExpiry, exists := badge["expires_at"]; exists && rawExpiry != nil {
			expiry, ok := floatValue(rawExpiry)
			if !ok || math.IsNaN(expiry) || math.IsInf(expiry, 0) || expiry > now+86400 {
				return fmt.Errorf("运行标记 expires_at 必须是未来一天内的 Unix 秒时间戳")
			}
			if expiry <= now {
				continue
			}
			expiresAt = &expiry
		}
		normalized = append(normalized, runtimeStatusBadge{ID: identifier, Label: label, ExpiresAt: expiresAt})
	}
	b.mu.Lock()
	previous, existed := b.badges[pluginID]
	changed := !existed || !equalBadges(previous, normalized)
	if len(normalized) > 0 {
		b.badges[pluginID] = normalized
	} else {
		delete(b.badges, pluginID)
	}
	b.mu.Unlock()
	if changed {
		b.scheduleExpiry()
		b.notifyChanged()
	}
	return nil
}

// Snapshot 返回未过期标记的副本
func (b *RuntimeStatusBadges) Snapshot() map[string][]map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := float64(time.Now().UnixNano()) / 1e9
	out := make(map[string][]map[string]any, len(b.badges))
	for pluginID, badges := range b.badges {
		items := make([]map[string]any, 0, len(badges))
		for _, badge := range badges {
			if badge.ExpiresAt != nil && *badge.ExpiresAt <= now {
				continue
			}
			item := map[string]any{"id": badge.ID, "label": badge.Label}
			if badge.ExpiresAt != nil {
				item["expires_at"] = *badge.ExpiresAt
			}
			items = append(items, item)
		}
		if len(items) > 0 {
			out[pluginID] = items
		}
	}
	return out
}

// Remove 移除插件的全部标记
func (b *RuntimeStatusBadges) Remove(pluginID string) {
	b.mu.Lock()
	_, existed := b.badges[pluginID]
	delete(b.badges, pluginID)
	b.mu.Unlock()
	if existed {
		b.scheduleExpiry()
		b.notifyChanged()
	}
}

// Clear 清空所有标记并停止计时器
func (b *RuntimeStatusBadges) Clear() {
	b.mu.Lock()
	changed := len(b.badges) > 0
	b.badges = map[string][]runtimeStatusBadge{}
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.mu.Unlock()
	if changed {
		b.notifyChanged()
	}
}

func (b *RuntimeStatusBadges) scheduleExpiry() {
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	earliest := math.Inf(1)
	for _, badges := range b.badges {
		for _, badge := range badges {
			if badge.ExpiresAt != nil && *badge.ExpiresAt < earliest {
				earliest = *badge.ExpiresAt
			}
		}
	}
	if !math.IsInf(earliest, 1) {
		delay := time.Duration(math.Max(0, earliest-float64(time.Now().UnixNano())/1e9) * float64(time.Second))
		b.timer = time.AfterFunc(delay, b.expire)
	}
	b.mu.Unlock()
}

func (b *RuntimeStatusBadges) expire() {
	b.mu.Lock()
	now := float64(time.Now().UnixNano()) / 1e9
	before := 0
	for _, badges := range b.badges {
		before += len(badges)
	}
	for pluginID, badges := range b.badges {
		kept := make([]runtimeStatusBadge, 0, len(badges))
		for _, badge := range badges {
			if badge.ExpiresAt == nil || *badge.ExpiresAt > now {
				kept = append(kept, badge)
			}
		}
		if len(kept) > 0 {
			b.badges[pluginID] = kept
		} else {
			delete(b.badges, pluginID)
		}
	}
	b.mu.Unlock()
	b.scheduleExpiry()
	after := 0
	b.mu.Lock()
	for _, badges := range b.badges {
		after += len(badges)
	}
	b.mu.Unlock()
	if after != before {
		b.notifyChanged()
	}
}

func (b *RuntimeStatusBadges) notifyChanged() {
	if b.onChanged != nil {
		b.onChanged()
	}
}

// UpdateRuntimeStatusBadges 只有已加载的插件可以上报运行标记
func (h *Host) UpdateRuntimeStatusBadges(pluginID string, badges any) error {
	if h == nil || h.statusBadges == nil {
		return fmt.Errorf("Host 未初始化")
	}
	if h.PluginLoadStatuses()[strings.TrimSpace(pluginID)] != "success" {
		return fmt.Errorf("只有已加载的插件可以上报运行标记")
	}
	return h.statusBadges.Update(strings.TrimSpace(pluginID), badges)
}

// RuntimeStatusBadgesSnapshot 返回未过期标记的副本
func (h *Host) RuntimeStatusBadgesSnapshot() map[string][]map[string]any {
	if h == nil || h.statusBadges == nil {
		return nil
	}
	return h.statusBadges.Snapshot()
}

func equalBadges(left, right []runtimeStatusBadge) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID || left[index].Label != right[index].Label {
			return false
		}
		if (left[index].ExpiresAt == nil) != (right[index].ExpiresAt == nil) {
			return false
		}
		if left[index].ExpiresAt != nil && *left[index].ExpiresAt != *right[index].ExpiresAt {
			return false
		}
	}
	return true
}

func floatValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}
