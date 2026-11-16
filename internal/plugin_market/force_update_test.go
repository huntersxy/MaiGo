package pluginmarket

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 不在 PATH 中")
	}
}

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s 失败: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	runTestGit(t, dir, "init")
	runTestGit(t, dir, "config", "user.email", "test@example.com")
	runTestGit(t, dir, "config", "user.name", "Test User")
	runTestGit(t, dir, "config", "commit.gpgsign", "false")
	runTestGit(t, dir, "config", "core.autocrlf", "false")
	return dir
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateConflictAndBackupRestore(t *testing.T) {
	repo := initTestRepo(t)
	writeTestFile(t, filepath.Join(repo, "_manifest.json"), `{"id":"demo.plugin"}`)
	writeTestFile(t, filepath.Join(repo, "a.py"), "one\n")
	runTestGit(t, repo, "add", ".")
	runTestGit(t, repo, "commit", "-m", "A")
	writeTestFile(t, filepath.Join(repo, "a.py"), "two\n")
	runTestGit(t, repo, "add", "a.py")
	runTestGit(t, repo, "commit", "-m", "B")

	writeTestFile(t, filepath.Join(repo, "a.py"), "local\n")
	runTestGit(t, repo, "add", "a.py")
	writeTestFile(t, filepath.Join(repo, "notes.py"), "untracked\n")
	writeTestFile(t, filepath.Join(repo, "config.toml"), "[plugin]\nenabled = true\n")
	writeTestFile(t, filepath.Join(repo, "data", "state.txt"), "runtime\n")

	conflict, err := updateConflict(context.Background(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if conflict == nil || conflict.Code != "local_changes" {
		t.Fatalf("期望检测到本地修改，实际: %#v", conflict)
	}
	if !containsString(conflict.Files, "a.py") {
		t.Fatalf("冲突文件缺少 a.py: %#v", conflict.Files)
	}
	for _, blocked := range []string{"config.toml", "data/state.txt"} {
		if containsString(conflict.Files, blocked) {
			t.Fatalf("运行数据或配置不应算作代码修改: %s", blocked)
		}
	}

	backup := filepath.Join(t.TempDir(), "backup")
	if err := backupCode(context.Background(), repo, backup, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(backup, "files", "a.py")); err != nil {
		t.Fatalf("代码备份缺少 a.py: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backup, "files", "notes.py")); err != nil {
		t.Fatalf("代码备份缺少未跟踪文件: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backup, "configuration", "config.toml")); err != nil {
		t.Fatalf("配置备份缺少 config.toml: %v", err)
	}

	if err := replaceCode(context.Background(), repo, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if content := readTestFile(t, filepath.Join(repo, "a.py")); content != "two\n" {
		t.Fatalf("强制替换后代码应为 HEAD 内容，实际: %q", content)
	}
	if content := readTestFile(t, filepath.Join(repo, "config.toml")); content == "" {
		t.Fatal("强制替换不应删除受管配置")
	}
	if content := readTestFile(t, filepath.Join(repo, "data", "state.txt")); content != "runtime\n" {
		t.Fatal("强制替换不应触碰运行数据")
	}

	if err := restoreCode(context.Background(), repo, backup); err != nil {
		t.Fatal(err)
	}
	if content := readTestFile(t, filepath.Join(repo, "a.py")); content != "local\n" {
		t.Fatalf("恢复后代码应为本地修改，实际: %q", content)
	}
	if content := readTestFile(t, filepath.Join(repo, "notes.py")); content != "untracked\n" {
		t.Fatal("恢复后应保留未跟踪代码文件")
	}
	if staged := runTestGit(t, repo, "diff", "--cached", "--name-only"); !strings.Contains(staged, "a.py") {
		t.Fatalf("恢复后应重新应用暂存差异，实际: %q", staged)
	}
}

func TestApplyPluginCodeForceAndConflict(t *testing.T) {
	repo := initTestRepo(t)
	writeTestFile(t, filepath.Join(repo, "_manifest.json"), `{"id":"demo.plugin"}`)
	writeTestFile(t, filepath.Join(repo, "a.py"), "one\n")
	runTestGit(t, repo, "add", ".")
	runTestGit(t, repo, "commit", "-m", "A")
	head := runTestGit(t, repo, "rev-parse", "HEAD")
	writeTestFile(t, filepath.Join(repo, "a.py"), "local\n")

	service := NewService()
	wrong := strings.Repeat("a", 40)
	outcome := service.applyPluginCode(context.Background(), repo, "HEAD", true, &wrong, "")
	if outcome.Success || outcome.StatusCode != 409 {
		t.Fatalf("过期的 expected_commit 应返回 409，实际: %#v", outcome)
	}
	if content := readTestFile(t, filepath.Join(repo, "a.py")); content != "local\n" {
		t.Fatal("目标提交校验失败时不应改写代码")
	}

	outcome = service.applyPluginCode(context.Background(), repo, "HEAD", true, &head, "")
	if !outcome.Success || outcome.BackupPath == "" {
		t.Fatalf("强制更新应成功并返回备份路径，实际: %#v", outcome)
	}
	if content := readTestFile(t, filepath.Join(repo, "a.py")); content != "one\n" {
		t.Fatalf("强制更新后应为目标提交内容，实际: %q", content)
	}

	writeTestFile(t, filepath.Join(repo, "a.py"), "local-again\n")
	outcome = service.applyPluginCode(context.Background(), repo, "HEAD", false, nil, "")
	if outcome.Success || outcome.StatusCode != 409 {
		t.Fatalf("非强制更新遇到本地修改应返回 409，实际: %#v", outcome)
	}
	conflict, ok := outcome.Payload.(*CodeConflict)
	if !ok || conflict.Code != "local_changes" || !containsString(conflict.Files, "a.py") {
		t.Fatalf("409 应携带结构化冲突详情，实际: %#v", outcome.Payload)
	}
}

func TestInstallReleaseForceRequiresExpectedCommit(t *testing.T) {
	service := NewService()
	service.SetInstanceDir(t.TempDir())
	entry := &PluginReleaseEntry{
		ID:            "demo.plugin",
		ManifestID:    stringPointer("demo.plugin"),
		RepositoryURL: "https://github.com/example/demo",
		Mode:          "releases",
		Versions:      []PluginRelease{},
	}
	release := &PluginRelease{Version: "1.0.0", Tag: "1.0.0", Commit: strings.Repeat("b", 40)}
	wrong := strings.Repeat("c", 40)
	_, err := service.installRelease(context.Background(), "demo.plugin", entry, release, true, false, "", true, &wrong)
	if marketErr, ok := AsMarketError(err); !ok || marketErr.StatusCode != 409 {
		t.Fatalf("目标提交变化应返回 409，实际: %v", err)
	}
}

func TestLoadSourceSettingsMigratesAndRecovers(t *testing.T) {
	instance := t.TempDir()
	if err := os.MkdirAll(filepath.Join(instance, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(instance, "data", "plugin_market.json")
	writeTestFile(t, path, `{"information_source":"official","download_source":"github"}`)
	settings, err := LoadSourceSettings(instance)
	if err != nil {
		t.Fatal(err)
	}
	if settings.InformationSource != "cnb" || settings.DownloadSource != "github" {
		t.Fatalf("official 源应迁移为 cnb，实际: %#v", settings)
	}

	writeTestFile(t, path, `{"use_github":false}`)
	settings, err = LoadSourceSettings(instance)
	if err != nil {
		t.Fatal(err)
	}
	if settings.InformationSource != "cnb" {
		t.Fatalf("旧 use_github 开关应迁移为 cnb，实际: %#v", settings)
	}

	writeTestFile(t, path, `{broken json`)
	settings, err = LoadSourceSettings(instance)
	if err != nil {
		t.Fatalf("损坏设置不应阻止读取: %v", err)
	}
	if settings != DefaultSourceSettings() {
		t.Fatalf("损坏设置应恢复默认值，实际: %#v", settings)
	}
	entries, err := os.ReadDir(filepath.Join(instance, "data"))
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
		t.Fatal("损坏设置原文件应被隔离保留")
	}
}

func TestBuildCatalogSkipsInvalidItemAndKeepsFallbackRepository(t *testing.T) {
	service := NewService()
	service.SetInstanceDir(t.TempDir())
	items := []map[string]any{
		{
			"id": "market.valid",
			"manifest": map[string]any{
				"manifest_version": float64(2),
				"id":               "demo.valid",
				"name":             "Valid",
				"version":          "1.0.0",
				"urls": map[string]any{
					"repository": "https://github.com/example/valid",
				},
			},
			"stats": map[string]any{"downloads": float64(1)},
		},
		{
			"id": "market.invalid",
			"manifest": map[string]any{
				"manifest_version": float64(2),
				"id":               "demo.invalid",
				"name":             "Invalid",
				"version":          "1.0.0",
			},
			"repository_url": "not-a-repository",
		},
	}
	plugins, err := service.buildCatalog(context.Background(), items, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins) != 1 {
		t.Fatalf("无效条目应只跳过自身，实际插件数: %d", len(plugins))
	}
	if plugins[0].ID != "demo.valid" {
		t.Fatalf("保留了错误插件: %#v", plugins[0])
	}
	if plugins[0].RepositoryURL != "https://github.com/example/valid" {
		t.Fatalf("repository_url 回退失败: %q", plugins[0].RepositoryURL)
	}
	if plugins[0].Stats != nil {
		t.Fatal("统计应由统计服务单独加载")
	}
}

func TestParseReleaseIndexSkipsInvalidEntries(t *testing.T) {
	index, err := ParseReleaseIndex(`{"schema_version":1,"plugins":["broken",{"id":""}]}`)
	if err != nil {
		t.Fatalf("单条无效不应拖垮整个索引: %v", err)
	}
	if len(index.Plugins) != 0 {
		t.Fatalf("无效条目应被跳过，实际: %#v", index.Plugins)
	}
}

func TestParseStatisticsSummary(t *testing.T) {
	stats, err := parseStatisticsSummary(map[string]any{
		"success": true,
		"stats": map[string]any{
			"demo.plugin": map[string]any{
				"plugin_id": "demo.plugin", "downloads": float64(3), "likes": float64(2),
				"dislikes": float64(0), "rating": 4.5, "rating_count": float64(2),
				"comment_count": float64(1),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats["demo.plugin"]["downloads"] != float64(3) {
		t.Fatalf("统计汇总解析错误: %#v", stats)
	}
	if _, err := parseStatisticsSummary(map[string]any{
		"success": true,
		"stats": map[string]any{
			"demo.plugin": map[string]any{"plugin_id": "other.plugin"},
		},
	}); err == nil {
		t.Fatal("不一致的统计插件 ID 应被拒绝")
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
