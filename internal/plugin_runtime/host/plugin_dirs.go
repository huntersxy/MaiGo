package host

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultBuiltinPluginDir 返回 Python 内置插件目录，对齐 integration._get_builtin_plugin_dirs
func DefaultBuiltinPluginDir(projectRoot string) string {
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		return ""
	}
	candidate := filepath.Join(projectRoot, "src", "plugins", "built_in")
	if isDirectory(candidate) {
		return candidate
	}
	return ""
}

// DefaultThirdPartyPluginDir 返回实例插件目录，对齐 integration._get_third_party_plugin_dirs
func DefaultThirdPartyPluginDir(instanceRoot string) string {
	instanceRoot = strings.TrimSpace(instanceRoot)
	if instanceRoot == "" {
		return ""
	}
	candidate := filepath.Join(instanceRoot, "plugins")
	if isDirectory(candidate) {
		return candidate
	}
	return ""
}

// ResolveInstanceRoot 解析实例根目录：优先 MAIBOT_INSTANCE_DIR，否则当前工作目录
func ResolveInstanceRoot() string {
	if root := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")); root != "" {
		return filepath.Clean(root)
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return ""
}

// DefaultRunnerPluginDirs 按分组返回缺省插件目录与受信目录
// Go 宿主目前只启动一个 Runner，third_party 组同时加载内置插件以保持功能对等
func DefaultRunnerPluginDirs(projectRoot string, group RunnerGroup, instanceRoot string) (dirs []string, trusted []string) {
	builtin := DefaultBuiltinPluginDir(projectRoot)
	if group == RunnerGroupBuiltin {
		if builtin != "" {
			return []string{builtin}, []string{builtin}
		}
		return nil, nil
	}
	if builtin != "" {
		dirs = append(dirs, builtin)
		trusted = append(trusted, builtin)
	}
	if thirdParty := DefaultThirdPartyPluginDir(instanceRoot); thirdParty != "" {
		dirs = append(dirs, thirdParty)
	}
	return dirs, trusted
}

// applyDefaultPluginDirs 在未显式配置插件目录时补齐内置与实例插件目录
func applyDefaultPluginDirs(base StartRunnerOptions) StartRunnerOptions {
	if len(base.PluginDirs) == 0 {
		if envDirs := strings.TrimSpace(os.Getenv(EnvPluginDirs)); envDirs != "" {
			base.PluginDirs = filepath.SplitList(envDirs)
		}
	}
	if len(base.TrustedPluginDirs) == 0 {
		if envTrusted := strings.TrimSpace(os.Getenv(EnvTrustedPluginDirs)); envTrusted != "" {
			base.TrustedPluginDirs = filepath.SplitList(envTrusted)
		}
	}
	if len(base.PluginDirs) != 0 {
		return base
	}
	dirs, trusted := DefaultRunnerPluginDirs(base.ProjectRoot, base.Group, ResolveInstanceRoot())
	base.PluginDirs = dirs
	if len(base.TrustedPluginDirs) == 0 {
		base.TrustedPluginDirs = trusted
	}
	return base
}

func isDirectory(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
