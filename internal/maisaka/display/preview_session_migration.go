package display

// 历史会话哈希目录迁移：早期预览目录直接使用会话 MD5，WebUI 无法按平台会话解析。
// 目录名改为与 Python 一致的平台命名后，把已有文件并入同一目录，避免同一聊天分裂成两条会话。

import (
	"os"
	"path/filepath"
	"regexp"
)

var previewSessionHashPattern = regexp.MustCompile("^[0-9a-f]{32}$")

// MigrateLegacySessionDirs 把历史哈希会话目录并入平台命名目录，返回已迁移的目录数
func (l *PromptPreviewLogger) MigrateLegacySessionDirs() (int, error) {
	if l == nil {
		return 0, nil
	}
	lookup := defaultPreviewSessionLookup()
	if lookup == nil {
		return 0, nil
	}
	stages, err := os.ReadDir(l.BaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	migrated := 0
	for _, stage := range stages {
		if !stage.IsDir() {
			continue
		}
		stageDir := filepath.Join(l.BaseDir, stage.Name())
		sessions, err := os.ReadDir(stageDir)
		if err != nil {
			continue
		}
		for _, session := range sessions {
			if !session.IsDir() || !previewSessionHashPattern.MatchString(session.Name()) {
				continue
			}
			target := BuildPreviewChatDirName(session.Name(), lookup)
			if target == session.Name() {
				continue
			}
			moved, err := mergeSessionDir(filepath.Join(stageDir, session.Name()), filepath.Join(stageDir, target))
			if err != nil {
				return migrated, err
			}
			if moved {
				migrated++
			}
		}
	}
	return migrated, nil
}

// mergeSessionDir 把历史目录里的文件并入目标目录，同名文件保留目标侧
func mergeSessionDir(source, target string) (bool, error) {
	entries, err := os.ReadDir(source)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		sourcePath := filepath.Join(source, entry.Name())
		destination := filepath.Join(target, entry.Name())
		if _, err := os.Stat(destination); err == nil {
			// 目标目录已有同名记录，保留目标侧并清掉历史副本
			if err := os.Remove(sourcePath); err != nil {
				return false, err
			}
			continue
		}
		if err := os.Rename(sourcePath, destination); err != nil {
			return false, err
		}
	}
	remaining, err := os.ReadDir(source)
	if err != nil {
		return true, err
	}
	if len(remaining) > 0 {
		return false, nil
	}
	return true, os.Remove(source)
}
