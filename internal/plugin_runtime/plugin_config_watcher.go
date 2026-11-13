package pluginruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	"maibot-go/internal/common/version"
)

// 插件配置监听默认参数，对齐 Python integration.py:1866-1872 的 FileWatcher 配置
const (
	defaultPluginConfigWatchPollInterval = 500 * time.Millisecond
	defaultPluginConfigWatchDebounce     = 600 * time.Millisecond
	// defaultPluginConfigWatchMinInterval 同一插件两次配置回调之间的最小间隔
	defaultPluginConfigWatchMinInterval = time.Second
)

// PluginConfigWatcherOptions 单插件配置文件监听选项
type PluginConfigWatcherOptions struct {
	// PluginDirs 返回当前需要监听的插件根目录
	PluginDirs func() []string
	// OnConfigChanged 某个插件的 config.toml 变化后触发，参数为插件 ID
	OnConfigChanged func(ctx context.Context, pluginID string)
	// PollInterval 轮询间隔
	PollInterval time.Duration
	// Debounce 防抖窗口
	Debounce time.Duration
	// MinInterval 同一插件两次回调之间的最小间隔
	MinInterval time.Duration
}

func (o *PluginConfigWatcherOptions) applyDefaults() {
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

// StartPluginConfigWatcher 轮询每个插件的 config.toml，变化后按插件 ID 定向回调
// 对齐 Python integration.py:1998-2043 的 _refresh_plugin_config_watch_subscriptions
// 与 :2072-2083 的 _handle_plugin_config_changes：配置变更只影响对应插件
func StartPluginConfigWatcher(ctx context.Context, opts PluginConfigWatcherOptions) {
	if opts.PluginDirs == nil || opts.OnConfigChanged == nil {
		return
	}
	opts.applyDefaults()
	go func() {
		committed := snapshotPluginConfigs(opts.PluginDirs())
		pending := map[string]time.Time{}
		lastCallback := map[string]time.Time{}
		ticker := time.NewTicker(opts.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current := snapshotPluginConfigs(opts.PluginDirs())
				now := time.Now()
				for pluginID, snap := range current {
					previous, existed := committed[pluginID]
					if existed && previous == snap {
						continue
					}
					// 变更仍在沉降时不断顺延该插件的防抖窗口
					if deadline, waiting := pending[pluginID]; !waiting || now.After(deadline) {
						pending[pluginID] = now.Add(opts.Debounce)
						continue
					}
					if now.Sub(lastCallback[pluginID]) < opts.MinInterval {
						continue
					}
					delete(pending, pluginID)
					lastCallback[pluginID] = now
					zap.L().Info("检测到插件配置变更，定向通知该插件", zap.String("plugin_id", pluginID))
					opts.OnConfigChanged(ctx, pluginID)
				}
				// 插件被移除时清理其待处理状态
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

// pluginConfigSnapshot 单个插件配置文件的修改时间与大小
type pluginConfigSnapshot struct {
	mod  time.Time
	size int64
}

// snapshotPluginConfigs 扫描插件目录下每个插件的 config.toml
func snapshotPluginConfigs(pluginDirs []string) map[string]pluginConfigSnapshot {
	snapshot := map[string]pluginConfigSnapshot{}
	for _, rawRoot := range pluginDirs {
		root := strings.TrimSpace(rawRoot)
		if root == "" {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || isReservedPluginDependencyDirectory(entry.Name()) {
				continue
			}
			pluginPath := filepath.Join(root, entry.Name())
			configPath := filepath.Join(pluginPath, "config.toml")
			info, err := os.Stat(configPath)
			if err != nil || info.IsDir() {
				continue
			}
			pluginID, ok := readPluginIDForConfigWatch(pluginPath)
			if !ok {
				continue
			}
			snapshot[pluginID] = pluginConfigSnapshot{mod: info.ModTime(), size: info.Size()}
		}
	}
	return snapshot
}

// readPluginIDForConfigWatch 读取插件目录的规范化插件 ID
func readPluginIDForConfigWatch(pluginPath string) (string, bool) {
	validator := NewManifestValidator("")
	validator.HostVersion = version.String()
	validator.SkipPythonPackageDependencies = true
	return validator.ReadPluginIDFromPluginPath(pluginPath, false)
}
