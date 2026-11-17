//go:build windows

package host

import (
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

// listenControl 在 Windows 命名管道上监听宿主控制通道
func listenControl(address string) (net.Listener, error) {
	return winio.ListenPipe(address, nil)
}

// dialControl 连接宿主控制通道
func dialControl(address string) (net.Conn, error) {
	timeout := 5 * time.Second
	return winio.DialPipe(address, &timeout)
}
