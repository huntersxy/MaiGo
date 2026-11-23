package test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	runtime "MaiGo/internal/maisaka/runtime"
	"MaiGo/internal/platform_io"
	qq "MaiGo/internal/platform_io/drivers/qq_official"
	"MaiGo/internal/plugin_runtime/host"
	"MaiGo/internal/plugin_runtime/hostports"
	pluginroutes "MaiGo/internal/webui/routers/plugin"
)

type qqTestAPIs map[string]func(context.Context, string, string, map[string]any) (any, error)

func (apis qqTestAPIs) SetAPILocalHandler(name, _ string, _ map[string]any, handler func(context.Context, string, string, map[string]any) (any, error)) bool {
	apis[name] = handler
	return true
}

func (apis qqTestAPIs) RemoveAPIsByPlugin(string) int {
	count := len(apis)
	clear(apis)
	return count
}

func TestQQOfficialCodecPreservesAuthorAndMentionCards(t *testing.T) {
	settings, err := qq.ParseSettings(nil)
	if err != nil || settings.QQOfficial.EnableMapping {
		t.Fatalf("OpenID must be the default: settings=%#v err=%v", settings.Plugin, err)
	}
	payload := map[string]any{
		"id": "message", "group_openid": "group", "timestamp": "2026-11-17T00:00:00Z", "content": "hello <@peer>",
		"author":   map[string]any{"member_openid": "author", "union_openid": "union", "username": "Alice", "member_nickname": "Alice card"},
		"mentions": []any{map[string]any{"member_openid": "peer", "username": "Bob", "member_nickname": "Bob card"}},
	}
	decoded, _, _, err := qq.DecodeMessage("GROUP_AT_MESSAGE_CREATE", payload, "bot", "Bot", "", "Group", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	message, err := runtime.DecodeExternalPluginMessage(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if message.ExternalUserID != "author" || message.Nickname != "Alice" || message.GroupCard != "Alice card" || message.GroupName != "Group" || message.AdditionalConfig["qq_official_author_union_openid"] != "union" {
		t.Fatalf("unexpected author data: %#v", message)
	}
	for _, part := range message.MessageParts {
		if part.Kind == "at" && part.ExternalAtUserID == "peer" && part.AtNickname == "Bob" && part.AtCardname == "Bob card" {
			return
		}
	}
	t.Fatalf("missing mention identities: %#v", message.MessageParts)
}

func TestQQOfficialParsesStructuredModerationTargets(t *testing.T) {
	input := map[string]any{"mute": map[string]any{"allowed_groups": []any{map[string]any{"id": "group", "name": "Display"}}}}
	settings, err := qq.ParseSettings(input)
	if err != nil || len(settings.Mute.AllowedGroups) != 1 || settings.Mute.AllowedGroups[0] != (qq.ConfigTarget{Platform: "qq", Type: "group", ID: "group", Name: "Display"}) {
		t.Fatalf("invalid group targets: %#v, %v", settings.Mute.AllowedGroups, err)
	}
	input["mute"].(map[string]any)["allowed_groups"] = []any{map[string]any{"id": "peer", "type": "user"}}
	if _, err := qq.ParseSettings(input); err == nil {
		t.Fatal("accepted user in allowed groups")
	}
}

func TestQQOfficialMappingWebUI(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	t.Setenv("MAIBOT_INSTANCE_DIR", root)
	configRoot := filepath.Join(root, "config", "builtin_plugins", qq.DirectoryName)
	dataRoot := filepath.Join(root, "data", "plugins", qq.PluginID)
	for path, content := range map[string]string{
		filepath.Join(configRoot, "config.toml"): "[plugin]\nenabled = true\nconfig_version = \"1.0.0\"\n[qq_official]\nenable_mapping = false\n",
		filepath.Join(dataRoot, "id_map.json"):   `{"group":{"group":"9007199254740997"},"user":{"peer":"1234"}}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	database, err := sql.Open("sqlite", filepath.Join(root, "data", "MaiBot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE person_info (platform TEXT, user_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	pluginHost := host.NewHost()
	manager, err := platformio.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(ctx) })
	pluginHost.SetMessageGatewayPort(hostports.NewGateway(manager, nil))
	native := platformio.DefaultNativeRuntime()
	previous, existed := native.Get(qq.PluginID)
	if err := native.Register(platformio.NativeRegistration{
		PluginID: qq.PluginID, DirectoryName: qq.DirectoryName, ConfigRoot: configRoot,
		DefaultConfig: qq.DefaultConfigMap(), Factory: qq.NewFactoryStandalone(),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pluginHost.NativeTeardown(ctx, qq.PluginID); err != nil {
			t.Error(err)
		}
		native.Unregister(qq.PluginID)
		if existed {
			if err := native.Register(previous); err != nil {
				t.Error(err)
			}
		}
	})
	if err := pluginHost.StartNativeRuntimeIfEnabled(ctx, qq.PluginID); err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	router.Route("/api/webui/plugins", func(router chi.Router) {
		pluginroutes.Mount(router, pluginroutes.Deps{PluginHost: pluginHost, RequireAuth: func(writer http.ResponseWriter, request *http.Request) bool {
			if request.Header.Get("Authorization") == "Bearer local-test" {
				return true
			}
			writer.WriteHeader(http.StatusUnauthorized)
			return false
		}})
	})
	request := func(path, body string) map[string]any {
		t.Helper()
		url := "/api/webui/plugins/runtime/webui/" + qq.PluginID + "/group-mappings/" + path
		request := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer local-test")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		var payload struct {
			Success bool           `json:"success"`
			Result  map[string]any `json:"result"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); response.Code != http.StatusOK || err != nil || !payload.Success || payload.Result == nil {
			t.Fatalf("%s: HTTP %d %s", path, response.Code, response.Body.String())
		}
		return payload.Result
	}
	if result := request("queries/identity_mode", `{}`); result["mapping_enabled"] != false {
		t.Fatal(result)
	}
	result := request("queries/mappings", `{}`)
	if groups, ok := result["group"].([]any); !ok || len(groups) != 1 || groups[0].(map[string]any)["id"] != "9007199254740997" {
		t.Fatal(result)
	}
	if result := request("queries/login_status", `{}`); result["connected"] != false {
		t.Fatal(result)
	}
	if result := request("actions/set_mapping_mode", `{"args":{"enabled":true}}`); result["mapping_enabled"] != true {
		t.Fatal(result)
	}
	result = request("queries/mapping_migrations", `{}`)
	if candidates, ok := result["candidates"].([]any); !ok || len(candidates) != 1 {
		t.Fatal(result)
	} else if candidate := candidates[0].(map[string]any); candidate["source"] != "1234" || candidate["status"] != "empty" {
		t.Fatal(candidate)
	} else if targets, ok := candidate["targets"].([]any); !ok || len(targets) != 1 || targets[0] != "peer" {
		t.Fatal(candidate)
	}
	if result := request("actions/delete_bindings", `{"confirmed":true,"args":{"kind":"user","openids_json":"[\"peer\"]"}}`); result["deleted"] != float64(1) {
		t.Fatal(result)
	}
	result = request("queries/mappings", `{}`)
	if users, ok := result["user"].([]any); !ok || len(users) != 0 || result["mapping_enabled"] != true {
		t.Fatal(result)
	}
	if result := request("actions/finish_mapping", `{"confirmed":true}`); result["enabled"] != false {
		t.Fatal(result)
	}
}

func TestQQOfficialRecoversAndDeletesBindingFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	settings := qq.DefaultSettings()
	settings.Plugin.Enabled = true
	api := qqTestAPIs{}
	adapter := qq.NewAdapter(settings, &qq.HostHooks{APIs: api, DataDir: func() string { return dir }})
	journal := filepath.Join(dir, "binding_delete_pending.json")
	data, err := json.Marshal(map[string]string{
		"id_map.json":             `{"group":{},"user":{"peer":"100"}}`,
		"auto_user_profiles.json": `[]`,
		"union_user_ids.json":     `{"version":1,"bindings":[]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Stop(ctx) })
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatalf("journal must be replayed and removed: %v", err)
	}
	deleteBindings := api["webui_delete_bindings"]
	if deleteBindings == nil {
		t.Fatal("delete binding API missing")
	}
	if _, err := deleteBindings(ctx, qq.PluginID, "webui_delete_bindings", map[string]any{"kind": "user", "openids_json": `["peer"]`}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "id_map.json"))
	var mapping struct{ User map[string]string }
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &mapping); err != nil || mapping.User == nil || len(mapping.User) != 0 {
		t.Fatalf("binding not deleted: %s, %v", raw, err)
	}
}
