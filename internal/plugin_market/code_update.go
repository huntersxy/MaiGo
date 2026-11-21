package pluginmarket

// 插件强制更新与代码、配置备份；运行数据不参与代码替换，对齐 src/common/code_update.py。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// 运行数据目录不参与代码备份与替换，对齐 RUNTIME_DIRECTORIES
var codeUpdateRuntimeDirectories = map[string]struct{}{
	"data": {}, "logs": {}, "log": {}, "temp": {}, "tmp": {}, "cache": {},
	"config_back": {}, ".git": {}, ".venv": {}, "venv": {}, "node_modules": {},
	"__pycache__": {}, ".pytest_cache": {}, ".update_backups": {}, ".update_tmp": {},
}

// 受管配置文件名，不参与代码替换，对齐 CONFIG_NAMES
var codeUpdateConfigNames = map[string]struct{}{
	"config.toml": {}, "bot_config.toml": {}, "model_config.toml": {}, ".env": {},
}

const codeUpdateGitTimeout = 120 * time.Second

// CodeConflict 对齐 update_conflict 的冲突详情，前端按该结构识别 409
type CodeConflict struct {
	Code         string   `json:"code"`
	Message      string   `json:"message"`
	Files        []string `json:"files"`
	TargetCommit string   `json:"target_commit"`
}

// isCodePath 对齐 is_code_path：排除运行数据、配置与本地数据库文件
func isCodePath(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}
	parts := strings.Split(strings.ReplaceAll(trimmed, "\\", "/"), "/")
	for _, part := range parts {
		if _, blocked := codeUpdateRuntimeDirectories[strings.ToLower(part)]; blocked {
			return false
		}
	}
	if strings.EqualFold(parts[0], "config") {
		return false
	}
	base := strings.ToLower(filepath.Base(trimmed))
	if _, blocked := codeUpdateConfigNames[base]; blocked {
		return false
	}
	switch strings.ToLower(filepath.Ext(trimmed)) {
	case ".db", ".sqlite", ".sqlite3", ".log", ".pyc":
		return false
	}
	return true
}

// checkCodePath 对齐 _path：拒绝绝对路径、越界路径与链接路径
func checkCodePath(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("代码路径越界：%s", name)
	}
	for _, part := range strings.Split(strings.ReplaceAll(name, "\\", "/"), "/") {
		if part == ".." {
			return "", fmt.Errorf("代码路径越界：%s", name)
		}
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootResolved := rootAbs
	if resolved, resolveErr := filepath.EvalSymlinks(rootAbs); resolveErr == nil {
		rootResolved = resolved
	}
	candidate := filepath.Join(rootAbs, filepath.FromSlash(name))
	candidateResolved := candidate
	if resolved, resolveErr := filepath.EvalSymlinks(candidate); resolveErr == nil {
		candidateResolved = resolved
	}
	if !isPathWithin(rootResolved, candidateResolved) {
		return "", fmt.Errorf("代码路径越界：%s", name)
	}
	for current := filepath.Clean(candidate); ; current = filepath.Dir(current) {
		if info, statErr := os.Lstat(current); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("代码路径包含链接：%s", name)
		}
		parent := filepath.Dir(current)
		if parent == current || strings.EqualFold(filepath.Clean(current), filepath.Clean(rootAbs)) {
			break
		}
	}
	return candidate, nil
}

// gitOutput 对齐 _git_bytes：统一前置 --literal-pathspecs 并返回标准输出
func gitOutput(ctx context.Context, root string, args ...string) (string, error) {
	fullArgs := append([]string{"--literal-pathspecs", "-C", root}, args...)
	stdout, stderrText, err := runGitCommand(ctx, "", codeUpdateGitTimeout, fullArgs...)
	if err != nil {
		detail := strings.TrimSpace(stderrText)
		if detail == "" {
			detail = strings.TrimSpace(stdout)
		}
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("Git 操作失败：%s", detail)
	}
	return stdout, nil
}

func splitNulPaths(output string) []string {
	parts := strings.Split(output, "\x00")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			names = append(names, part)
		}
	}
	return names
}

