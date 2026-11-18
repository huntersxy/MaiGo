package webuiapi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModelProviderByNameMergesSharedCatalog(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("创建配置目录失败: %v", err)
	}
	instance := "inner = { version = '1.17.26' }\n\n[model_task_config.embedding]\nmodel_list = []\n"
	if err := os.WriteFile(filepath.Join(configDir, "model_config.toml"), []byte(instance), 0o600); err != nil {
		t.Fatalf("写入实例配置失败: %v", err)
	}
	catalogDir := filepath.Join(root, "host")
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatalf("创建宿主目录失败: %v", err)
	}
	catalog := `inner = { version = '3.0.0' }

[[api_providers]]
name = 'cc'
base_url = 'https://api.example.test/provider/v1'
api_key = 'k'

[[models]]
name = 'demo-model'
api_provider = 'cc'
model_identifier = 'demo'
model_type = 'llm'
`
	if err := os.WriteFile(filepath.Join(catalogDir, "model_catalog.toml"), []byte(catalog), 0o600); err != nil {
		t.Fatalf("写入共享目录失败: %v", err)
	}
	t.Setenv("MAIBOT_SHARED_MODEL_CATALOG", filepath.Join(catalogDir, "model_catalog.toml"))

	deps := Deps{InstanceDir: root}
	provider, ok := deps.modelProviderByName("cc")
	if !ok {
		t.Fatal("宿主模式下应能解析共享目录里的供应商")
	}
	if provider["base_url"] != "https://api.example.test/provider/v1" {
		t.Fatalf("供应商字段不符: %#v", provider)
	}
	model, ok := deps.modelConfigModelByName("demo-model")
	if !ok {
		t.Fatal("宿主模式下应能解析共享目录里的模型")
	}
	if model["api_provider"] != "cc" {
		t.Fatalf("模型字段不符: %#v", model)
	}
}

func TestModelProviderByNameWithoutSharedCatalog(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("创建配置目录失败: %v", err)
	}
	instance := `[[api_providers]]
name = 'local'
base_url = 'https://local.example.test/v1'
`
	if err := os.WriteFile(filepath.Join(configDir, "model_config.toml"), []byte(instance), 0o600); err != nil {
		t.Fatalf("写入实例配置失败: %v", err)
	}
	t.Setenv("MAIBOT_SHARED_MODEL_CATALOG", "")

	deps := Deps{InstanceDir: root}
	if _, ok := deps.modelProviderByName("local"); !ok {
		t.Fatal("无共享目录时应回落到实例配置")
	}
	if _, ok := deps.modelProviderByName("cc"); ok {
		t.Fatal("无共享目录时不应解析出共享供应商")
	}
}
