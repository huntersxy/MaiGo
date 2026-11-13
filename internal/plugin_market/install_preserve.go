package pluginmarket

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 运行缓存与虚拟环境不复制到新版本，对齐 _preserve_user_files 的过滤集合
var preservedSkipDirectories = map[string]struct{}{
	".venv": {}, "venv": {}, "__pycache__": {}, ".pytest_cache": {},
}

// preserveUserFiles 对齐 release_install._preserve_user_files
func preserveUserFiles(ctx context.Context, source, target string) error {
	if err := checkTree(source); err != nil {
		return err
	}
	names := map[string]struct{}{"config.toml": {}, "config_back": {}, "data": {}}
	gitDirectory := filepath.Join(source, ".git")
	if info, err := os.Stat(gitDirectory); err == nil && info.IsDir() {
		modified, err := runPluginGit(ctx, source, "diff", "HEAD", "--name-only", "-z")
		if err != nil {
			return err
		}
		changedCode := make([]string, 0, 4)
		for _, name := range strings.Split(modified, "\x00") {
			if name == "" || name == "config.toml" || name == releaseReceiptName {
				continue
			}
			changedCode = append(changedCode, name)
		}
		if len(changedCode) > 0 {
			return conflictError("插件存在本地代码修改，请先处理：%s", strings.Join(changedCode, ", "))
		}
		// 同时保留未跟踪和被忽略的数据文件
		untracked, err := runPluginGit(ctx, source, "ls-files", "--others", "-z")
		if err != nil {
			return err
		}
		for _, name := range strings.Split(untracked, "\x00") {
			if name == "" || name == releaseReceiptName {
				continue
			}
			skip := false
			for _, part := range strings.Split(strings.ReplaceAll(name, "\\", "/"), "/") {
				if _, ok := preservedSkipDirectories[part]; ok {
					skip = true
					break
				}
			}
			if !skip {
				names[name] = struct{}{}
			}
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		leftParts := len(strings.Split(strings.ReplaceAll(ordered[i], "\\", "/"), "/"))
		rightParts := len(strings.Split(strings.ReplaceAll(ordered[j], "\\", "/"), "/"))
		if leftParts != rightParts {
			return leftParts < rightParts
		}
		return ordered[i] < ordered[j]
	})

	copied := make([]string, 0, len(ordered))
	for _, name := range ordered {
		if relativeWithinAny(name, copied) {
			continue
		}
		original, err := resolvePluginFilePath(source, name, true)
		if err != nil {
			return err
		}
		if !pathExists(original) {
			continue
		}
		destination, err := resolvePluginFilePath(target, name, true)
		if err != nil {
			return err
		}
		if pathExists(destination) {
			if name != "config.toml" {
				return conflictError("新版本代码与本地数据路径冲突：%s", name)
			}
			info, statErr := os.Stat(destination)
			if statErr != nil || info.IsDir() {
				return conflictError("新版本 config.toml 不是文件")
			}
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		info, err := os.Lstat(original)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := copyDirectory(original, destination); err != nil {
				return err
			}
		} else if err := copyFile(original, destination); err != nil {
			return err
		}
		copied = append(copied, name)
	}
	return nil
}

// relativeWithinAny 判断路径是否已包含在已复制列表内
func relativeWithinAny(name string, parents []string) bool {
	normalized := strings.ReplaceAll(name, "\\", "/")
	for _, parent := range parents {
		parentNormalized := strings.ReplaceAll(parent, "\\", "/")
		if normalized == parentNormalized || strings.HasPrefix(normalized, parentNormalized+"/") {
			return true
		}
	}
	return false
}

func copyDirectory(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("拒绝复制符号链接: %s", path)
		}
		return copyFile(path, target)
	})
}

func copyFile(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}
