package capabilities

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageTrackerQuarantinesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin_usage.json")
	if err := os.WriteFile(path, []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	tracker := NewPluginUsageTracker(path)
	if err := tracker.Load(); err != nil {
		t.Fatalf("损坏记录不应阻止加载: %v", err)
	}
	if totals := tracker.Snapshot(); len(totals) != 0 {
		t.Fatalf("损坏记录应重新计时，实际: %#v", totals)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	quarantined := false
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".corrupt-") {
			quarantined = true
		}
	}
	if !quarantined {
		t.Fatal("损坏记录原文件应被隔离保留")
	}
}