// codePaths 对齐 code_paths：工作区、HEAD 与目标提交中的代码路径并集
func codePaths(ctx context.Context, root, target string) ([]string, error) {
	names := map[string]struct{}{}
	for _, args := range [][]string{
		{"ls-files", "-z"},
		{"ls-tree", "-r", "--name-only", "-z", "HEAD"},
		{"ls-tree", "-r", "--name-only", "-z", target},
	} {
		output, err := gitOutput(ctx, root, args...)
		if err != nil {
			return nil, err
		}
		for _, name := range splitNulPaths(output) {
			if isCodePath(name) {
				names[name] = struct{}{}
			}
		}
	}
	paths := make([]string, 0, len(names))
	for name := range names {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	return paths, nil
}

// updateConflict 检查本地代码修改及目标版本会覆盖的未跟踪文件。
func updateConflict(ctx context.Context, root, targetRoot, target string) (*CodeConflict, error) {
	changedOutput, err := gitOutput(ctx, root, "diff", "HEAD", "--name-only", "--no-renames", "-z")
	if err != nil {
		return nil, err
	}
	filesSet := map[string]struct{}{}
	for _, name := range splitNulPaths(changedOutput) {
		if isCodePath(name) {
			filesSet[name] = struct{}{}
		}
	}
	tracked := map[string]struct{}{}
	for _, args := range [][]string{
		{"ls-files", "-z"},
		{"ls-tree", "-r", "--name-only", "-z", "HEAD"},
	} {
		output, err := gitOutput(ctx, root, args...)
		if err != nil {
			return nil, err
		}
		for _, name := range splitNulPaths(output) {
			tracked[name] = struct{}{}
		}
	}
	incoming, err := gitOutput(ctx, targetRoot, "ls-tree", "-r", "--name-only", "-z", target)
	if err != nil {
		return nil, err
	}
	for _, name := range splitNulPaths(incoming) {
		if _, ok := tracked[name]; ok || !isCodePath(name) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name))); err == nil {
			filesSet[name] = struct{}{}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if len(filesSet) == 0 {
		return nil, nil
	}
	files := make([]string, 0, len(filesSet))
	for name := range filesSet {
		files = append(files, name)
	}
	sort.Strings(files)
	commitOutput, err := gitOutput(ctx, targetRoot, "rev-parse", target)
	if err != nil {
		return nil, err
	}
	return &CodeConflict{
		Code:         "local_changes",
		Message:      "存在本地代码修改，可备份后强制更新",
		Files:        files,
		TargetCommit: strings.TrimSpace(commitOutput),
	}, nil
}

