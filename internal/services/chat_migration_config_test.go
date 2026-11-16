package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChatMigrationConfigRendersInlineAndBlocks(t *testing.T) {
	raw := `[expression]
# 保留这行注释
expression_groups = [
  { platform = "qq", item_id = "1001", rule_type = "private" },
  { platform = "*", item_id = "*", rule_type = "all" },
]

[jargon]
# 黑话组注释
[[jargon.jargon_groups]]
platform = "qq"
item_id = "1001"
rule_type = "private"

[[jargon.jargon_groups]]
platform = "*"
item_id = "*"
rule_type = "all"
`
	path := filepath.Join(t.TempDir(), "bot_config.toml")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	source := map[string]any{"platform": "qq", "user_id": "1001"}
	target := map[string]any{"platform": "qq", "user_id": "1002"}
	snapshot, err := ChatMigrationConfigSnapshotFrom(path, source, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Sections) != 2 {
		t.Fatalf("期望两类配置冲突，实际 %d", len(snapshot.Sections))
	}
	decisions := map[string]string{}
	for _, section := range snapshot.Sections {
		decisions[section.ID] = "source"
	}
	rendered, err := RenderChatMigrationConfig(snapshot, decisions)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "# 保留这行注释") || !strings.Contains(rendered, "# 黑话组注释") {
		t.Fatalf("无关注释丢失:\n%s", rendered)
	}
	if strings.Count(rendered, `item_id = "1001"`) != 2 {
		t.Fatalf("来源规则应保留:\n%s", rendered)
	}
	if strings.Count(rendered, `item_id = "1002"`) != 2 {
		t.Fatalf("来源规则未复制到目标:\n%s", rendered)
	}
}
