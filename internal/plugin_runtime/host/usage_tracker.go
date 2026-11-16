package host

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// StartUsageTracking 加载已有累计时长并启动周期 checkpoint
func (h *Host) StartUsageTracking(path string) error {
	if h == nil {
		return fmt.Errorf("Host 未初始化")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("插件使用时长记录路径不能为空")
	}
	if err := h.stopUsageTracking(); err != nil {
		return err
	}
	tracker := NewPluginUsageTracker(path)
	if err := tracker.Load(); err != nil {
		// 计时记录不可读时只停用计时，不阻止插件运行时启动。
		h.mu.Lock()
		h.usage = nil
		h.usageTracking = false
		h.usageError = err
		h.mu.Unlock()
		zap.L().Warn("读取插件累计使用时长失败，已停用计时", zap.String("path", path), zap.Error(err))
		return nil
	}
	return h.attachUsageTracking(tracker, DefaultPluginUsageCheckpointInterval)
}

// UsageAvailable 返回累计使用时长是否可用；记录损坏时前端省略 usage_seconds
func (h *Host) UsageAvailable() bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.usage != nil && h.usageError == nil
}

// UsageError 返回停用计时的原因
func (h *Host) UsageError() error {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.usageError
}

// StopUsageTracking 停止周期 checkpoint，结算当前运行区间并落盘
func (h *Host) StopUsageTracking() error {
	return h.stopUsageTracking()
}

// attachUsageTracking 绑定已加载的记录器并启动 checkpoint goroutine
func (h *Host) attachUsageTracking(tracker *PluginUsageTracker, interval time.Duration) error {
	if h == nil || tracker == nil {
		return fmt.Errorf("插件使用时长记录器未初始化")
	}
	if interval <= 0 {
		interval = DefaultPluginUsageCheckpointInterval
	}
	h.mu.Lock()
	h.usage = tracker
	h.usageTracking = true
	h.usageError = nil
	ctx, cancel := context.WithCancel(context.Background())
	h.usageCancel = cancel
	h.usageWG.Add(1)
	h.mu.Unlock()
	go h.checkpointUsage(ctx, tracker, interval)
	h.ReconcileUsage()
	return nil
}

// ensureUsageTracking 在首个 Runner 启动前加载记录器；已有记录器时不重复加载
func (h *Host) ensureUsageTracking() (bool, error) {
	if h == nil {
		return false, fmt.Errorf("Host 未初始化")
	}
	h.mu.RLock()
	tracking := h.usage != nil && h.usageTracking
	h.mu.RUnlock()
	if tracking {
		return false, nil
	}
	if err := h.StartUsageTracking(DefaultPluginUsagePath()); err != nil {
		return false, err
	}
	return true, nil
}

func (h *Host) checkpointUsage(ctx context.Context, tracker *PluginUsageTracker, interval time.Duration) {
	defer h.usageWG.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.ReconcileUsage()
			if err := tracker.Save(); err != nil {
				zap.L().Warn("保存插件累计使用时长失败", zap.Error(err), zap.String("path", tracker.Path()))
			}
		}
	}
}

func (h *Host) stopUsageTracking() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	tracker := h.usage
	h.usageTracking = false
	cancel := h.usageCancel
	h.usageCancel = nil
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	h.usageWG.Wait()
	if tracker == nil {
		return nil
	}
	tracker.Reconcile(nil)
	if err := tracker.Save(); err != nil {
		return fmt.Errorf("保存插件使用时长失败: %w", err)
	}
	return nil
}
