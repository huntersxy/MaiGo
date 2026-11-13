package host

import (
	"os"
	"path/filepath"
	"strings"
)

// PluginDirs 返回当前 Runner 已注册的插件根目录，未启动 Runner 时回退到环境变量与默认目录
// 对齐 Python integration._iter_plugin_dirs 的目录来源
func (h *Host) PluginDirs() []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, 4)
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		if _, ok := seen[dir]; ok {
			return
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	if h != nil {
		h.mu.RLock()
		for _, state := range h.runners {
			if state == nil {
				continue
			}
			for _, dir := range state.PluginDirs {
				add(dir)
			}
			for _, dir := range state.TrustedPluginDirs {
				add(dir)
			}
		}
		h.mu.RUnlock()
	}
	if len(out) == 0 {
		for _, dir := range filepath.SplitList(os.Getenv(EnvPluginDirs)) {
			add(dir)
		}
	}
	if len(out) == 0 {
		for _, dir := range []string{filepath.Join("data", "plugins"), "plugins"} {
			if isDirectory(dir) {
				add(dir)
			}
		}
	}
	return out
}
