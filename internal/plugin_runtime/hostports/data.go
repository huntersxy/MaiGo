package hostports

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var pluginIDPattern = regexp.MustCompile(`^[A-Za-z0-9_]+(?:[.-][A-Za-z0-9_]+)+$`)

// PluginDataDirResolver 返回插件数据目录解析器；base 为空时取 cwd/data/plugins
func PluginDataDirResolver(base string) func(pluginID string) (string, error) {
	root := strings.TrimSpace(base)
	if root == "" {
		root = strings.TrimSpace(os.Getenv("MAIBOT_PLUGIN_DATA_DIR"))
	}
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return func(string) (string, error) { return "", err }
		}
		root = filepath.Join(cwd, "data", "plugins")
	}
	return func(pluginID string) (string, error) {
		pluginID = strings.TrimSpace(pluginID)
		if !pluginIDPattern.MatchString(pluginID) {
			return "", fmt.Errorf("插件 ID 不合法: %s", pluginID)
		}
		dir := filepath.Join(root, pluginID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		return dir, nil
	}
}

func errPort(name string) error {
	return fmt.Errorf("%s 端口未接线：主程序尚未注入对应服务", name)
}

func intFromAny(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		var parsed int
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &parsed); err == nil {
			return parsed
		}
	}
	return fallback
}
