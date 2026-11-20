package webuiapi

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalCacheStatsSkipsTablesAndCaches(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	images := filepath.Join(data, "images")
	if err := os.MkdirAll(images, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(images, "a.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "MaiBot.db"), []byte("not-a-real-db"), 0o644); err != nil {
		t.Fatal(err)
	}

	invalidateLocalCacheStatsCache()
	paths, err := resolveSystemCachePaths(root)
	if err != nil {
		t.Fatal(err)
	}

	first := buildLocalCacheStatsResponse(paths)
	if len(first.Database.Tables) != 0 {
		t.Fatalf("overview should skip tables, got %d", len(first.Database.Tables))
	}
	if got := first.Directories[0].FileCount; got != 1 {
		t.Fatalf("images file_count=%d", got)
	}
	_ = storeLocalCacheStats(first)

	cached, hit := getCachedLocalCacheStats()
	if !hit {
		t.Fatal("expected cache hit")
	}
	if cached.Directories[0].FileCount != 1 {
		t.Fatalf("cached file_count=%d", cached.Directories[0].FileCount)
	}

	invalidateLocalCacheStatsCache()
	if _, hit := getCachedLocalCacheStats(); hit {
		t.Fatal("cache should be cleared after invalidate")
	}
}

func TestDatabaseStatsCacheTTL(t *testing.T) {
	invalidateLocalCacheStatsCache()
	stats := databaseStorageStats{Backend: "sqlite", TotalSize: 7}
	_ = storeDatabaseStats(stats)
	got, hit := getCachedDatabaseStats()
	if !hit || got.TotalSize != 7 {
		t.Fatalf("hit=%v size=%d", hit, got.TotalSize)
	}
	localCacheStatsMu.Lock()
	databaseStatsExpiry = time.Now().Add(-time.Second)
	localCacheStatsMu.Unlock()
	if _, hit := getCachedDatabaseStats(); hit {
		t.Fatal("expired cache should miss")
	}
}
