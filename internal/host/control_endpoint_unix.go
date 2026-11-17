//go:build !windows

package host

import (
	"net"
	"os"
)

// listenControl 在 Unix socket 上监听宿主控制通道
func listenControl(address string) (net.Listener, error) {
	if info, err := os.Stat(address); err == nil && info.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(address)
	}
	listener, err := net.Listen("unix", address)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(address, 0o600)
	return listener, nil
}

// dialControl 连接宿主控制通道
func dialControl(address string) (net.Conn, error) {
	return net.Dial("unix", address)
}
