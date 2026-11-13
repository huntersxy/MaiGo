package networksecurity

import (
	"net"
	"runtime"
	"strconv"
	"strings"
)

// 本文件对齐 CPython socket.inet_aton 的字面量识别语义
// 权威来源：/home/huntersxy/MaiBot/src/common/network_security.py:41-47 _is_ip_literal
//
// Python 用 inet_aton 判断字面量，它同时接受十进制整数、十六进制、八进制与缩写
// IPv4（127.1、2130706433、0x7f.0.0.1、0177.0.0.1），避免这些写法被当成域名放行
// Go 的 net.ParseIP 只认点分十进制，因此这里按 inet_aton 规则自行解析

// parseInetAton 按 inet_aton 规则解析 IPv4 字面量，失败返回 nil。
// 支持 a.b.c.d / a.b.c / a.b / a，每段可为十进制、0 开头八进制或 0x 十六进制。
//
// 先按严格点分四段解析，未命中再走宽松规则。Windows 的 inet_aton 用
// INADDR_NONE(0xffffffff) 表示错误，因此 "0xffffffff"/"4294967295" 在 Windows 被拒、
// Linux/BSD 接受；这里按 GOOS 区分以匹配 CPython 的宿主行为。
func parseInetAton(hostname string) net.IP {
	if hostname == "" {
		return nil
	}
	if address, ok := parseStrictDottedQuad(hostname); ok {
		return net.IPv4(byte(address>>24), byte(address>>16), byte(address>>8), byte(address)).To4()
	}
	parts := strings.Split(hostname, ".")
	if len(parts) > 4 {
		return nil
	}
	values := make([]uint64, 0, len(parts))
	for _, part := range parts {
		value, ok := parseInetAtonPart(part)
		if !ok {
			return nil
		}
		values = append(values, value)
	}
	// 前 n-1 段各占 8 位，最后一段占剩余位宽
	var address uint64
	for index, value := range values {
		if index == len(values)-1 {
			width := uint(8 * (5 - len(values)))
			if value >= 1<<width {
				return nil
			}
			address |= value
			break
		}
		if value > 0xff {
			return nil
		}
		address |= value << uint(8*(3-index))
	}
	if address > 0xffffffff {
		return nil
	}
	// Windows 的 inet_aton 用 INADDR_NONE 表示失败，该值在这条路径上不可达
	if address == 0xffffffff && runtime.GOOS == "windows" {
		return nil
	}
	// 返回 4 字节形式，与 Python getaddrinfo 的 AF_INET 结果一致
	return net.IPv4(byte(address>>24), byte(address>>16), byte(address>>8), byte(address)).To4()
}

// parseStrictDottedQuad 严格点分四段：四段且每段为十进制 0-255
//
// 对齐 CPython inet_aton 的第一条路径 inet_pton(AF_INET)：它只接受四位点分十进制，
// 不接受 0x/0 前缀，也不受 INADDR_NONE 判定影响。
func parseStrictDottedQuad(hostname string) (uint64, bool) {
	parts := strings.Split(hostname, ".")
	if len(parts) != 4 {
		return 0, false
	}
	var address uint64
	for _, part := range parts {
		if part == "" || len(part) > 3 {
			return 0, false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return 0, false
			}
		}
		value, err := strconv.ParseUint(part, 10, 16)
		if err != nil || value > 0xff {
			return 0, false
		}
		address = address<<8 | value
	}
	return address, true
}

// parseInetAtonPart 解析单段：0x 前缀为十六进制，0 前缀为八进制，其余为十进制
func parseInetAtonPart(part string) (uint64, bool) {
	if part == "" {
		return 0, false
	}
	base := 10
	digits := part
	lowered := strings.ToLower(part)
	if strings.HasPrefix(lowered, "0x") {
		base = 16
		digits = part[2:]
		if digits == "" {
			return 0, false
		}
	} else if len(part) > 1 && part[0] == '0' {
		base = 8
		digits = part[1:]
	}
	value, err := strconv.ParseUint(digits, base, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}
