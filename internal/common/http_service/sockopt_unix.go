//go:build !windows

package httpservice

import "syscall"

// setIPv6Only 让 IPv6 监听独占端口，避免抢占同端口 IPv4 绑定（对齐 Python IPV6_V6ONLY）
func setIPv6Only(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_V6ONLY, 1)
}
