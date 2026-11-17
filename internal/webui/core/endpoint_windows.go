//go:build windows

package app

import (
	"net"

	"github.com/Microsoft/go-winio"
)

// listenNamedPipe 在 Windows 命名管道上监听 HTTP
func listenNamedPipe(address string) (net.Listener, error) {
	return winio.ListenPipe(address, nil)
}