// requireNoSymlinkAncestors 拒绝备份目录链上的链接，对齐 _path 对备份路径的检查
func requireNoSymlinkAncestors(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for current := filepath.Clean(absolute); ; current = filepath.Dir(current) {
		if info, statErr := os.Lstat(current); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("代码路径包含链接：%s", path)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return nil
}

// backupCode 对齐 backup_code：保存代码文件、删除状态和暂存差异，并额外保存受管配置
func backupCode(ctx context.Context, root, backup, target string) error {
	paths, err := codePaths(ctx, root, target)
	if err != nil {
		return err
	}
	pathSet := make(map[string]struct{}, len(paths))
	for _, name := range paths {
		pathSet[name] = struct{}{}
	}
	untrackedOutput, err := gitOutput(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	for _, name := range splitNulPaths(untrackedOutput) {
		if isCodePath(name) {
			pathSet[name] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(pathSet))
	for name := range pathSet {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)

	if err := requireNoSymlinkAncestors(backup); err != nil {
		return err
	}
	if _, statErr := os.Lstat(backup); statErr == nil {
		return fmt.Errorf("备份目录已存在：%s", backup)
	}
	if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(backup, 0o755); err != nil {
		return err
	}

	present := make([]string, 0, len(ordered))
	for _, name := range ordered {
		source, err := checkCodePath(root, name)
		if err != nil {
			return err
		}
		info, statErr := os.Lstat(source)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return statErr
		}
		if info.IsDir() {
			return fmt.Errorf("代码文件与本地目录冲突：%s", name)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		destination := filepath.Join(backup, "files", filepath.FromSlash(name))
		if err := copyFile(source, destination); err != nil {
			return err
		}
		present = append(present, name)
	}

	configEntries := []string{"config.toml", "bot_config.toml", "model_config.toml", ".env", "config", "config_back"}
	for _, name := range configEntries {
		source, err := checkCodePath(root, name)
		if err != nil {
			return err
		}
		info, statErr := os.Lstat(source)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return statErr
		}
		destination := filepath.Join(backup, "configuration", filepath.FromSlash(name))
		if info.IsDir() {
			if err := filepath.WalkDir(source, func(child string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				relative, relErr := filepath.Rel(root, child)
				if relErr != nil {
					return relErr
				}
				_, pathErr := checkCodePath(root, relative)
				return pathErr
			}); err != nil {
				return err
			}
			if err := copyDirectory(source, destination); err != nil {
				return err
			}
		} else if info.Mode().IsRegular() {
			if err := copyFile(source, destination); err != nil {
				return err
			}
		}
	}

	commitOutput, err := gitOutput(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	branchOutput, err := gitOutput(ctx, root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	metadata := struct {
		Commit  string   `json:"commit"`
		Branch  string   `json:"branch"`
		Paths   []string `json:"paths"`
		Present []string `json:"present"`
	}{
		Commit:  strings.TrimSpace(commitOutput),
		Branch:  strings.TrimSpace(branchOutput),
		Paths:   ordered,
		Present: present,
	}
	raw, err := sonic.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(backup, "snapshot.json"), raw, 0o644); err != nil {
		return err
	}

	// 只保存代码的暂存差异；diff 不支持 pathspec-from-file，分批避免 Windows 命令长度限制。
	var patches []byte
	for start := 0; start < len(ordered); start += 50 {
		end := start + 50
		if end > len(ordered) {
			end = len(ordered)
		}
		args := []string{"diff", "--cached", "--binary", "HEAD", "--"}
		args = append(args, ordered[start:end]...)
		output, err := gitOutput(ctx, root, args...)
		if err != nil {
			return err
		}
		patches = append(patches, []byte(output)...)
	}
	return os.WriteFile(filepath.Join(backup, "staged.patch"), patches, 0o644)
}

// replaceCode 对齐 replace_code：只检出代码路径，随后移动 HEAD
func replaceCode(ctx context.Context, root, target string) error {
	paths, err := codePaths(ctx, root, target)
	if err != nil {
		return err
	}
	known := map[string]struct{}{}
	for _, args := range [][]string{
		{"ls-files", "-z"},
		{"ls-tree", "-r", "--name-only", "-z", target},
	} {
		output, err := gitOutput(ctx, root, args...)
		if err != nil {
			return err
		}
		for _, name := range splitNulPaths(output) {
			known[name] = struct{}{}
		}
	}
	for _, name := range paths {
		path, err := checkCodePath(root, name)
		if err != nil {
			return err
		}
		if info, statErr := os.Lstat(path); statErr == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("代码文件与本地目录冲突：%s", name)
		}
	}
	restorePaths := make([]string, 0, len(paths))
	for _, name := range paths {
		if _, ok := known[name]; ok {
			restorePaths = append(restorePaths, name)
		}
	}
	if len(restorePaths) > 0 {
		spec, err := os.CreateTemp("", "maibot-code-paths-*")
		if err != nil {
			return err
		}
		specName := spec.Name()
		for _, name := range restorePaths {
			if _, err := spec.WriteString(name + "\x00"); err != nil {
				spec.Close()
				os.Remove(specName)
				return err
			}
		}
		if err := spec.Close(); err != nil {
			os.Remove(specName)
			return err
		}
		_, restoreErr := gitOutput(ctx, root, "restore", "--source="+target, "--staged", "--worktree",
			"--pathspec-from-file="+specName, "--pathspec-file-nul")
		os.Remove(specName)
		if restoreErr != nil {
			return restoreErr
		}
	}
	for _, name := range paths {
		if _, ok := known[name]; ok {
			continue
		}
		path, err := checkCodePath(root, name)
		if err != nil {
			return err
		}
		if info, statErr := os.Lstat(path); statErr == nil && info.Mode().IsRegular() {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	_, err = gitOutput(ctx, root, "reset", "--mixed", target)
	return err
}

// restoreCode 对齐 restore_code：恢复代码文件并重新应用暂存差异
func restoreCode(ctx context.Context, root, backup string) error {
	raw, err := os.ReadFile(filepath.Join(backup, "snapshot.json"))
	if err != nil {
		return err
	}
	var metadata struct {
		Commit  string   `json:"commit"`
		Branch  string   `json:"branch"`
		Paths   []string `json:"paths"`
		Present []string `json:"present"`
	}
	if err := sonic.Unmarshal(raw, &metadata); err != nil {
		return err
	}
	if err := replaceCode(ctx, root, metadata.Commit); err != nil {
		return err
	}
	present := make(map[string]struct{}, len(metadata.Present))
	for _, name := range metadata.Present {
		present[name] = struct{}{}
	}
	for _, name := range metadata.Paths {
		destination, err := checkCodePath(root, name)
		if err != nil {
			return err
		}
		if _, ok := present[name]; ok {
			source := filepath.Join(backup, "files", filepath.FromSlash(name))
			if err := copyFile(source, destination); err != nil {
				return err
			}
			continue
		}
		if info, statErr := os.Lstat(destination); statErr == nil && info.Mode().IsRegular() {
			if err := os.Remove(destination); err != nil {
				return err
			}
		}
	}
	patchPath := filepath.Join(backup, "staged.patch")
	patchInfo, err := os.Stat(patchPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if patchInfo.Size() > 0 {
		absolutePatch, err := filepath.Abs(patchPath)
		if err != nil {
			return err
		}
		if _, err := gitOutput(ctx, root, "apply", "--cached", absolutePatch); err != nil {
			return err
		}
	}
	return nil
}
