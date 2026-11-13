package transport

import (
	"fmt"
	"net"
	"strings"
)

// ListenTCP 在 addr 上监听 TCP 连接；addr 为空时使用 127.0.0.1:0
//
// 返回 listener 与实际监听地址（host:port），供 Runner 的 MAIBOT_IPC_ADDRESS 使用
func ListenTCP(addr string) (net.Listener, string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("TCP 监听 %q 失败: %w", addr, err)
	}
	actual := ln.Addr().String()
	return ln, actual, nil
}
