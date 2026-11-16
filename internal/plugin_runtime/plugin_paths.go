package pluginruntime

// 内置插件目录解析；安装版 Runner 的源码位于 site-packages。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"MaiGo/internal/plugin_runtime/host"
)

// BuiltinPluginDir 返回内置插件代码目录
func BuiltinPluginDir() string {
	return host.DefaultBuiltinPluginDir(strings.TrimSpace(os.Getenv(host.EnvPluginProjectRoot)))
}

// FindBuiltinPluginPath 按 manifest ID 在内置插件目录中查找插件路径
func FindBuiltinPluginPath(pluginID string) string {
	root := BuiltinPluginDir()
	if root == "" {
		return ""
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pluginPath := filepath.Join(root, entry.Name())
		raw, err := os.ReadFile(filepath.Join(pluginPath, "_manifest.json"))
		if err != nil {
			continue
		}
		manifest, err := parseManifestID(raw)
		if err != nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(manifest), strings.TrimSpace(pluginID)) {
			return pluginPath
		}
	}
	return ""
}

func parseManifestID(raw []byte) (string, error) {
	var manifest struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", err
	}
	return manifest.ID, nil
}
