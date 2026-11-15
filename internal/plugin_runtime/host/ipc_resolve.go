package host

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"MaiGo/internal/plugin_runtime/transport"
)

// IPCListenKind 指定空地址时的默认传输类型
type IPCListenKind int

const (
	IPCListenKindAuto IPCListenKind = iota
	IPCListenKindTCP
	IPCListenKindUDS
	IPCListenKindNamedPipe
)

// IPCListenRequest 传给 RPCServer 的解析后监听参数
type IPCListenRequest struct {
	Address      string
	NamedPipe    bool
	ExplicitKind IPCListenKind
}

// ResolveIPCListenRequest 解析 Runner IPC 地址与传输类型
//
// 显式地址优先；空地址时 MAIBOT_PLUGIN_IPC=pipe 在 Windows 上使用 Named Pipe（对齐 MaiBot factory）
func ResolveIPCListenRequest(configured string) (IPCListenRequest, error) {
	configured = strings.TrimSpace(configured)
	if transport.IsNamedPipeAddress(configured) {
		return IPCListenRequest{Address: configured, NamedPipe: true, ExplicitKind: IPCListenKindNamedPipe}, nil
	}
	if configured != "" {
		switch {
		case strings.Contains(configured, ":"):
			return IPCListenRequest{Address: configured, ExplicitKind: IPCListenKindTCP}, nil
		case strings.Contains(configured, "/") || strings.HasSuffix(configured, ".sock"):
			return IPCListenRequest{Address: configured, ExplicitKind: IPCListenKindUDS}, nil
		default:
			if runtime.GOOS == "windows" {
				return IPCListenRequest{Address: configured, NamedPipe: true, ExplicitKind: IPCListenKindNamedPipe}, nil
			}
			return IPCListenRequest{}, fmt.Errorf("无法识别的传输地址格式: %q", configured)
		}
	}

	mode := strings.ToLower(strings.TrimSpace(os.Getenv(EnvPluginIPC)))
	switch mode {
	case "", "tcp":
		return IPCListenRequest{ExplicitKind: IPCListenKindTCP}, nil
	case "uds":
		return IPCListenRequest{ExplicitKind: IPCListenKindUDS}, nil
	case "pipe":
		if runtime.GOOS != "windows" {
			return IPCListenRequest{}, fmt.Errorf("%s=pipe 仅支持 Windows", EnvPluginIPC)
		}
		return IPCListenRequest{NamedPipe: true, ExplicitKind: IPCListenKindNamedPipe}, nil
	default:
		return IPCListenRequest{}, fmt.Errorf("未知的 %s 值: %q（支持 tcp、uds、pipe）", EnvPluginIPC, mode)
	}
}
