package test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	maibotcfg "MaiGo/internal/config/maibot"

	"go.uber.org/zap"
)

func TestSharedModelCatalogChangeReloadsRuntimeModel(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	hostDir := filepath.Join(root, "host")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := maibotcfg.WriteDefaultBotConfig(filepath.Join(configDir, "bot_config.toml")); err != nil {
		t.Fatal(err)
	}
	modelConfig := fmt.Sprintf(`[inner]
version = %q

[model_task_config.replyer]
model_list = ["priced-model"]
`, maibotcfg.ModelConfigTargetVersion)
	if err := os.WriteFile(filepath.Join(configDir, "model_config.toml"), []byte(modelConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(hostDir, maibotcfg.CatalogFilename)
	writeCatalog := func(price float64) {
		t.Helper()
		body := fmt.Sprintf(`[inner]
version = %q

[[api_providers]]
name = "priced-provider"
base_url = "http://127.0.0.1:8080"
api_key = "test-key"
auth_type = "bearer"
client_type = "openai"

[[models]]
name = "priced-model"
model_identifier = "priced-model"
api_provider = "priced-provider"
model_type = "llm"
price_in = %g
cache_price_in = 0
price_out = 0
`, maibotcfg.CatalogVersion, price)
		if err := os.WriteFile(catalogPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeCatalog(1)
	t.Setenv(maibotcfg.SharedModelsEnv, catalogPath)

	holder, err := maibotcfg.NewInstanceHolder(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := holder.Config().Model.Models[0].PriceIn; got != 1 {
		t.Fatalf("initial price_in = %v, want 1", got)
	}

	type reloadResult struct {
		price  float64
		scopes []maibotcfg.ReloadScope
	}
	reloaded := make(chan reloadResult, 8)
	holder.RegisterReloadCallback(func(cfg *maibotcfg.InstanceConfig, scopes []maibotcfg.ReloadScope) {
		reloaded <- reloadResult{price: cfg.Model.Models[0].PriceIn, scopes: append([]maibotcfg.ReloadScope(nil), scopes...)}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	holder.StartConfigWatcher(ctx, maibotcfg.ConfigWatcherOptions{
		PollInterval:      20 * time.Millisecond,
		Debounce:          20 * time.Millisecond,
		MinReloadInterval: 20 * time.Millisecond,
		Logger:            zap.NewNop(),
	})

	writeCatalog(2)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case result := <-reloaded:
			if result.price != 2 {
				continue
			}
			if !slices.Contains(result.scopes, maibotcfg.ReloadScopeModel) {
				t.Fatalf("reload scopes = %v, want model", result.scopes)
			}
			if got := holder.Config().Model.Models[0].PriceIn; got != 2 {
				t.Fatalf("runtime price_in = %v, want 2", got)
			}
			return
		case <-deadline:
			t.Fatal("shared model catalog change was not reloaded")
		}
	}
}
