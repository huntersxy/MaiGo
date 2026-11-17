package app

// Worker 的监听入口：宿主下发的运行契约优先，未在宿主下运行时沿用 HTTP 多地址绑定。

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"

	"MaiGo/internal/host"
)

// Listen 按运行契约或配置启动 HTTP 服务
//
// 契约存在时只监听宿主分配的进程间通道（Unix socket / Windows 命名管道），
// 对外地址由宿主的代理提供；这样同一工作区可以并行运行多个 Bot。
func (a *App) Listen(ctx context.Context, hosts []string) error {
	if host.HasRuntimeContext() {
		contract, err := host.CurrentRuntimeContext()
		if err != nil {
			return err
		}
		return serveEndpoint(ctx, a.Server(), contract.HTTPEndpoint)
	}
	return a.ListenAndServeMulti(ctx, hosts)
}

func serveEndpoint(ctx context.Context, server *http.Server, endpoint string) error {
	listener, err := listenEndpoint(endpoint)
	if err != nil {
		return err
	}
	defer listener.Close()
	errCh := make(chan error, 1)
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()
	select {
	case <-ctx.Done():
		return server.Shutdown(context.Background())
	case err := <-errCh:
		return err
	}
}

func listenEndpoint(endpoint string) (net.Listener, error) {
	if runtime.GOOS == "windows" && strings.HasPrefix(endpoint, `\\.\pipe\`) {
		return listenNamedPipe(endpoint)
	}
	if info, err := os.Stat(endpoint); err == nil && info.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(endpoint)
	}
	return net.Listen("unix", endpoint)
}
