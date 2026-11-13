package pluginruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// 插件源码监听默认参数，对齐 Python integration.py:1866-1872 的 FileWatcher 配置
const (
	defaultPluginWatchPollInterval = 500 * time.Millisecond
	defaultPluginWatchDebounce     = 600 * time.Millisecond
	// defaultPluginWatchMinInterval 两次重载之间的最小间隔，避免连续变更反复重启
	defaultPluginWatchMinInterval = 2 * time.Second
)

// PluginSourceWatcherOptions 插件源码监听选项
type PluginSourceWatcherOptions struct {
	// PluginDirs 返回当前需要监听的插件根目录
	PluginDirs func() []string
	// OnSourceChanged 源码或清单变化后触发，调用方负责依赖同步与 Runner 重启
	OnSourceChanged func(ctx context.Context)
	// PollInterval 轮询间隔
	PollInterval time.Duration
	// Debounce 防抖窗口
	Debounce time.Duration
	// MinInterval 两次回调之间的最小间隔
	MinInterval time.Duration
}

func (o *PluginSourceWatcherOptions) applyDefaults() {
	if o.PollInterval <= 0 {
		o.PollInterval = defaultPluginWatchPollInterval
	}
	if o.Debounce <= 0 {
		o.Debounce = defaultPluginWatchDebounce
	}
	if o.MinInterval <= 0 {
		o.MinInterval = defaultPluginWatchMinInterval
	}
}

// IsWatchablePluginSource 判断路径是否为需要监听的插件源码或清单
// 对齐 Python integration.py:2107-2119 _is_watchable_plugin_source
func IsWatchablePluginSource(path string, pluginDirs []string) bool {
	name := filepath.Base(path)
	if name != "_manifest.json" && filepath.Ext(name) != ".py" {
		return false
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, rawRoot := range pluginDirs {
		root, err := filepath.Abs(strings.TrimSpace(rawRoot))
		if err != nil || root == "" {
			continue
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		first := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		if !isReservedPluginDependencyDirectory(first) {
			return true
		}
	}
	return false
}

// StartPluginSourceWatcher 轮询插件目录，源码或清单变化后防抖触发回调
// 对齐 Python integration.py:1857-1877 的 _start_plugin_file_watcher
func StartPluginSourceWatcher(ctx context.Context, opts PluginSourceWatcherOptions) {
	if opts.PluginDirs == nil || opts.OnSourceChanged == nil {
		return
	}
	opts.applyDefaults()
	go func() {
		committed := snapshotPluginSources(opts.PluginDirs())
		var debounceUntil time.Time
		var lastCallback time.Time
		ticker := time.NewTicker(opts.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current := snapshotPluginSources(opts.PluginDirs())
				if !pluginSourceSnapshotsDiffer(committed, current) {
					continue
				}
				// 变更仍在沉降时不断顺延防抖窗口
				if debounceUntil.IsZero() || time.Now().After(debounceUntil) {
					debounceUntil = time.Now().Add(opts.Debounce)
					continue
				}
				if time.Since(lastCallback) < opts.MinInterval {
					continue
				}
				committed = current
				debounceUntil = time.Time{}
				lastCallback = time.Now()
				zap.L().Info("检测到插件源码变更，准备重新同步依赖并重启 Runner")
				opts.OnSourceChanged(ctx)
			}
		}
	}()
}

// pluginSourceSnapshot 单个可监听文件的修改时间与大小
type pluginSourceSnapshot struct {
	mod  time.Time
	size int64
}

// snapshotPluginSources 扫描插件目录下所有可监听文件
func snapshotPluginSources(pluginDirs []string) map[string]pluginSourceSnapshot {
	snapshot := map[string]pluginSourceSnapshot{}
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
			_ = filepath.WalkDir(pluginPath, func(path string, d os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return nil
				}
				if d.IsDir() {
					if path != pluginPath && isReservedPluginDependencyDirectory(d.Name()) {
						return filepath.SkipDir
					}
					return nil
				}
				if !IsWatchablePluginSource(path, []string{root}) {
					return nil
				}
				info, err := d.Info()
				if err != nil {
					return nil
				}
				snapshot[path] = pluginSourceSnapshot{mod: info.ModTime(), size: info.Size()}
				return nil
			})
		}
	}
	return snapshot
}

// pluginSourceSnapshotsDiffer 判断两份快照是否有差异
func pluginSourceSnapshotsDiffer(left, right map[string]pluginSourceSnapshot) bool {
	if len(left) != len(right) {
		return true
	}
	for path, snap := range left {
		other, ok := right[path]
		if !ok || other.mod != snap.mod || other.size != snap.size {
			return true
		}
	}
	return false
}
