package test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	pluginruntime "MaiGo/internal/plugin_runtime"
	"MaiGo/internal/plugin_runtime/host"
	pluginroutes "MaiGo/internal/webui/routers/plugin"
	chinesetypo "MaiGo/plugins/built_in/chinese_typo"
)

func TestNativeChineseTypoWebUI(t *testing.T) {
	root := t.TempDir()
	t.Setenv(host.EnvPluginProjectRoot, "")
	t.Setenv(host.EnvPluginSpawn, "0")
	h := host.NewHost()
	h.SetInstanceRoot(root)
	ctx := context.Background()
	const id = chinesetypo.PluginID
	status, err := h.ApplyPluginConfig(ctx, id)
	if err != nil || status != "success" || h.HasActiveRunner() {
		t.Fatalf("cold start without Python: %q, %v", status, err)
	}
	if h.LoadedPluginVersions()[id] != "1.0.0" {
		t.Fatalf("native plugin version is missing from the Host catalog: %#v", h.LoadedPluginVersions())
	}
	h.SyncPluginDependencies(ctx, nil, nil, nil)
	if h.BlockedPluginReasons()[id] == "" {
		t.Fatal("Python Runner may load a second copy of the plugin")
	}
	configPath := filepath.Join(root, "config", "builtin_plugins", "chinese_typo", "config.toml")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatal(err)
	}
	deps := pluginroutes.Deps{PluginHost: h, InstanceDir: root, RequireAuth: func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") == "Bearer local-test" {
			return true
		}
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}}
	router := chi.NewRouter()
	router.Route("/api/webui/plugins", func(r chi.Router) { pluginroutes.Mount(r, deps) })
	request := func(method, path, body string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, "/api/webui/plugins"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer local-test")
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || response.Code != want {
			t.Fatalf("%s %s = HTTP %d %s, want %d", method, path, response.Code, response.Body.String(), want)
		}
		return payload
	}
	installed := func() map[string]any {
		list := request(http.MethodGet, "/installed", "", http.StatusOK)["plugins"].([]any)
		var record map[string]any
		for _, value := range list {
			item := value.(map[string]any)
			if item["id"] == id {
				if record != nil {
					t.Fatal("duplicate native plugin in installed list")
				}
				record = item
			}
		}
		return record
	}
	if record := installed(); record["builtin"] != true || record["load_status"] != "success" || record["enabled"] != true ||
		len(record["manifest"].(map[string]any)["dependencies"].([]any)) != 0 {
		t.Fatalf("native plugin catalog record: %#v", record)
	}
	base := "/config/" + id
	bundle := request(http.MethodGet, base+"/bundle", "", http.StatusOK)
	sections := bundle["schema"].(map[string]any)["sections"].(map[string]any)
	ruleField := sections["keyword_reaction"].(map[string]any)["fields"].(map[string]any)["rules"].(map[string]any)
	if ruleField["item_type"] != "object" || len(ruleField["item_fields"].(map[string]any)) != 3 {
		t.Fatalf("missing repeatable rule fields: %#v", ruleField)
	}
	if widgets := request(http.MethodGet, base+"/widgets", "", http.StatusOK); len(widgets["widgets"].([]any)) != 0 {
		t.Fatalf("unexpected widget data: %#v", widgets)
	}
	request(http.MethodPut, base, `{"config":{"typo.mode":"dictionary","typo.dictionary_probability":1,"keyword_reaction.rules":[{"keywords":[" 欢迎 "],"regex":["(?P<ignored>.)"],"reaction":"  保留[ignored]  "},{"regex":["[","(?<=欢迎)(?P<name>麦麦)(?P=name)(?P<missing>!)?"],"reaction":"向[name]打招呼[missing]"}]}}`, http.StatusOK)
	request(http.MethodPut, base, `{"config":{"typo.dictionary_probability":1.5}}`, http.StatusBadRequest)
	request(http.MethodPut, base, `{"config":{"keyword_reaction.rules":[{"keywords":[5]}]}}`, http.StatusBadRequest)
	request(http.MethodPut, base, `{"config":{"typo.mode":"unknown"}}`, http.StatusBadRequest)
	emit := func() string {
		t.Helper()
		args := map[string]any{"match_text": "欢迎麦麦麦麦", "extra_prompt": "前置要求", "session_id": "session", "reply_tool_args": map[string]any{"media": true}}
		result := h.EmitHook(ctx, "maisaka.replyer.before_request", args)
		if len(result.Errors) != 0 || result.Kwargs["reply_tool_args"].(map[string]any)["media"] != true || args["extra_prompt"] != "前置要求" {
			t.Fatalf("hook payload corrupted: %#v", result)
		}
		return result.Kwargs["extra_prompt"].(string)
	}
	if prompt := emit(); !strings.Contains(prompt, "- 保留[ignored]\n- 向麦麦打招呼") || !strings.HasPrefix(prompt, "前置要求\n") {
		t.Fatal(prompt)
	}
	if response := request(http.MethodPost, base+"/toggle", "", http.StatusOK); response["enabled"] != false || response["runtime_status"] != "inactive" {
		t.Fatalf("toggle off: %#v", response)
	}
	if result := installed(); result["load_status"] != "disabled" || emit() != "前置要求" {
		t.Fatalf("disabled plugin still executing: %#v", result)
	}
	request(http.MethodPost, base+"/toggle", "", http.StatusOK)
	if installed()["load_status"] != "success" || !strings.Contains(emit(), "向麦麦打招呼") {
		t.Fatal("enable did not restore the persisted rules")
	}
	boolString := `{"config":"[plugin]\nenabled = \"n\"\nconfig_version = \"1.0.0\"\n"}`
	request(http.MethodPut, base+"/raw", boolString, http.StatusOK)
	if row := installed(); row["enabled"] != false || row["load_status"] != "disabled" {
		t.Fatalf("TOML bool coercion differs between catalog and runtime: %#v", row)
	}
	request(http.MethodPost, base+"/toggle", "", http.StatusOK)
	invalid := `{"config":"[plugin]\nconfig_version = \"1.0.0\"\n[typo]\nmode = \"invalid\"\n"}`
	if result := request(http.MethodPut, base+"/raw", invalid, http.StatusOK); result["runtime_status"] != "config_error" {
		t.Fatalf("raw TOML status: %#v", result)
	}
	if record := installed(); record["load_status"] != "failed" || record["load_error"] == "" || emit() != "前置要求" {
		t.Fatalf("invalid config did not disable the hooks: %#v", record)
	}
	request(http.MethodPost, base+"/reset", "", http.StatusOK)
	if installed()["load_status"] != "success" || emit() != "前置要求" || len(h.Components().ComponentsByPlugin(id, false, "")) != 2 {
		t.Fatal("reset did not restore the original defaults and both hooks")
	}
	result, err := h.UnloadPlugins(ctx, []string{id}, "test")
	if err != nil || !result.Success || len(h.Components().ComponentsByPlugin(id, false, "")) != 0 {
		t.Fatalf("unload without Runner: %#v, %v", result, err)
	}
	if result, err := h.ReloadPlugins(ctx, []string{id}, "test", nil); err != nil || !result.Success || installed()["load_status"] != "success" {
		t.Fatalf("reload without Runner: %#v, %v", result, err)
	}
}

