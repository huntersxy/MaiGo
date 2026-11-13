package app

import (
	"context"

	"maibot-go/internal/common/http_service"
)

// BindHosts 返回当前应监听的地址列表
//
// 实现在 common/http_service（对齐 Python src/common/http_service/server.py
// 的 _filter_bindable_hosts）。
func (a *App) BindHosts() []string {
	return httpservice.BindHosts()
}

// LogBindAddresses 输出访问地址
//
// 对齐 Python server.py 的 _log_bind_addresses。
func (a *App) LogBindAddresses(hosts []string) {
	httpservice.LogBindAddresses(hosts, a.boot.ServerPort)
}

// ListenAndServeMulti 在多个地址上监听同一 handler
func (a *App) ListenAndServeMulti(ctx context.Context, hosts []string) error {
	return httpservice.ListenAndServeMulti(ctx, a.Server(), hosts, a.boot.ServerPort)
}
