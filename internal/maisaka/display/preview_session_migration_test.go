package display

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacySessionDirsMergesHashedDirectory(t *testing.T) {
	base := t.TempDir()
	hash := "e90f5a34f0f8450bed46a357ebcea2cb"
	source := filepath.Join(base, "maisaka.replyer", hash)
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "1700000000000.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	SetPreviewSessionLookup(func(sessionID string) (PreviewSessionInfo, bool) {
		if sessionID != hash {
			return PreviewSessionInfo{}, false
		}
		return PreviewSessionInfo{Platform: "qq", IsGroup: true, GroupID: "994571792"}, true
	})
	t.Cleanup(func() { SetPreviewSessionLookup(nil) })

	logger := NewPromptPreviewLogger(base)
	migrated, err := logger.MigrateLegacySessionDirs()
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if migrated != 1 {
		t.Fatalf("迁移目录数 = %d, 期望 1", migrated)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("历史目录未删除: %v", err)
	}
	target := filepath.Join(base, "maisaka.replyer", "qq_group_994571792", "1700000000000.json")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("目标文件不存在: %v", err)
	}
	if second, err := logger.MigrateLegacySessionDirs(); err != nil || second != 0 {
		t.Fatalf("重复迁移 = %d, %v", second, err)
	}
}