func TestNativeChineseTypoModes(t *testing.T) {
	ctx := context.Background()
	part := func(text, separator string) map[string]any {
		return map[string]any{"text": text, "separator": separator, "is_correction": false, "quote_previous": false}
	}
	plugin := map[string]any{"config_version": "1.0.0"}
	config, _, err := chinesetypo.ParseConfig(map[string]any{"plugin": plugin, "typo": map[string]any{"mode": "dictionary", "dictionary_probability": 1}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := chinesetypo.NewRuntime(config)
	var correctionSeen bool
	for seed := uint64(1); seed <= 16; seed++ {
		segments := []map[string]any{part("``` 现在", "\n"), part("现在 ```", "。"), part("现在", "!"), part("“现在” @现在 /现在", "")}
		out, err := runtime.ApplyTypos(ctx, segments, rand.New(rand.NewPCG(seed, 1)))
		if err != nil {
			t.Fatal(err)
		}
		if out[0].(map[string]any)["text"] != "``` 现在" || out[1].(map[string]any)["text"] != "现在 ```" ||
			out[2].(map[string]any)["text"] != "现再" || out[2].(map[string]any)["separator"] != "!" ||
			out[len(out)-1].(map[string]any)["text"] != "“现在” @现在 /现在" || segments[2]["text"] != "现在" {
			t.Fatalf("protected spans or the single replacement changed: %#v", out)
		}
		if len(out) == 5 {
			correction := out[3].(map[string]any)
			if correction["text"] != "现在" || correction["is_correction"] != true || correction["quote_previous"] != true {
				t.Fatalf("invalid correction: %#v", correction)
			}
			correctionSeen = true
		}
	}
	if !correctionSeen {
		t.Fatal("no dictionary correction emitted")
	}
	config, _, err = chinesetypo.ParseConfig(map[string]any{"plugin": plugin, "typo": map[string]any{
		"error_rate": 1, "min_freq": 0, "tone_error_rate": 0, "word_replace_rate": 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime = chinesetypo.NewRuntime(config)
	var changed bool
	for seed := uint64(1); seed <= 8; seed++ {
		out, err := runtime.ApplyTypos(ctx, []map[string]any{part("事实 实验 需要 现在 银行", "。")}, rand.New(rand.NewPCG(seed, 7)))
		if err != nil {
			t.Fatal(err)
		}
		changed = changed || out[0].(map[string]any)["text"] != "事实 实验 需要 现在 银行"
	}
	if !changed {
		t.Fatal("rule mode never selected a candidate")
	}
}

func TestNativeChineseTypoFileWatcher(t *testing.T) {
	h := host.NewHost()
	h.SetInstanceRoot(t.TempDir())
	id := chinesetypo.PluginID
	if _, err := h.ApplyPluginConfig(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	path, err := h.ResolveNativePluginConfigPath(id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started, applied := make(chan struct{}, 1), make(chan string, 1)
	pluginruntime.StartNativePluginConfigWatcher(ctx, pluginruntime.NativePluginConfigWatcherOptions{
		PollInterval: 20 * time.Millisecond, Debounce: 60 * time.Millisecond, MinInterval: 20 * time.Millisecond,
		ConfigPaths: func() map[string]string {
			select {
			case started <- struct{}{}:
			default:
			}
			return map[string]string{id: path}
		},
		OnConfigChanged: func(ctx context.Context, id string) {
			status, _ := h.ApplyPluginConfig(ctx, id)
			applied <- status
		},
	})
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := os.WriteFile(path, []byte("[plugin]\nenabled = false\nconfig_version = '1.0.0'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-applied:
		if status != "inactive" || h.PluginLoadStatuses()[id] != "inactive" {
			t.Fatalf("watcher did not apply the confirmed file snapshot: %v", status)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
