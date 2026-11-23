package test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	maibotcfg "MaiGo/internal/config/maibot"

	"github.com/pelletier/go-toml/v2"
)

const modelTaskListsFixture = `[inner]
version = "1.17.26"

[model_task_config.replyer]
model_list = ["test-model"]

[model_task_config.planner]
temperature = 0.5

[model_task_config.memory]
model_list = []

[model_task_config.web_search]
`

func writeModelTaskListsFixture(t *testing.T, root string, contents string) string {
	t.Helper()
	dir := filepath.Join(root, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "model_config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestModelConfigUpgradeWritesEmptyTaskLists(t *testing.T) {
	t.Setenv(maibotcfg.SharedModelsEnv, "")
	root := t.TempDir()
	path := writeModelTaskListsFixture(t, root, modelTaskListsFixture)
	if _, err := maibotcfg.LoadFromInstanceDirWithOptions(root, maibotcfg.LoadOptions{
		DisableLegacyMigration: true,
		ForceWriteback:         true,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Inner struct {
			Version string `toml:"version"`
		} `toml:"inner"`
		Tasks map[string]struct {
			ModelList []string `toml:"model_list"`
		} `toml:"model_task_config"`
	}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Inner.Version != maibotcfg.ModelConfigTargetVersion {
		t.Fatalf("upgraded version = %q, want %q", doc.Inner.Version, maibotcfg.ModelConfigTargetVersion)
	}
	for _, name := range []string{"planner", "memory", "web_search"} {
		if list := doc.Tasks[name].ModelList; list == nil || len(list) != 0 {
			t.Errorf("%s.model_list = %#v, want explicit empty array", name, list)
		}
	}
	if list := doc.Tasks["replyer"].ModelList; !slices.Equal(list, []string{"test-model"}) {
		t.Errorf("replyer.model_list = %v, want [test-model]", list)
	}
}

func TestModelConfigGETDefaultsMissingTaskLists(t *testing.T) {
	root := t.TempDir()
	catalog := filepath.Join(root, "model_catalog.toml")
	if err := os.WriteFile(catalog, []byte("[inner]\nversion = '"+maibotcfg.CatalogVersion+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(maibotcfg.SharedModelsEnv, catalog)
	contents := modelTaskListsFixture + `
[model_task_config.unrecognized]
marker = true
`
	path := writeModelTaskListsFixture(t, root, contents)
	router := setupTestRouter(newTestDeps(root))
	response := serve(router, authenticatedRequest(http.MethodGet, "/api/webui/config/model", ""))
	if response.Code != http.StatusOK {
		t.Fatalf("GET model config: status %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Success bool `json:"success"`
		Config  struct {
			Tasks map[string]map[string]json.RawMessage `json:"model_task_config"`
		} `json:"config"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || !payload.Success {
		t.Fatalf("invalid model config response: %v, %s", err, response.Body.String())
	}
	for name, want := range map[string]string{
		"replyer": `["test-model"]`, "planner": `[]`, "memory": `[]`, "web_search": `[]`,
	} {
		if got := string(payload.Config.Tasks[name]["model_list"]); got != want {
			t.Errorf("%s.model_list = %q, want %q", name, got, want)
		}
	}
	if fields := payload.Config.Tasks["unrecognized"]; len(fields) != 1 || string(fields["marker"]) != "true" {
		t.Errorf("unrecognized task changed: %v", fields)
	}
	if got := string(payload.Config.Tasks["planner"]["temperature"]); got != "0.5" {
		t.Errorf("planner.temperature = %q, want 0.5", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != contents {
		t.Error("GET model config modified the file")
	}
}
