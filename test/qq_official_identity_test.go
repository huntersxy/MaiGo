package test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	runtime "MaiGo/internal/maisaka/runtime"
	qq "MaiGo/internal/platform_io/drivers/qq_official"
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
