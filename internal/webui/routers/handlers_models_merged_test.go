package webuiapi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModelConfigFileMergedIncludesSharedCatalog(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("创建配置目录失败: %v", err)
	}
	instance := `[inner]
version = '1.17.26'

[model_task_config.planner]
model_list = ['shared-model']
`
	if err := os.WriteFile(filepath.Join(configDir, "model_config.toml"), []byte(instance), 0o600); err != nil {
		t.Fatalf("写入实例配置失败: %v", err)
	}
	hostDir := filepath.Join(root, "host")
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		t.Fatalf("创建宿主目录失败: %v", err)
	}
	catalog := `inner = { version = '3.0.0' }

[[api_providers]]
name = 'qwen'
base_url = 'https://example.test/v1'
api_key = 'k'

[[models]]
name = 'shared-model'
api_provider = 'qwen'
model_identifier = 'shared'
model_type = 'llm'
`
	if err := os.WriteFile(filepath.Join(hostDir, "model_catalog.toml"), []byte(catalog), 0o600); err != nil {
		t.Fatalf("写入共享目录失败: %v", err)
	}
	t.Setenv("MAIBOT_SHARED_MODEL_CATALOG", filepath.Join(hostDir, "model_catalog.toml"))

	deps := Deps{InstanceDir: root}
	file, ok := deps.modelConfigFileMerged()
	if !ok {
		t.Fatal("应能读取合并后的模型配置")
	}
	if _, exists := findModelInfoByName(file.Models, "shared-model"); !exists {
		t.Fatalf("共享目录里的模型应可见，实际模型数=%d", len(file.Models))
	}
}
