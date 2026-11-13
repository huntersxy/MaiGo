package internalchat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/bytedance/sonic"
)

const (
	// DefaultUserName 内部聊天主人默认昵称，对齐 MaiBot DEFAULT_USER_NAME
	DefaultUserName = "人类"
	// profileRelativePath 内部聊天昵称文件，与 MaiBot 实例目录保持一致
	profileRelativePath = "data/internal_chat_profile.json"
	// maxUserNameRunes 昵称最大字符数，对齐 MaiBot _validate_name
	maxUserNameRunes = 64
)

var profileMu sync.Mutex

// ProfilePath 返回内部聊天昵称文件路径，未指定实例目录时使用当前工作目录
func ProfilePath(instanceDir string) (string, error) {
	root := strings.TrimSpace(instanceDir)
	if root == "" {
		abs, err := filepath.Abs(".")
		if err != nil {
			return "", err
		}
		root = abs
	} else {
		root = filepath.Clean(root)
	}
	return filepath.Join(root, filepath.FromSlash(profileRelativePath)), nil
}

// LoadUserName 读取共享主人昵称；文件不存在时用 initialName 首次落盘
func LoadUserName(instanceDir, initialName string) (string, error) {
	profileMu.Lock()
	defer profileMu.Unlock()

	path, err := ProfilePath(instanceDir)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if initialName == "" {
			return DefaultUserName, nil
		}
		return saveUserNameLocked(path, initialName)
	}
	if err != nil {
		return "", fmt.Errorf("读取内部聊天昵称失败: %w", err)
	}
	var payload map[string]any
	if err := sonic.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("解析内部聊天昵称失败: %w", err)
	}
	value, ok := payload["user_name"]
	if !ok {
		return "", fmt.Errorf("内部聊天昵称必须是字符串")
	}
	name, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("内部聊天昵称必须是字符串")
	}
	return validateUserName(name)
}

// SaveUserName 校验并原子保存共享主人昵称
func SaveUserName(instanceDir, name string) (string, error) {
	profileMu.Lock()
	defer profileMu.Unlock()

	path, err := ProfilePath(instanceDir)
	if err != nil {
		return "", err
	}
	return saveUserNameLocked(path, name)
}

func saveUserNameLocked(path, name string) (string, error) {
	normalized, err := validateUserName(name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("创建内部聊天昵称目录失败: %w", err)
	}
	payload, err := sonic.Marshal(map[string]string{"user_name": normalized})
	if err != nil {
		return "", fmt.Errorf("序列化内部聊天昵称失败: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".internal-chat-*.tmp")
	if err != nil {
		return "", fmt.Errorf("创建内部聊天昵称临时文件失败: %w", err)
	}
	tempPath := temporary.Name()
	defer func() {
		if _, statErr := os.Stat(tempPath); statErr == nil {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("设置内部聊天昵称文件权限失败: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("写入内部聊天昵称失败: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("关闭内部聊天昵称临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return "", fmt.Errorf("替换内部聊天昵称失败: %w", err)
	}
	return normalized, nil
}

func validateUserName(name string) (string, error) {
	normalized := strings.TrimSpace(name)
	length := utf8.RuneCountInString(normalized)
	if length < 1 || length > maxUserNameRunes {
		return "", fmt.Errorf("内部聊天昵称长度必须为 1～64 个字符")
	}
	return normalized, nil
}
