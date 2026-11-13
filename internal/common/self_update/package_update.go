package selfupdate

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "github.com/glebarez/go-sqlite"
)

type packageEnvironment struct {
	Prefix     string
	Python     string
	Receipt    string
	UV         string
	Index      string
	Entrypoint string
	ToolDir    string
}

var (
	receiptSpecifierPattern    = regexp.MustCompile(`(?m)^requirements = \[\{ name = "maibot", specifier = "[^"\r\n]*" \}\][ \t]*$`)
	receiptRequirementsPattern = regexp.MustCompile(`(?ms)^requirements\s*=\s*\[(.*?)\]\s*$`)
	receiptKeyPattern          = regexp.MustCompile(`(name|specifier)\s*=\s*"([^"]*)"`)
	receiptFieldNamePattern    = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*=`)
	entrypointListPattern      = regexp.MustCompile(`(?s)entrypoints\s*=\s*\[(.*?)\]`)
	entrypointNamePattern      = regexp.MustCompile(`name\s*=\s*"(maibot|maibot\.exe)"`)
	entrypointPathPattern      = regexp.MustCompile(`install-path\s*=\s*"([^"]+)"`)
)

func applyPackageUpdate(ctx context.Context, root string, pending PendingUpdate, result *UpdateResult) (retErr error) {
	if time.Since(pendingRequestedAt(pending)) > pendingMaxAge {
		return errorf("request_expired", "更新请求已过期，请重新检查")
	}
	local, err := InspectLocal(ctx, root, result.FromVersion)
	if err != nil {
		return err
	}
	if local.Blocker != "" {
		return NewError(local.Blocker, BlockerMessage(local.Blocker))
	}
	if local.Version != pending.BaseCommit {
		return errorf("state_changed", "当前版本已变化，请重新检查更新")
	}
	env, err := resolvePackageEnvironment(root, pending.SourceID)
	if err != nil {
		return err
	}
	result.SourceName = packageSourceName(pending.SourceID)
	target := pending.TargetCommit
	result.ToVersion = target
	installed, err := packageInstalledVersion(ctx, env)
	if err != nil {
		return err
	}
	if installed != pending.BaseCommit {
		return errorf("package_changed", "主程序版本已被外部更新，请重新检查")
	}
	lock, err := acquirePackageUpdateLock(root, env.Prefix)
	if err != nil {
		return err
	}
	defer lock.Release()
	result.Stage = "backup"
	backup, err := backupPackageState(ctx, root, env)
	if err != nil {
		return err
	}
	inventory, err := packageInventory(ctx, env)
	if err != nil {
		backup.restore()
		return err
	}
	backup.requirements = filepath.Join(backup.dir, "requirements.txt")
	if err := os.WriteFile(backup.requirements, []byte(inventory+"\n"), 0o644); err != nil {
		backup.restore()
		return err
	}
	defer func() {
		if retErr == nil {
			return
		}
		if rollbackErr := rollbackPackage(ctx, env, backup.requirements, pending.BaseCommit); rollbackErr != nil {
			result.RollbackError = rollbackErr.Error()
		} else {
			result.RolledBack = true
		}
		backup.restore()
	}()
	result.Stage = "dependencies"
	if err := uvInstall(ctx, env, target); err != nil {
		return err
	}
	result.Stage = "verify"
	if err := verifyPackageVersion(ctx, env, target); err != nil {
		return err
	}
	if err := verifyPackageEntrypoint(ctx, env, target); err != nil {
		return err
	}
	if err := checkPackageConsistency(ctx, env); err != nil {
		return err
	}
	if hasSpecifier, err := validateReceiptRequirements(readFileString(env.Receipt)); err != nil {
		return err
	} else if hasSpecifier {
		if err := updateReceiptSpecifier(env.Receipt, target); err != nil {
			return err
		}
	}
	backup.cleanup()
	return nil
}

