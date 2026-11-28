package test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	maibotcfg "MaiGo/internal/config/maibot"
	memory "MaiGo/internal/common/database"
	llm "MaiGo/internal/llm_models"
	modelstats "MaiGo/internal/services/model_stats"
	statschema "MaiGo/internal/services/statistics/schema"
)

func TestCalculateUsageCostRoundToEven(t *testing.T) {
	got := llm.CalculateUsageCost(0.07, 0.07, 0, 95, 0, 0, 0)
	const want = 0.000007
	if diff := got - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("CalculateUsageCost() = %v, want %v", got, want)
	}
}

func TestRecalcUsageCostsUpdatesEmbeddingZeros(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "host")
	configDir := filepath.Join(root, "config")
	dataDir := filepath.Join(root, "data")
	for _, dir := range []string{catalogDir, configDir, dataDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	catalog := `[inner]
version = "3.0.0"

[[api_providers]]
name = "SiliconFlow"
base_url = "http://127.0.0.1:8080"
api_key = "test"
auth_type = "bearer"
client_type = "openai"

[[models]]
name = "Qwen/Qwen3-Embedding-0.6B"
model_identifier = "Qwen/Qwen3-Embedding-0.6B"
api_provider = "SiliconFlow"
model_type = "embedding"
price_in = 0.07
cache_price_in = 0.07
price_out = 0
`
	if err := os.WriteFile(filepath.Join(catalogDir, maibotcfg.CatalogFilename), []byte(catalog), 0o600); err != nil {
		t.Fatal(err)
	}
	local := `[inner]
version = "1.17.26"

[model_task_config.embedding]
model_list = ["Qwen/Qwen3-Embedding-0.6B"]
`
	if err := os.WriteFile(filepath.Join(configDir, "model_config.toml"), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	bot := `[inner]
version = "1.17.26"
`
	if err := os.WriteFile(filepath.Join(configDir, "bot_config.toml"), []byte(bot), 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := memory.OpenDB(filepath.Join(dataDir, memory.DefaultDatabaseFileName))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := statschema.Ensure(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Format("2006-01-02 15:04:05.000000")
	if err := db.Exec(`INSERT INTO llm_usage(
		model_name, model_assign_name, model_api_provider_name, session_id, task_name, request_type,
		time_cost, timestamp, prompt_tokens, completion_tokens, total_tokens,
		prompt_cache_enabled, prompt_cache_hit_tokens, prompt_cache_miss_tokens, cost
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"Qwen/Qwen3-Embedding-0.6B", "Qwen/Qwen3-Embedding-0.6B", "SiliconFlow", "", "embedding", "embedding",
		0.1, now, 1000, 0, 1000, false, 0, 0, 0.0,
	).Error; err != nil {
		t.Fatal(err)
	}

	report, err := modelstats.RecalcUsageCosts(context.Background(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	if report.Updated != 1 || report.EmbeddingUpdated != 1 {
		t.Fatalf("report = %+v", report)
	}
	var cost float64
	if err := db.Raw(`SELECT cost FROM llm_usage LIMIT 1`).Scan(&cost).Error; err != nil {
		t.Fatal(err)
	}
	const want = 0.00007
	if diff := cost - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("cost = %v, want %v", cost, want)
	}
}
