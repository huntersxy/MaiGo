//go:build unix

package transport

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// ListenUDS 在 Unix domain socket 路径上监听；path 为空时在系统临时目录自动分配
func ListenUDS(path string) (net.Listener, string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = filepath.Join(os.TempDir(), fmt.Sprintf("maibot-plugin-%d-%s.sock", os.Getpid(), uuid.New().String()[:8]))
	}
	if err := os.RemoveAll(path); err != nil {
		return nil, "", fmt.Errorf("清理 UDS 路径 %q 失败: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, "", fmt.Errorf("创建 UDS 父目录失败: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, "", fmt.Errorf("UDS 监听 %q 失败: %w", path, err)
	}
	_ = os.Chmod(path, 0o600)
	return ln, path, nil
}