func resolvePackageEnvironment(root, sourceID string) (packageEnvironment, error) {
	prefix := ""
	candidates := make([]string, 0, 5)
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Dir(filepath.Dir(executable)))
	}
	candidates = append(candidates, root, strings.TrimSpace(os.Getenv("VIRTUAL_ENV")), filepath.Join(root, ".venv"))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if pathExists(filepath.Join(candidate, "uv-receipt.toml")) {
			prefix = candidate
			break
		}
	}
	if prefix == "" {
		return packageEnvironment{}, errorf("not_uv_tool", "%s", BlockerMessage("not_uv_tool"))
	}
	if !strings.EqualFold(filepath.Base(prefix), "maibot") {
		return packageEnvironment{}, errorf("not_uv_tool", "%s", BlockerMessage("not_uv_tool"))
	}
	python := filepath.Join(prefix, "bin", "python")
	if !pathExists(python) {
		python = filepath.Join(prefix, "Scripts", "python.exe")
	}
	if !pathExists(python) {
		return packageEnvironment{}, errorf("not_uv_tool", "工具环境中没有可用的 Python")
	}
	uv, err := exec.LookPath("uv")
	if err != nil {
		return packageEnvironment{}, errorf("uv_missing", "%s", BlockerMessage("uv_missing"))
	}
	receipt := filepath.Join(prefix, "uv-receipt.toml")
	raw, err := os.ReadFile(receipt)
	if err != nil {
		return packageEnvironment{}, err
	}
	entrypoint := packageEntrypoint(string(raw))
	if entrypoint == "" || !pathExists(entrypoint) {
		return packageEnvironment{}, errorf("not_uv_tool", "找不到 uv 登记的 maibot 命令入口，请手动更新")
	}
	if _, err := validateReceiptRequirements(string(raw)); err != nil {
		return packageEnvironment{}, err
	}
	for _, key := range []string{"constraints", "overrides", "build-constraints"} {
		if receiptKeyHasValue(string(raw), key) {
			return packageEnvironment{}, errorf("not_uv_tool", "带额外依赖约束的工具环境请手动更新")
		}
	}
	index, err := packageIndex(sourceID)
	if err != nil {
		return packageEnvironment{}, err
	}
	return packageEnvironment{
		Prefix: prefix, Python: python, Receipt: receipt, UV: uv, Index: index,
		Entrypoint: entrypoint, ToolDir: filepath.Dir(prefix),
	}, nil
}

func packageEntrypoint(receipt string) string {
	list := entrypointListPattern.FindStringSubmatch(receipt)
	if len(list) != 2 {
		return ""
	}
	found := ""
	for _, block := range strings.Split(list[1], "}") {
		if !entrypointNamePattern.MatchString(block) {
			continue
		}
		match := entrypointPathPattern.FindStringSubmatch(block)
		if len(match) != 2 {
			return ""
		}
		if found != "" {
			return ""
		}
		found = strings.TrimSpace(match[1])
	}
	return found
}

// validateReceiptRequirements 确认 uv 记录里只有 maibot 一个依赖
func validateReceiptRequirements(receipt string) (bool, error) {
	matches := receiptRequirementsPattern.FindAllStringSubmatch(receipt, -1)
	if len(matches) != 1 {
		return false, errorf("not_uv_tool", "自定义工具依赖请通过原安装方式更新")
	}
	body := matches[0][1]
	for _, field := range receiptFieldNamePattern.FindAllStringSubmatch(body, -1) {
		if field[1] != "name" && field[1] != "specifier" {
			return false, errorf("not_uv_tool", "自定义工具依赖请通过原安装方式更新")
		}
	}
	fields := receiptKeyPattern.FindAllStringSubmatch(body, -1)
	if len(fields) == 0 {
		return false, errorf("not_uv_tool", "自定义工具依赖请通过原安装方式更新")
	}
	hasName, hasSpecifier := false, false
	for _, field := range fields {
		switch field[1] {
		case "name":
			if field[2] != "maibot" {
				return false, errorf("not_uv_tool", "自定义工具依赖请通过原安装方式更新")
			}
			hasName = true
		case "specifier":
			hasSpecifier = true
		default:
			return false, errorf("not_uv_tool", "自定义工具依赖请通过原安装方式更新")
		}
	}
	if !hasName {
		return false, errorf("not_uv_tool", "自定义工具依赖请通过原安装方式更新")
	}
	return hasSpecifier, nil
}

