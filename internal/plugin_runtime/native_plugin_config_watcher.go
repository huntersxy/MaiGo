package pluginruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// NativePluginConfigWatcherOptions 按绝对 config.toml 路径轮询原生适配器配置
type NativePluginConfigWatcherOptions struct {
	// ConfigPaths 返回 pluginID -> config.toml 绝对路径
	ConfigPaths func() map[string]string
	OnConfigChanged func(ctx context.Context, pluginID string)
	PollInterval time.Duration
	Debounce     time.Duration
	MinInterval  time.Duration
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

// StartNativePluginConfigWatcher 轮询 NativeRuntime 已登记适配器的 config.toml
func StartNativePluginConfigWatcher(ctx context.Context, opts NativePluginConfigWatcherOptions) {
	if opts.ConfigPaths == nil || opts.OnConfigChanged == nil {
		return
	}
	opts.applyDefaults()
	go func() {
		committed := snapshotNativePluginConfigs(opts.ConfigPaths())
		pending := map[string]time.Time{}
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
						continue
					}
					if deadline, waiting := pending[pluginID]; !waiting || now.After(deadline) {
						pending[pluginID] = now.Add(opts.Debounce)
						continue
					}
					if now.Sub(lastCallback[pluginID]) < opts.MinInterval {
						continue
					}
					delete(pending, pluginID)
					lastCallback[pluginID] = now
					zap.L().Info("检测到原生适配器配置变更", zap.String("plugin_id", pluginID))
					opts.OnConfigChanged(ctx, pluginID)
				}
				for pluginID := range committed {
					if _, exists := current[pluginID]; !exists {
						delete(pending, pluginID)
						delete(lastCallback, pluginID)
					}
				}
				committed = current
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
		if err != nil || info.IsDir() {
			continue
		}
		snapshot[pluginID] = pluginConfigSnapshot{mod: info.ModTime(), size: info.Size()}
	}
	return snapshot
}
