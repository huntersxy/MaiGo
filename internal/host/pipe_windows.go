//go:build windows

package host

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// winioDialPipe 用 go-winio 连接命名管道，超时由调用方 ctx 控制
func winioDialPipe(ctx context.Context, address string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, address)
}
