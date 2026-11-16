package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPMShortcutConfigRoundTrip(t *testing.T) {
	root := t.TempDir()
	host := &Host{}
	host.SetInstanceRoot(root)
	added, err := host.writePMShortcut("/插件列表", "/pm plugin list")
	if err != nil || !added {
		t.Fatalf("写入别名失败: %v", err)
	}
	config, err := host.readPMConfig()
	if err != nil {
		t.Fatalf("读取别名失败: %v", err)
	}
	aliases := config["aliases"].(map[string]any)
	shortcuts := aliases["shortcuts"].(map[string]any)
	if shortcuts["/插件列表"] != "/pm plugin list" {
		t.Fatalf("别名内容不符: %#v", shortcuts)
	}
	if _, err := os.Stat(filepath.Join(root, "config", "commands", "pm.toml")); err != nil {
		t.Fatalf("别名文件未生成: %v", err)
	}
	removed, err := host.writePMShortcut("/插件列表", "")
	if err != nil || !removed {
		t.Fatalf("删除别名失败: %v", err)
	}
	config, _ = host.readPMConfig()
	aliases = config["aliases"].(map[string]any)
	shortcuts = aliases["shortcuts"].(map[string]any)
	if _, exists := shortcuts["/插件列表"]; exists {
		t.Fatalf("别名未删除: %#v", shortcuts)
	}
}
