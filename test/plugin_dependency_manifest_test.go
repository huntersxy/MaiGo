package test

import (
	"os"
	"path/filepath"
	"testing"

	pluginruntime "MaiGo/internal/plugin_runtime"
)

// writeTestPlugin 在 dir 下生成一个最小可解析的插件目录
func writeTestPlugin(t *testing.T, dir, pluginID string, withEntry bool, withManifest bool) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建插件目录失败: %v", err)
	}
	if withEntry {
		if err := os.WriteFile(filepath.Join(dir, "plugin.py"), []byte(""), 0o644); err != nil {
			t.Fatalf("写入 plugin.py 失败: %v", err)
		}
	}
	if withManifest {
		manifest := `{"manifest_version": 2, "id": "` + pluginID + `", "dependencies": []}`
		if err := os.WriteFile(filepath.Join(dir, "_manifest.json"), []byte(manifest), 0o644); err != nil {
			t.Fatalf("写入 _manifest.json 失败: %v", err)
		}
	}
}

// TestCollectPluginDependencyManifestsSkipsNonPluginDirs 确认容器目录与插件数据目录
// 不会被当成插件：plugin_management 是 Go 原生实现（无 plugin.py），
// built_in 只是容器目录，data/plugins/<id> 是插件数据目录。
func TestCollectPluginDependencyManifestsSkipsNonPluginDirs(t *testing.T) {
	root := t.TempDir()

	// 真正的 Python 插件：两份清单文件齐全，应当被收集
	writeTestPlugin(t, filepath.Join(root, "real_plugin"), "vendor.real-plugin", true, true)
	// 容器目录：只有子目录，起不到插件作用
	writeTestPlugin(t, filepath.Join(root, "built_in", "plugin_management"), "builtin.plugin-management", false, true)
	// 插件数据目录：空目录
	if err := os.MkdirAll(filepath.Join(root, "vendor.data-plugin"), 0o755); err != nil {
		t.Fatalf("创建数据目录失败: %v", err)
	}
	// 残缺插件：只有 manifest，没有 plugin.py，应当被跳过（不阻断其它插件）
	writeTestPlugin(t, filepath.Join(root, "broken_plugin"), "vendor.broken", false, true)

	manifests := pluginruntime.CollectPluginDependencyManifests([]string{root})
	if _, ok := manifests["vendor.real-plugin"]; !ok {
		t.Fatalf("应当收集到真实插件，实际: %#v", manifests)
	}
	if _, ok := manifests["builtin.plugin-management"]; ok {
		t.Fatal("built_in 是容器目录，不应被当作插件收集")
	}
	if _, ok := manifests["vendor.data-plugin"]; ok {
		t.Fatal("插件数据目录不应被当作插件收集")
	}
	if _, ok := manifests["vendor.broken"]; ok {
		t.Fatal("缺少 plugin.py 的残缺插件不应被收集")
	}
}

// TestCollectPluginDependencyManifestsIgnoresMissingRoot 根目录不存在时返回空表
func TestCollectPluginDependencyManifestsIgnoresMissingRoot(t *testing.T) {
	manifests := pluginruntime.CollectPluginDependencyManifests([]string{
		filepath.Join(t.TempDir(), "not-exist"),
	})
	if len(manifests) != 0 {
		t.Fatalf("期望空结果，实际: %#v", manifests)
	}
}
