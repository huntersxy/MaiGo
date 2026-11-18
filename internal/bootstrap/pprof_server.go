package bootstrap

// 可选的内存剖析端点：只在显式设置 MAIBOT_PPROF_ADDR 时启动，
// 默认监听回环地址，不随镜像暴露到容器外。

import (
	"context"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
)

// startPprofServer 按 MAIBOT_PPROF_ADDR 启动 pprof；未配置时返回空操作。
func startPprofServer() (stop func(context.Context) error) {
	addr := strings.TrimSpace(os.Getenv("MAIBOT_PPROF_ADDR"))
	if addr == "" {
		return func(context.Context) error { return nil }
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		zap.L().Warn("pprof 监听失败，内存剖析不可用", zap.String("addr", addr), zap.Error(err))
		return func(context.Context) error { return nil }
	}
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			zap.L().Warn("pprof 服务异常退出", zap.Error(serveErr))
		}
	}()
	zap.L().Info("pprof 已启用", zap.String("addr", addr))
	return server.Shutdown
}
