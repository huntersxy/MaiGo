package test

import (
	"os"
	"path/filepath"
	"testing"

	"MaiGo/internal/prompt"
)

func TestDefaultPromptOverlayReloadAndLocalePriority(t *testing.T) {
	root := t.TempDir()
	custom := prompt.CustomPromptsRoot(root)
	manager, err := prompt.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.LoadFromDir(custom); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(custom, "zh-CN", "planner_action_judge.prompt"), "fallback custom")
	write(filepath.Join(custom, "en-US", ".versions", "planner_action_judge", "draft.prompt"), "inactive version")
	override := filepath.Join(prompt.DefaultPromptsRoot(root), "en-US", "main", "planner", "common", "planner_action_judge.prompt")
	write(override, "edited default")
	assert := func(expect string) {
		t.Helper()
		resolved, err := manager.ResolveTemplate("planner_action_judge", "en-US", "", true)
		if err != nil || resolved.Template != expect || resolved.Customized {
			t.Fatalf("resolved=%+v err=%v", resolved, err)
		}
		text, err := manager.RenderLocale("en-US", "planner_action_judge", nil)
		if err != nil || text != expect {
			t.Fatalf("rendered=%q err=%v", text, err)
		}
	}
	assert("edited default")
	write(override, "second edit")
	assert("second edit")
}
