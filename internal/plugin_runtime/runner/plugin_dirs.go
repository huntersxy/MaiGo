package runner

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultBuiltinPluginDir 返回内置插件目录
//
// Python 的 integration._get_builtin_plugin_dirs 指向 src/plugins/built_in；
// Go 的插件源码不在 Python 的 src 下，改用仓库根的 plugins/built_in。
// 两个候选都探测，优先返回真实存在的那个。
func DefaultBuiltinPluginDir(projectRoot string) string {
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		return ""
	}
	for _, rel := range []string{
		filepath.Join("plugins", "built_in"),
		filepath.Join("src", "plugins", "built_in"),
	} {
		candidate := filepath.Join(projectRoot, rel)
		if isDir(candidate) {
			return candidate
		}
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
	if isDir(candidate) {
		return candidate
	}
	return ""
}

// isDir 判断路径是否为目录
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
