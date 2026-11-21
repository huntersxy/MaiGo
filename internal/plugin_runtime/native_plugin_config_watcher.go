package pluginruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// NativePluginConfigWatcherOptions 按绝对 config.toml 路径轮询原生插件配置
type NativePluginConfigWatcherOptions struct {
	// ConfigPaths 返回 pluginID -> config.toml 绝对路径
	ConfigPaths     func() map[string]string
	OnConfigChanged func(ctx context.Context, pluginID string)
	PollInterval    time.Duration
	Debounce        time.Duration
	MinInterval     time.Duration
}

func (o *NativePluginConfigWatcherOptions) applyDefaults() {
	if o.PollInterval <= 0 {
		o.PollInterval = defaultPluginConfigWatchPollInterval
	}
	if o.Debounce <= 0 {
		o.Debounce = defaultPluginConfigWatchDebounce
	}
	if o.MinInterval <= 0 {
		o.MinInterval = defaultPluginConfigWatchMinInterval
	}
}

// StartNativePluginConfigWatcher 在一次配置变更稳定后通知运行时。
func StartNativePluginConfigWatcher(ctx context.Context, opts NativePluginConfigWatcherOptions) {
	if opts.ConfigPaths == nil || opts.OnConfigChanged == nil {
		return
	}
	opts.applyDefaults()
	go func() {
		committed := snapshotNativePluginConfigs(opts.ConfigPaths())
		type change struct {
			snapshot pluginConfigSnapshot
			deadline time.Time
		}
		pending := map[string]change{}
		lastCallback := map[string]time.Time{}
		ticker := time.NewTicker(opts.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current := snapshotNativePluginConfigs(opts.ConfigPaths())
				now := time.Now()
				for pluginID, snap := range current {
					previous, existed := committed[pluginID]
					if existed && previous == snap {
						delete(pending, pluginID)
						continue
					}
					candidate, waiting := pending[pluginID]
					if !waiting || candidate.snapshot != snap {
						pending[pluginID] = change{snap, now.Add(opts.Debounce)}
						continue
					}
					if now.Before(candidate.deadline) || now.Sub(lastCallback[pluginID]) < opts.MinInterval {
						continue
					}
					delete(pending, pluginID)
					lastCallback[pluginID] = now
					committed[pluginID] = snap
					zap.L().Info("检测到原生插件配置变更", zap.String("plugin_id", pluginID))
					opts.OnConfigChanged(ctx, pluginID)
				}
				for pluginID := range committed {
					if _, exists := current[pluginID]; !exists {
						delete(committed, pluginID)
						delete(pending, pluginID)
						delete(lastCallback, pluginID)
					}
				}
			}
		}
	}()
}

func snapshotNativePluginConfigs(paths map[string]string) map[string]pluginConfigSnapshot {
	snapshot := map[string]pluginConfigSnapshot{}
	for pluginID, path := range paths {
		pluginID = strings.TrimSpace(pluginID)
		path = strings.TrimSpace(path)
		if pluginID == "" || path == "" {
			continue
		}
		info, err := os.Stat(filepath.Clean(path))
		if os.IsNotExist(err) {
			snapshot[pluginID] = pluginConfigSnapshot{}
			continue
		}
		if err != nil || info.IsDir() {
			continue
		}
		snapshot[pluginID] = pluginConfigSnapshot{mod: info.ModTime(), size: info.Size()}
	}
	return snapshot
}
