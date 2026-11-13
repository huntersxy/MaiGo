package networksecurity

import "net"

// 本文件对齐 CPython 3.13 ipaddress 的地址分类表
// 权威来源：/usr/lib/python3.13/ipaddress.py 的 _IPv4Constants / _IPv6Constants
// 调用语义见 /home/huntersxy/MaiBot/src/common/network_security.py:50-64
//
// Python 的 IPv6Address 在 is_private/is_reserved/is_link_local/is_multicast/
// is_unspecified/is_loopback 上先判断 ipv4_mapped（::ffff:0:0/96）并委托给 IPv4，
// 因此 Go 侧统一用 To4() 归一化：纯 IPv4 与 IPv4-mapped IPv6 都走 IPv4 表

var (
	// fakeIPNetwork 对齐 allow_fake_ip 放行的 198.18.0.0/15
	fakeIPNetwork = mustCIDR("198.18.0.0/15")

	// IPv4 分类网段
	ipv4LinkLocalNetwork = mustCIDR("169.254.0.0/16")
	ipv4LoopbackNetwork  = mustCIDR("127.0.0.0/8")
	ipv4MulticastNetwork = mustCIDR("224.0.0.0/4")

	// ipv4ReservedNetworks 对齐 _IPv4Constants._reserved_network
	ipv4ReservedNetworks = []*net.IPNet{mustCIDR("240.0.0.0/4")}

	// ipv4PrivateNetworks 对齐 _IPv4Constants._private_networks
	ipv4PrivateNetworks = []*net.IPNet{
		mustCIDR("0.0.0.0/8"),
		mustCIDR("10.0.0.0/8"),
		mustCIDR("127.0.0.0/8"),
		mustCIDR("169.254.0.0/16"),
		mustCIDR("172.16.0.0/12"),
		mustCIDR("192.0.0.0/24"),
		mustCIDR("192.0.0.170/31"),
		mustCIDR("192.0.2.0/24"),
		mustCIDR("192.168.0.0/16"),
		mustCIDR("198.18.0.0/15"),
		mustCIDR("198.51.100.0/24"),
		mustCIDR("203.0.113.0/24"),
		mustCIDR("240.0.0.0/4"),
		mustCIDR("255.255.255.255/32"),
	}

	// ipv4PrivateNetworkExceptions 对齐 _IPv4Constants._private_networks_exceptions
	ipv4PrivateNetworkExceptions = []*net.IPNet{
		mustCIDR("192.0.0.9/32"),
		mustCIDR("192.0.0.10/32"),
	}

	// IPv6 分类网段
	ipv6LinkLocalNetwork = mustCIDR("fe80::/10")
	ipv6MulticastNetwork = mustCIDR("ff00::/8")

	// ipv6ReservedNetworks 对齐 _IPv6Constants._reserved_networks
	// 其中 ::/8 覆盖 64:ff9b::/96，因此 NAT64 映射的云元数据地址
	// （如 64:ff9b::a9fe:a9fe）按 Python 语义直接判为保留地址
	ipv6ReservedNetworks = []*net.IPNet{
		mustCIDR("::/8"),
		mustCIDR("100::/8"),
		mustCIDR("200::/7"),
		mustCIDR("400::/6"),
		mustCIDR("800::/5"),
		mustCIDR("1000::/4"),
		mustCIDR("4000::/3"),
		mustCIDR("6000::/3"),
		mustCIDR("8000::/3"),
		mustCIDR("a000::/3"),
		mustCIDR("c000::/3"),
		mustCIDR("e000::/4"),
		mustCIDR("f000::/5"),
		mustCIDR("f800::/6"),
		mustCIDR("fe00::/9"),
	}

	// ipv6PrivateNetworks 对齐 _IPv6Constants._private_networks
	// ::ffff:0:0/96 在 Python 中会被 ipv4_mapped 短路，这里保留表项以对齐权威表
	ipv6PrivateNetworks = []*net.IPNet{
		mustCIDR("::1/128"),
		mustCIDR("::/128"),
		mustCIDR("::ffff:0:0/96"),
		mustCIDR("64:ff9b:1::/48"),
		mustCIDR("100::/64"),
		mustCIDR("2001::/23"),
		mustCIDR("2001:db8::/32"),
		mustCIDR("2002::/16"),
		mustCIDR("3fff::/20"),
		mustCIDR("fc00::/7"),
		mustCIDR("fe80::/10"),
	}

	// ipv6PrivateNetworkExceptions 对齐 _IPv6Constants._private_networks_exceptions
	ipv6PrivateNetworkExceptions = []*net.IPNet{
		mustCIDR("2001:1::1/128"),
		mustCIDR("2001:1::2/128"),
		mustCIDR("2001:3::/32"),
		mustCIDR("2001:4:112::/48"),
		mustCIDR("2001:20::/28"),
		mustCIDR("2001:30::/28"),
	}
)

