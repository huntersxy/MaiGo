package host

import "sync/atomic"

// defaultHost 记录当前进程最近构造的插件 Host，供非 Host 注入链的端口按需访问
var defaultHost atomic.Pointer[Host]

// setDefaultHost 在 NewHost 成功时更新进程级 Host
func setDefaultHost(host *Host) {
	defaultHost.Store(host)
}

// DefaultHost 返回当前进程的插件 Host；未初始化时返回 nil
func DefaultHost() *Host {
	return defaultHost.Load()
}