func uvInstall(ctx context.Context, env packageEnvironment, target string) error {
	args := []string{
		"--no-config", "pip", "install", "maibot==" + target,
		"--reinstall-package", "maibot",
		"--python", env.Python,
		"--default-index", env.Index,
		"--no-python-downloads",
	}
	return runUV(ctx, env, args...)
}

func verifyPackageVersion(ctx context.Context, env packageEnvironment, target string) error {
	installed, err := packageInstalledVersion(ctx, env)
	if err != nil {
		return err
	}
	if installed != target {
		return errorf("package_version_mismatch", "安装后版本为 %s，期望 %s", installed, target)
	}
	return nil
}

func packageInstalledVersion(ctx context.Context, env packageEnvironment) (string, error) {
	code := "from importlib.metadata import version; print(version(\"maibot\"))"
	command := exec.CommandContext(ctx, env.Python, "-I", "-c", code)
	command.Env = packageCommandEnv(env.ToolDir)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", errorf("verify_failed", "无法读取已安装版本：%s", strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func verifyPackageEntrypoint(ctx context.Context, env packageEnvironment, target string) error {
	command := exec.CommandContext(ctx, env.Entrypoint, "--version")
	command.Env = packageCommandEnv(env.ToolDir)
	output, err := command.CombinedOutput()
	if err != nil {
		return errorf("package_entrypoint_failed", "命令入口验证失败：%s", strings.TrimSpace(string(output)))
	}
	if value := strings.TrimSpace(string(output)); value != target {
		return errorf("package_entrypoint_failed", "命令入口版本为 %s，期望 %s", value, target)
	}
	return nil
}

func checkPackageConsistency(ctx context.Context, env packageEnvironment) error {
	return runUV(ctx, env, "--no-config", "pip", "check", "--python", env.Python)
}

func runUV(ctx context.Context, env packageEnvironment, args ...string) error {
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	command := exec.CommandContext(runCtx, env.UV, args...)
	command.Dir = env.Prefix
	command.Env = packageCommandEnv(env.ToolDir)
	output, err := command.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		if len(text) > 4000 {
			text = text[len(text)-4000:]
		}
		return errorf("package_update_failed", "uv 命令执行失败：%s", text)
	}
	return nil
}

func packageCommandEnv(toolDir string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		key := item
		if index := strings.IndexByte(item, '='); index >= 0 {
			key = item[:index]
		}
		if strings.HasPrefix(key, "UV_") || key == "VIRTUAL_ENV" || key == "PYTHONPATH" || key == "PYTHONHOME" {
			continue
		}
		env = append(env, item)
	}
	if toolDir != "" {
		env = append(env, "UV_TOOL_DIR="+toolDir)
	}
	return env
}

func updateReceiptSpecifier(receipt, target string) error {
	raw, err := os.ReadFile(receipt)
	if err != nil {
		return err
	}
	replacement := `requirements = [{ name = "maibot", specifier = "==` + target + `" }]`
	updated := receiptSpecifierPattern.ReplaceAllString(string(raw), replacement)
	if updated == string(raw) {
		return errorf("receipt_update_failed", "无法在 uv-receipt.toml 中定位 maibot 版本声明")
	}
	temporary := receipt + ".tmp"
	if err := os.WriteFile(temporary, []byte(updated), 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, receipt)
}

func receiptKeyHasValue(receipt, key string) bool {
	for _, line := range strings.Split(receipt, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, key) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, key))
		if !strings.HasPrefix(rest, "=") {
			continue
		}
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "="))
		return rest != "" && rest != "[]"
	}
	return false
}

func readFileString(path string) string {
	raw, _ := os.ReadFile(path)
	return string(raw)
}

type packageBackup struct {
	dir          string
	root         string
	sqliteFiles  map[string]string
	receipt      []byte
	receiptPath  string
	requirements string
}

