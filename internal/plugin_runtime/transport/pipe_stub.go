//go:build !windows

package transport

import (
	"fmt"
	"net"
)

// ListenNamedPipe 非 Windows 平台不可用
func ListenNamedPipe(string) (net.Listener, string, error) {
	return nil, "", fmt.Errorf("Named Pipe 仅支持 Windows 平台")
}

// DialNamedPipe 非 Windows 平台不可用
func DialNamedPipe(string) (net.Conn, error) {
	return nil, fmt.Errorf("Named Pipe 仅支持 Windows 平台")
}
