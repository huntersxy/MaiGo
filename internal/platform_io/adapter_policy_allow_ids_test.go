package platformio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGetAdapterPolicyAllowIDsAlwaysArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adapter_policy.toml")
	content := "[[adapters]]\n" +
		"plugin_id = \"maibot-team.snowluma-adapter\"\n" +
		"platform = \"qq\"\n" +
		"[adapters.group]\n" +
		"default_action = \"allow\"\n" +
		"[adapters.private]\n" +
		"default_action = \"inherit\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := NewAdapterPolicyManager(path)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := manager.GetAdapterPolicy(AdapterIdentity{
		PluginID: "maibot-team.snowluma-adapter",
		Platform: "qq",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, chatType := range []string{"group", "private"} {
		section, ok := decoded[chatType].(map[string]any)
		if !ok {
			t.Fatalf("%s missing", chatType)
		}
		for _, field := range []string{"allow_ids", "deny_ids"} {
			value, exists := section[field]
			if !exists {
				t.Fatalf("%s.%s missing", chatType, field)
			}
			if value == nil {
				t.Fatalf("%s.%s is null", chatType, field)
			}
			if _, ok := value.([]any); !ok {
				t.Fatalf("%s.%s type=%T want []", chatType, field, value)
			}
		}
	}
}

func TestNormalizeIDListNonListReturnsEmpty(t *testing.T) {
	if got := normalizeIDList(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil -> %#v", got)
	}
	if got := normalizeIDList("x"); got == nil || len(got) != 0 {
		t.Fatalf("string -> %#v", got)
	}
	if got := normalizeIDList([]string{" a ", "a", ""}); len(got) != 1 || got[0] != "a" {
		t.Fatalf("[]string -> %#v", got)
	}
}