func backupPackageState(ctx context.Context, root string, env packageEnvironment) (*packageBackup, error) {
	if err := os.MkdirAll(StateDir(root), 0o755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(StateDir(root), "package-backup-")
	if err != nil {
		return nil, err
	}
	backup := &packageBackup{dir: dir, sqliteFiles: make(map[string]string)}
	raw, err := os.ReadFile(env.Receipt)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	backup.receipt = raw
	backup.receiptPath = env.Receipt
	backup.root = root
	if configDir := filepath.Join(root, "config"); pathExists(configDir) {
		if err := copyDirectory(configDir, filepath.Join(dir, "config")); err != nil {
			return nil, err
		}
	}
	dataRoot := filepath.Join(root, "data")
	if pathExists(dataRoot) {
		err := filepath.WalkDir(dataRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, relErr := filepath.Rel(dataRoot, path)
			if relErr != nil {
				return relErr
			}
			if entry.IsDir() {
				if relative == "self_update" {
					return filepath.SkipDir
				}
				return nil
			}
			lower := strings.ToLower(entry.Name())
			if !strings.HasSuffix(lower, ".db") && !strings.HasSuffix(lower, ".sqlite") && !strings.HasSuffix(lower, ".sqlite3") {
				return nil
			}
			target := filepath.Join(dir, "data", relative)
			if err := backupSQLiteDatabase(ctx, path, target); err != nil {
				return err
			}
			backup.sqliteFiles[path] = target
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return backup, nil
}

func (b *packageBackup) restore() {
	if configDir := filepath.Join(b.dir, "config"); pathExists(configDir) && b.root != "" {
		_ = copyDirectory(configDir, filepath.Join(b.root, "config"))
	}
	for source, saved := range b.sqliteFiles {
		// 先移除旧 WAL/SHM，避免旧日志应用到回滚后的主库
		_ = os.Remove(source + "-wal")
		_ = os.Remove(source + "-shm")
		temporary := source + ".restore.tmp"
		if err := copyFile(saved, temporary); err == nil {
			_ = os.Rename(temporary, source)
		} else {
			_ = os.Remove(temporary)
		}
	}
	if len(b.receipt) > 0 {
		_ = os.WriteFile(b.receiptPath, b.receipt, 0o644)
	}
	_ = os.RemoveAll(b.dir)
}

// backupSQLiteDatabase 使用 SQLite 在线备份生成一致快照
func backupSQLiteDatabase(ctx context.Context, source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	db, err := sql.Open("sqlite", source)
	if err != nil {
		return errorf("package_backup_failed", "打开 SQLite 失败: %s: %v", source, err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		_ = os.Remove(target)
		return errorf("package_backup_failed", "连接 SQLite 失败: %s: %v", source, err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		_ = os.Remove(target)
		return errorf("package_backup_failed", "设置 SQLite 超时失败: %s: %v", source, err)
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO "+sqliteStringLiteral(target)); err != nil {
		_ = os.Remove(target)
		return errorf("package_backup_failed", "备份 SQLite 失败: %s: %v", source, err)
	}
	return nil
}

func sqliteStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func (b *packageBackup) cleanup() {
	_ = os.RemoveAll(b.dir)
}

func packageInventory(ctx context.Context, env packageEnvironment) (string, error) {
	code := "from importlib.metadata import distributions; print('\\n'.join(f'{d.name}=={d.version}' for d in distributions()))"
	command := exec.CommandContext(ctx, env.Python, "-I", "-c", code)
	command.Env = packageCommandEnv(env.ToolDir)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", errorf("verify_failed", "读取已安装依赖失败：%s", strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func rollbackPackage(ctx context.Context, env packageEnvironment, requirements, version string) error {
	if requirements == "" {
		return nil
	}
	if err := runUV(ctx, env, "--no-config", "pip", "sync", "--python", env.Python,
		"--default-index", env.Index, "--reinstall", requirements); err != nil {
		return errorf("rollback_failed", "同步依赖快照失败：%s", err.Error())
	}
	if err := verifyPackageVersion(ctx, env, version); err != nil {
		return errorf("rollback_failed", "回滚后的主程序版本不一致：%s", err.Error())
	}
	if err := verifyPackageEntrypoint(ctx, env, version); err != nil {
		return errorf("rollback_failed", "回滚后的命令入口验证失败：%s", err.Error())
	}
	if err := checkPackageConsistency(ctx, env); err != nil {
		return errorf("rollback_failed", "回滚后的依赖一致性检查失败：%s", err.Error())
	}
	return nil
}

func copyFile(source, target string) error {
	raw, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, raw, 0o644)
}

func copyDirectory(source, target string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if info.IsDir() {
			return os.MkdirAll(destination, info.Mode().Perm())
		}
		return copyFile(path, destination)
	})
}
