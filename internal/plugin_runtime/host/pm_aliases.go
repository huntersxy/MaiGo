package host

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// SetInstanceRoot 注入 Bot 实例根目录，供实例级命令配置定位
func (h *Host) SetInstanceRoot(root string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.instanceRoot = strings.TrimSpace(root)
	h.mu.Unlock()
}

func (h *Host) pmConfigPath() string {
	h.mu.RLock()
	root := h.instanceRoot
	h.mu.RUnlock()
	if strings.TrimSpace(root) == "" {
		root = ResolveInstanceRoot()
	}
	return filepath.Join(root, "config", "commands", "pm.toml")
}

// readPMConfig 读取 /pm 实例配置；文件不存在时返回空表
func (h *Host) readPMConfig() (map[string]any, error) {
	raw, err := os.ReadFile(h.pmConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var config map[string]any
	if err := toml.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("解析 /pm 配置失败: %w", err)
	}
	if config == nil {
		config = map[string]any{}
	}
	return config, nil
}

// writePMShortcut 新增或删除 /pm 快捷别名；target 为空表示删除
func (h *Host) writePMShortcut(alias, target string) (bool, error) {
	config, err := h.readPMConfig()
	if err != nil {
		return false, err
	}
	aliases, _ := config["aliases"].(map[string]any)
	if aliases == nil {
		aliases = map[string]any{}
		config["aliases"] = aliases
	}
	shortcuts, _ := aliases["shortcuts"].(map[string]any)
	if shortcuts == nil {
		shortcuts = map[string]any{}
		aliases["shortcuts"] = shortcuts
	}
	if target == "" {
		if _, exists := shortcuts[alias]; !exists {
			return false, nil
		}
		delete(shortcuts, alias)
	} else {
		shortcuts[alias] = target
	}
	path := h.pmConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	encoded, err := toml.Marshal(config)
	if err != nil {
		return false, err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return false, err
	}
	return true, nil
}
