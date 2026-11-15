package test

import (
	"os"
	"path/filepath"
	"testing"

	pluginruntime "MaiGo/internal/plugin_runtime"
)

// TestLoadPackageIndexSettingsWithoutPyproject 确认缺少 pyproject.toml 时
// 依赖安装不会取消：Go 二进制分发没有 Python 工程文件，应回落到默认源。
func TestLoadPackageIndexSettingsWithoutPyproject(t *testing.T) {
	pipeline := &pluginruntime.PluginDependencyPipeline{ProjectRoot: t.TempDir()}
	settings, err := pipeline.LoadPackageIndexSettings()
	if err != nil {
		t.Fatalf("缺少 pyproject.toml 不应报错，实际: %v", err)
	}
	if len(settings.IndexURLs) != 0 || len(settings.Constraints) != 0 {
		t.Fatalf("无配置时应返回空设置，实际: %+v", settings)
	}
}

// TestLoadPackageIndexSettingsReadsUVIndex 有 pyproject.toml 时仍按 [tool.uv] 读取索引
func TestLoadPackageIndexSettingsReadsUVIndex(t *testing.T) {
	root := t.TempDir()
	content := `[tool.uv]
constraint-dependencies = ["urllib3<3"]
[[tool.uv.index]]
url = "https://mirror.example/simple"
default = true
`
	if err := os.WriteFile(filepath.Join(root, "pyproject.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("写入 pyproject.toml 失败: %v", err)
	}
	pipeline := &pluginruntime.PluginDependencyPipeline{ProjectRoot: root}
	settings, err := pipeline.LoadPackageIndexSettings()
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(settings.IndexURLs) != 1 || settings.IndexURLs[0] != "https://mirror.example/simple" {
		t.Fatalf("索引解析异常: %+v", settings.IndexURLs)
	}
	if len(settings.Constraints) != 1 || settings.Constraints[0] != "urllib3<3" {
		t.Fatalf("约束解析异常: %+v", settings.Constraints)
	}
}

// TestLoadPackageIndexSettingsEnvOverride 环境变量优先于 pyproject.toml
func TestLoadPackageIndexSettingsEnvOverride(t *testing.T) {
	t.Setenv("MAIBOT_PLUGIN_DEP_INDEX_URLS", "https://mirror.a/simple, https://mirror.b/simple")
	pipeline := &pluginruntime.PluginDependencyPipeline{ProjectRoot: t.TempDir()}
	settings, err := pipeline.LoadPackageIndexSettings()
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(settings.IndexURLs) != 2 ||
		settings.IndexURLs[0] != "https://mirror.a/simple" ||
		settings.IndexURLs[1] != "https://mirror.b/simple" {
		t.Fatalf("索引覆盖异常: %+v", settings.IndexURLs)
	}
}
