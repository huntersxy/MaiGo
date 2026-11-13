package utils

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
)

// 端口冲突错误码，对齐 Python src/common/utils/port_checker.py:5
var portConflictErrnos = map[syscall.Errno]struct{}{
	48:    {}, // macOS EADDRINUSE
	98:    {}, // Linux EADDRINUSE
	10048: {}, // Windows WSAEADDRINUSE
}

// 地址不可用错误码，对齐 Python port_checker.py:8
var addressUnavailableErrnos = map[syscall.Errno]struct{}{
	47:    {}, // macOS EAFNOSUPPORT
	49:    {}, // macOS EADDRNOTAVAIL
	97:    {}, // Linux EAFNOSUPPORT
	99:    {}, // Linux EADDRNOTAVAIL
	10047: {}, // Windows WSAEAFNOSUPPORT
	10049: {}, // Windows WSAEADDRNOTAVAIL
}

// IsPortConflictError 判断错误是否为端口占用
// 对齐 Python port_checker.py:22-30
func IsPortConflictError(err error) bool {
	if err == nil {
		return false
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if _, ok := portConflictErrnos[errno]; ok {
			return true
		}
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "address already in use") || strings.Contains(message, "已被占用")
}

// IsAddressUnavailableError 判断错误是否为本机不可用的地址
// 对齐 Python port_checker.py:33-35
func IsAddressUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		_, ok := addressUnavailableErrnos[errno]
		return ok
	}
	return false
}

// CheckPortAvailable 检查端口是否可绑定
// 对齐 Python port_checker.py:38-60：仅端口冲突返回 false，其余绑定错误原样返回
func CheckPortAvailable(host string, port int, allowReuseAddr bool) (bool, error) {
	testHost := normalizeTestHost(host)
	address := net.JoinHostPort(testHost, fmt.Sprintf("%d", port))
	cfg := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			if !allowReuseAddr {
				return nil
			}
			var controlErr error
			if err := c.Control(func(fd uintptr) {
				controlErr = setSockReuseAddr(fd)
			}); err != nil {
				return err
			}
			return controlErr
		},
	}
	listener, err := cfg.Listen(nil, "tcp", address)
	if err == nil {
		_ = listener.Close()
		return true, nil
	}
	if IsPortConflictError(err) {
		return false, nil
	}
	return false, err
}

// normalizeTestHost 对齐 Python port_checker.py:17-20 的测试地址归一化
func normalizeTestHost(host string) string {
	switch strings.TrimSpace(host) {
	case "0.0.0.0":
		return "127.0.0.1"
	case "::":
		return "::1"
	default:
		return strings.TrimSpace(host)
	}
}

// BuildPortConflictMessage 构造端口冲突提示
// 对齐 Python port_checker.py:63-64
func BuildPortConflictMessage(serviceName, host string, port int) string {
	return fmt.Sprintf("%s 启动失败: 端口 %d 已被占用 (host=%s)", serviceName, port, host)
}

// PortConflictHints 返回端口冲突的排查建议
// 对齐 Python port_checker.py:67-84 的 log_port_conflict 文案
func PortConflictHints(port int, configHint string) []string {
	hints := []string{
		fmt.Sprintf("请检查是否有其他程序正在使用端口 %d", port),
	}
	if strings.TrimSpace(configHint) != "" {
		hints = append(hints, "请修改配置项 "+configHint+" 来更改端口")
	}
	hints = append(hints,
		fmt.Sprintf("Windows 用户可以运行: netstat -ano | findstr :%d", port),
		fmt.Sprintf("Linux/Mac 用户可以运行: lsof -i :%d", port),
	)
	return hints
}
