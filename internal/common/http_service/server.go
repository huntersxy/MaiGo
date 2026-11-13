package httpservice

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"

	"go.uber.org/zap"

	"maibot-go/internal/common/utils"
	maibotcfg "maibot-go/internal/config/maibot"
)

// BindHosts 解析并过滤可绑定的监听地址
//
// 对齐 Python http_service/server.py:126-149 的 _filter_bindable_hosts：
// 端口被占用时中止启动，地址在本机不可用（如未启用 IPv6）则跳过并告警。
// 返回 nil 表示存在必须中止的错误（端口冲突）。
func BindHosts() []string {
	address := maibotcfg.ResolveHTTPBindAddress(strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")), nil)
	if len(address.Hosts) == 0 {
		return []string{""}
	}
	bindable := make([]string, 0, len(address.Hosts))
	for _, host := range address.Hosts {
		available, err := utils.CheckPortAvailable(host, address.Port, true)
		if err != nil {
			if utils.IsAddressUnavailableError(err) {
				zap.L().Warn("HTTP 监听地址在本机不可用，已跳过",
					zap.String("host", host), zap.Int("port", address.Port), zap.Error(err))
				continue
			}
			zap.L().Error(utils.BuildPortConflictMessage("HTTP 服务器", host, address.Port))
			for _, hint := range utils.PortConflictHints(address.Port, "http.port (config/bot_config.toml)") {
				zap.L().Error(hint)
			}
			return nil
		}
		if !available {
			zap.L().Error(utils.BuildPortConflictMessage("HTTP 服务器", host, address.Port))
			for _, hint := range utils.PortConflictHints(address.Port, "http.port (config/bot_config.toml)") {
				zap.L().Error(hint)
			}
			return nil
		}
		bindable = append(bindable, host)
	}
	return bindable
}

// LogBindAddresses 输出实际访问地址
//
// 对齐 Python http_service/server.py:151-178：监听通配地址时额外提示 localhost。
func LogBindAddresses(hosts []string, port int) {
	hasV4Localhost, hasV6Localhost := false, false
	hasV4Wildcard, hasV6Wildcard := false, false
	for _, host := range hosts {
		if strings.Contains(host, ":") {
			zap.L().Info("访问地址", zap.String("url", fmt.Sprintf("http://[%s]:%d", host, port)))
			switch host {
			case "::":
				hasV6Wildcard = true
			case "::1":
				hasV6Localhost = true
			}
			continue
		}
		zap.L().Info("访问地址", zap.String("url", fmt.Sprintf("http://%s:%d", host, port)))
		switch host {
		case "0.0.0.0":
			hasV4Wildcard = true
		case "127.0.0.1":
			hasV4Localhost = true
		}
	}
	if !hasV4Wildcard && !hasV6Wildcard {
		return
	}
	local := make([]string, 0, 2)
	if hasV4Wildcard || hasV4Localhost {
		local = append(local, fmt.Sprintf("http://localhost:%d", port))
	}
	if hasV6Wildcard || hasV6Localhost {
		local = append(local, fmt.Sprintf("http://[::1]:%d", port))
	}
	if len(local) > 0 {
		zap.L().Info("本机访问", zap.String("urls", strings.Join(local, "，")))
	}
}

// ListenAndServeMulti 在多个地址上监听同一 handler
//
// 对齐 Python 的多地址绑定：任一地址绑定失败即关闭已开监听并返回错误，
// 避免只绑上了 IPv4 却以为 IPv6 也生效。
// Go 的 tcp 监听 0.0.0.0 会落到双栈 AF_INET6 并连带占用 ::1，导致随后绑定 ::1 冲突；
// Python 显式设置 IPV6_V6ONLY，这里按地址族分别绑定以保持同样的多地址语义。
func ListenAndServeMulti(ctx context.Context, server *http.Server, hosts []string, port int) error {
	if len(hosts) == 0 {
		return errors.New("没有可绑定的 HTTP 监听地址")
	}
	listeners := make([]net.Listener, 0, len(hosts))
	for _, host := range hosts {
		listener, err := listenHost(host, port)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return err
		}
		listeners = append(listeners, listener)
	}
	errCh := make(chan error, len(listeners))
	for _, listener := range listeners {
		go func(ln net.Listener) {
			if serveErr := server.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				errCh <- serveErr
			}
		}(listener)
	}
	select {
	case <-ctx.Done():
		return server.Shutdown(context.Background())
	case err := <-errCh:
		return err
	}
}

// listenHost 按主机地址族绑定，IPv6 监听强制 V6ONLY 以免抢占同端口的 IPv4 地址
func listenHost(host string, port int) (net.Listener, error) {
	network := "tcp4"
	if strings.Contains(host, ":") {
		network = "tcp6"
	}
	listenConfig := net.ListenConfig{
		Control: func(_, address string, conn syscall.RawConn) error {
			if network != "tcp6" {
				return nil
			}
			var controlErr error
			if err := conn.Control(func(fd uintptr) {
				controlErr = setIPv6Only(fd)
			}); err != nil {
				return err
			}
			return controlErr
		},
	}
	return listenConfig.Listen(context.Background(), network, net.JoinHostPort(host, fmt.Sprintf("%d", port)))
}
