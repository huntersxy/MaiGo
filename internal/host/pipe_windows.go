//go:build windows

package host

import (
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

// winioDialPipe 用 go-winio 连接命名管道
func winioDialPipe(address string) (net.Conn, error) {
	return winio.DialPipe(address, pointerToDuration(5*time.Second))
}

func pointerToDuration(value time.Duration) *time.Duration { return &value }
