package maibot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadModelConfigMergesSharedCatalogIntoRuntimeConfig(t *testing.T) {
	root := t.TempDir()
	catalogPath := filepath.Join(root, "host", "model_catalog.toml")
	if err := os.MkdirAll(filepath.Dir(catalogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	catalog := `[inner]
version = "3.0.0"

[[api_providers]]
name = "local"
base_url = "http://127.0.0.1:8080"
api_key = "test"
auth_type = "bearer"

[[models]]
name = "local-model"
model_identifier = "local-model"
api_provider = "local"
model_type = "llm"
`
	if err := os.WriteFile(catalogPath, []byte(catalog), 0o600); err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(root, "config", "model_config.toml")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatal(err)
	}
	local := `[inner]
version = "1.17.26"

[model_task_config.replyer]
model_list = ["local-model"]
`
	if err := os.WriteFile(localPath, []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SharedModelsEnv, catalogPath)
	var out InstanceConfig
	if err := loadModelConfigFile(localPath, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Model.ApiProviders) != 1 || out.Model.ApiProviders[0].Name != "local" {
		t.Fatalf("公共供应商未进入运行配置: %#v", out.Model.ApiProviders)
	}
	if len(out.Model.Models) != 1 || out.Model.Models[0].Name != "local-model" {
		t.Fatalf("公共模型未进入运行配置: %#v", out.Model.Models)
	}
}