// formatIP 对齐 Python str(ip_address)：IPv4-mapped IPv6 保留 ::ffff: 前缀
// Go 的 net.IP.String() 会把 ::ffff:192.0.2.1 折叠成 192.0.2.1，诊断信息会失真
//
// 依赖不变量：resolveHostAddresses 对纯 IPv4 一律产出 4 字节形式
// （parseInetAton 与 net.DefaultResolver 的 AF_INET 结果都是 4 字节），
// 因此 16 字节且 To4() 非 nil 只可能是真正的 IPv4-mapped IPv6 字面量
func formatIP(address net.IP) string {
	if len(address) == net.IPv6len {
		if v4 := address.To4(); v4 != nil {
			return "::ffff:" + v4.String()
		}
	}
	return address.String()
}

// containsNetwork 判断地址是否落在任一网段内
func containsNetwork(networks []*net.IPNet, address net.IP) bool {
	for _, network := range networks {
		if network.Contains(address) {
			return true
		}
	}
	return false
}

// isLinkLocal 对齐 is_link_local
func isLinkLocal(address net.IP) bool {
	if v4 := address.To4(); v4 != nil {
		return ipv4LinkLocalNetwork.Contains(v4)
	}
	return ipv6LinkLocalNetwork.Contains(address)
}

// isMulticast 对齐 is_multicast
func isMulticast(address net.IP) bool {
	if v4 := address.To4(); v4 != nil {
		return ipv4MulticastNetwork.Contains(v4)
	}
	return ipv6MulticastNetwork.Contains(address)
}

// isReserved 对齐 is_reserved
func isReserved(address net.IP) bool {
	if v4 := address.To4(); v4 != nil {
		return containsNetwork(ipv4ReservedNetworks, v4)
	}
	return containsNetwork(ipv6ReservedNetworks, address)
}

// isUnspecified 对齐 is_unspecified
func isUnspecified(address net.IP) bool {
	if v4 := address.To4(); v4 != nil {
		return v4.Equal(net.IPv4zero)
	}
	return isZeroIPv6(address)
}

// isLoopback 对齐 is_loopback
func isLoopback(address net.IP) bool {
	if v4 := address.To4(); v4 != nil {
		return ipv4LoopbackNetwork.Contains(v4)
	}
	return isIPv6Loopback(address)
}

// isPrivate 对齐 is_private
func isPrivate(address net.IP) bool {
	if v4 := address.To4(); v4 != nil {
		return containsNetwork(ipv4PrivateNetworks, v4) && !containsNetwork(ipv4PrivateNetworkExceptions, v4)
	}
	return containsNetwork(ipv6PrivateNetworks, address) && !containsNetwork(ipv6PrivateNetworkExceptions, address)
}

// isZeroIPv6 判断是否为全零的 :: 地址
func isZeroIPv6(address net.IP) bool {
	if len(address) != net.IPv6len {
		return false
	}
	for _, value := range address {
		if value != 0 {
			return false
		}
	}
	return true
}

// isIPv6Loopback 判断是否为 ::1
func isIPv6Loopback(address net.IP) bool {
	if len(address) != net.IPv6len {
		return false
	}
	for index := 0; index < net.IPv6len-1; index++ {
		if address[index] != 0 {
			return false
		}
	}
	return address[net.IPv6len-1] == 1
}
