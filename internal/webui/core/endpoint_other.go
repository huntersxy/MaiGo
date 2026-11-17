//go:build !windows

package app

import (
	"net"
)

// listenNamedPipe Unix 平台没有命名管道；调用方不会传入管道地址
func listenNamedPipe(address string) (net.Listener, error) {
	return nil, &net.OpError{Op: "listen", Net: "pipe", Err: errNamedPipeUnsupported{}}
}

type errNamedPipeUnsupported struct{}

func (errNamedPipeUnsupported) Error() string { return "Unix 平台不支持 Windows 命名管道" }
