//go:build windows

package transport

import (
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
)

// ListenNamedPipe 在 Windows Named Pipe 上监听；pipeName 为空时自动分配唯一管道名
func ListenNamedPipe(pipeName string) (net.Listener, string, error) {
	path := NormalizePipeAddress(pipeName)
	ln, err := winio.ListenPipe(path, nil)
	if err != nil {
		return nil, "", fmt.Errorf("Named Pipe 监听 %q 失败: %w", path, err)
	}
	return ln, path, nil
}

// DialNamedPipe 连接到 Host 监听的 Named Pipe（Runner 侧或测试用）
func DialNamedPipe(address string) (net.Conn, error) {
	path := NormalizePipeAddress(address)
	conn, err := winio.DialPipe(path, nil)
	if err != nil {
		return nil, fmt.Errorf("Named Pipe 连接 %q 失败: %w", path, err)
	}
	return conn, nil
}
