package selfupdate

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// defaultRefreshTimeout 为后台刷新单次联网的兜底预算；Python 由检查内部 60 秒截止时间兜底
const defaultRefreshTimeout = 2 * time.Minute

// StartBackgroundRefresh 启动与 WebUI 生命周期一致的后台刷新
// 首次刷新在完整 TTL 之后，之后每次检查结束再排下一次；启动不联网、不阻塞调用方
func (m *Manager) StartBackgroundRefresh() {
	m.refreshMu.Lock()
	if m.refreshActive {
		m.refreshMu.Unlock()
		return
	}
	stop := make(chan struct{})
	m.refreshActive = true
	m.refreshStop = stop
	interval := m.refreshIntervalValue()
	m.refreshMu.Unlock()
	go m.backgroundRefreshLoop(stop, interval)
}

// StopBackgroundRefresh 停止定时刷新并取消正在进行的后台检查
// 只发取消信号，不等待联网结束，避免拖慢关停
func (m *Manager) StopBackgroundRefresh() {
	m.refreshMu.Lock()
	if !m.refreshActive {
		m.refreshMu.Unlock()
		return
	}
	m.refreshActive = false
	stop := m.refreshStop
	cancel := m.refreshCancel
	m.refreshStop = nil
	m.refreshCancel = nil
	m.refreshMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if stop != nil {
		close(stop)
	}
}

func (m *Manager) backgroundRefreshLoop(stop <-chan struct{}, interval time.Duration) {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
		}
		// 定时器刚触发时可能已收到停止请求，先复核再联网
		select {
		case <-stop:
			return
		default:
		}

		ctx, cancel := context.WithTimeout(context.Background(), m.refreshTimeoutValue())
		m.refreshMu.Lock()
		if !m.refreshActive || m.refreshStop != stop {
			m.refreshMu.Unlock()
			cancel()
			return
		}
		m.refreshCancel = cancel
		m.refreshMu.Unlock()

		opts := m.backgroundOptions()
		_, err := m.Check(ctx, opts)
		cancel()

		m.refreshMu.Lock()
		if m.refreshActive && m.refreshStop == stop {
			m.refreshCancel = nil
		}
		m.refreshMu.Unlock()

		select {
		case <-stop:
			return
		default:
		}
		if err != nil {
			zap.L().Warn("自动刷新更新检查失败，等待下次重试", zap.Error(err))
		}
		resetRefreshTimer(timer, interval)
	}
}

func (m *Manager) refreshIntervalValue() time.Duration {
	if m.refreshInterval > 0 {
		return m.refreshInterval
	}
	// 与 Python self_update_cache.CACHE_TTL 一致
	return cacheTTL
}

func (m *Manager) refreshTimeoutValue() time.Duration {
	if m.refreshTimeout > 0 {
		return m.refreshTimeout
	}
	return defaultRefreshTimeout
}

func resetRefreshTimer(timer *time.Timer, interval time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(interval)
}
